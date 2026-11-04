package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullstorydev/grpcurl"
	"github.com/golang/protobuf/jsonpb"
	"github.com/golang/protobuf/proto"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

type grpcEvents struct {
	collector *streamCollector
	format    grpcurl.Formatter
	cancel    context.CancelFunc
	err       error
	status    *status.Status
}

func (h *grpcEvents) OnResolveMethod(*desc.MethodDescriptor) {}
func (h *grpcEvents) OnSendHeaders(metadata.MD)              {}
func (h *grpcEvents) OnReceiveHeaders(headers metadata.MD) {
	for key, values := range headers {
		for _, value := range values {
			h.collector.response.Headers.Add(key, value)
		}
	}
}
func (h *grpcEvents) OnReceiveResponse(message proto.Message) {
	if h.err != nil {
		return
	}
	data, err := h.format(message)
	if err == nil {
		err = h.collector.append([]byte(data))
	}
	if err != nil {
		h.err = err
		h.cancel()
	}
}
func (h *grpcEvents) OnReceiveTrailers(result *status.Status, trailers metadata.MD) {
	h.status = result
	h.collector.response.Status, h.collector.response.StatusText = int(result.Code()), result.Code().String()
	for key, values := range trailers {
		for _, value := range values {
			h.collector.response.Headers.Add("Grpc-Trailer-"+key, value)
		}
	}
}

func (s *Session) sendGRPC(ctx context.Context, r *collection.Request, sent *script.Request, res *Result, scope *vars.Scope) (*script.Response, error) {
	ctx, cancel := s.streamContext(ctx, r)
	defer cancel()
	auth := s.resolvedAuth(r, scope)
	switch auth.Mode {
	case "none", "basic", "bearer", "oauth2":
	case "apikey":
		if auth.Placement == "query" {
			return nil, errors.New("gRPC API keys must be placed in metadata headers")
		}
	default:
		return nil, fmt.Errorf("gRPC does not support %s HTTP authentication", auth.Mode)
	}
	u, err := url.Parse(sent.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid gRPC endpoint URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "grpc" && u.Scheme != "grpcs" {
		return nil, fmt.Errorf("unsupported gRPC endpoint scheme %q", u.Scheme)
	}
	if u.RawQuery != "" {
		return nil, errors.New("gRPC endpoint cannot contain a query string; use metadata for credentials")
	}
	tlsEnabled := u.Scheme == "https" || u.Scheme == "grpcs"
	base, ok := s.Client.Transport.(*http.Transport)
	if !ok {
		return nil, errors.New("gRPC requires an HTTP transport for TLS and proxy configuration")
	}
	t := &requestTransport{base: base, session: s, scope: scope}
	probeURL := *u
	probeURL.Scheme = "http"
	if tlsEnabled {
		probeURL.Scheme = "https"
	}
	probe := &http.Request{URL: &probeURL, Header: http.Header{}}
	transport, err := t.makeTransport(probe)
	if err != nil {
		return nil, err
	}
	defer transport.CloseIdleConnections()
	var secure credentials.TransportCredentials = insecure.NewCredentials()
	if tlsEnabled {
		secure = credentials.NewTLS(transport.TLSClientConfig)
	}
	options := []grpc.DialOption{grpc.WithTransportCredentials(secure), grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
		return t.dialGRPCProxy(ctx, transport, probe, address)
	})}
	if authority, ok := sent.Header("Host"); ok {
		options = append(options, grpc.WithAuthority(authority))
	}
	prefix := strings.TrimSuffix(u.EscapedPath(), "/")
	if prefix != "" {
		options = append(options, grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, response any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoke(ctx, prefix+method, request, response, cc, opts...)
		}), grpc.WithStreamInterceptor(func(ctx context.Context, description *grpc.StreamDesc, cc *grpc.ClientConn, method string, stream grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return stream(ctx, description, cc, prefix+method, opts...)
		}))
	}
	connection, err := grpc.NewClient("passthrough:///"+u.Host, options...)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	var headers []string
	for _, header := range sent.Headers {
		switch strings.ToLower(header.Name) {
		case "host", "content-type", "connection", "upgrade", "transfer-encoding":
			continue
		}
		headers = append(headers, header.Name+": "+header.Value)
	}
	source, err := s.grpcDescriptors(ctx, r, scope, connection, headers)
	if err != nil {
		return nil, err
	}
	resolver := grpcurl.AnyResolverFromDescriptorSource(source)
	handler := &grpcEvents{collector: newStreamCollector("grpc", sent.URL, s.OnStream), format: grpcurl.NewJSONFormatter(false, resolver), cancel: cancel}
	messages := r.Body.Messages
	if sent.Body != "" {
		messages = []collection.Message{{Type: "json", Data: sent.Body}}
	}
	index := 0
	supply := func(message proto.Message) error {
		if index >= len(messages) {
			return io.EOF
		}
		data := scope.Interpolate(messages[index].Data)
		index++
		if strings.TrimSpace(data) == "" {
			data = "{}"
		}
		return (&jsonpb.Unmarshaler{AnyResolver: resolver}).Unmarshal(strings.NewReader(data), message)
	}
	err = grpcurl.InvokeRPC(ctx, source, connection, strings.TrimPrefix(sent.Method, "/"), headers, handler, supply)
	response := handler.collector.snapshot()
	if handler.err != nil {
		return response, handler.err
	}
	if err != nil {
		return response, err
	}
	if handler.status != nil && handler.status.Err() != nil {
		return response, handler.status.Err()
	}
	return response, nil
}

func (s *Session) grpcDescriptors(ctx context.Context, r *collection.Request, scope *vars.Scope, connection *grpc.ClientConn, headers []string) (grpcurl.DescriptorSource, error) {
	resolve := func(path string) string {
		path = scope.Interpolate(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.Collection.Dir, path)
		}
		return path
	}
	imports := []string{s.Collection.Dir}
	for _, path := range s.Collection.Config.Protobuf.ImportPaths {
		if !path.Disabled && (path.Enabled == nil || *path.Enabled) {
			imports = append(imports, resolve(path.Path))
		}
	}
	var files []string
	if r.ProtoFilePath != "" {
		files = append(files, resolve(r.ProtoFilePath))
	}
	for _, path := range s.Collection.Config.Protobuf.ProtoFiles {
		if path.Disabled || path.Enabled != nil && !*path.Enabled {
			continue
		}
		file := resolve(path.Path)
		info, err := os.Stat(file)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			if err := filepath.WalkDir(file, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() && filepath.Ext(path) == ".proto" {
					files = append(files, path)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		} else {
			files = append(files, file)
		}
	}
	for _, file := range files {
		imports = append(imports, filepath.Dir(file))
	}
	if len(files) > 0 {
		return grpcurl.DescriptorSourceFromProtoFiles(imports, files...)
	}
	reflectionContext := metadata.NewOutgoingContext(ctx, grpcurl.MetadataFromHeaders(headers))
	reflection := grpcreflect.NewClientAuto(reflectionContext, connection)
	context.AfterFunc(ctx, reflection.Reset)
	return grpcurl.DescriptorSourceFromServer(reflectionContext, reflection), nil
}
