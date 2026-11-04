package runner

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
)

var grpcTestDescriptorOnce sync.Once
var grpcTestDescriptorError error

const grpcTestProto = `syntax = "proto3";
package brucli.test;
import "google/protobuf/wrappers.proto";
service Echo {
  rpc Unary(google.protobuf.StringValue) returns (google.protobuf.StringValue);
  rpc ServerStream(google.protobuf.StringValue) returns (stream google.protobuf.StringValue);
  rpc ClientStream(stream google.protobuf.StringValue) returns (google.protobuf.StringValue);
  rpc Bidi(stream google.protobuf.StringValue) returns (stream google.protobuf.StringValue);
}
`

func grpcTestServer(t *testing.T, options ...grpc.ServerOption) (*grpc.Server, net.Listener) {
	t.Helper()
	grpcTestDescriptorOnce.Do(func() {
		file := &descriptorpb.FileDescriptorProto{Name: proto.String("brucli_protocol_test.proto"), Package: proto.String("brucli.test"), Syntax: proto.String("proto3"), Dependency: []string{"google/protobuf/wrappers.proto"}}
		service := &descriptorpb.ServiceDescriptorProto{Name: proto.String("Echo")}
		for _, name := range []string{"Unary", "ServerStream", "ClientStream", "Bidi"} {
			service.Method = append(service.Method, &descriptorpb.MethodDescriptorProto{Name: proto.String(name), InputType: proto.String(".google.protobuf.StringValue"), OutputType: proto.String(".google.protobuf.StringValue"), ClientStreaming: proto.Bool(name == "ClientStream" || name == "Bidi"), ServerStreaming: proto.Bool(name == "ServerStream" || name == "Bidi")})
		}
		file.Service = []*descriptorpb.ServiceDescriptorProto{service}
		descriptor, err := protodesc.NewFile(file, protoregistry.GlobalFiles)
		if err != nil {
			grpcTestDescriptorError = err
			return
		}
		grpcTestDescriptorError = protoregistry.GlobalFiles.RegisterFile(descriptor)
	})
	if grpcTestDescriptorError != nil {
		t.Fatal(grpcTestDescriptorError)
	}
	server := grpc.NewServer(options...)
	type serviceInterface interface{}
	check := func(ctx context.Context) {
		if md, ok := metadata.FromIncomingContext(ctx); !ok || len(md.Get("x-test")) != 1 || md.Get("x-test")[0] != "yes" {
			t.Error("gRPC request metadata missing")
		}
	}
	unary := func(_ any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		check(ctx)
		request := new(wrapperspb.StringValue)
		if err := decode(request); err != nil {
			return nil, err
		}
		grpc.SendHeader(ctx, metadata.Pairs("x-initial", "yes"))
		grpc.SetTrailer(ctx, metadata.Pairs("x-trailer", "yes"))
		return wrapperspb.String("reply:" + request.Value), nil
	}
	streamHandler := func(name string) func(any, grpc.ServerStream) error {
		return func(_ any, stream grpc.ServerStream) error {
			check(stream.Context())
			stream.SendHeader(metadata.Pairs("x-initial", "yes"))
			stream.SetTrailer(metadata.Pairs("x-trailer", "yes"))
			var values []string
			for {
				request := new(wrapperspb.StringValue)
				err := stream.RecvMsg(request)
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				values = append(values, request.Value)
				if name == "ServerStream" {
					for i := 0; i < 2; i++ {
						if err := stream.SendMsg(wrapperspb.String(fmt.Sprintf("reply:%s:%d", request.Value, i))); err != nil {
							return err
						}
					}
					return nil
				}
				if name == "Bidi" {
					if err := stream.SendMsg(wrapperspb.String("reply:" + request.Value)); err != nil {
						return err
					}
				}
			}
			if name == "ClientStream" {
				return stream.SendMsg(wrapperspb.String(strings.Join(values, ",")))
			}
			return nil
		}
	}
	service := &grpc.ServiceDesc{ServiceName: "brucli.test.Echo", HandlerType: (*serviceInterface)(nil), Metadata: "brucli_protocol_test.proto", Methods: []grpc.MethodDesc{{MethodName: "Unary", Handler: unary}}}
	for _, name := range []string{"ServerStream", "ClientStream", "Bidi"} {
		service.Streams = append(service.Streams, grpc.StreamDesc{StreamName: name, Handler: streamHandler(name), ClientStreams: name != "ServerStream", ServerStreams: name != "ClientStream"})
	}
	server.RegisterService(service, struct{}{})
	reflection.Register(server)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	return server, listener
}

func TestGRPCCallPatterns(t *testing.T) {
	_, listener := grpcTestServer(t)
	for _, source := range []string{"reflection", "proto"} {
		for _, method := range []string{"Unary", "ServerStream", "ClientStream", "Bidi"} {
			t.Run(source+"/"+method, func(t *testing.T) {
				s, r := authRequest(t, "grpc://"+listener.Addr().String(), collection.Auth{Mode: "none"})
				s.Collection.Root.Defaults.Scripts = collection.Scripts{}
				r.Kind, r.Method = "grpc", "/brucli.test.Echo/"+method
				r.Headers = []collection.KV{{Name: "x-test", Value: "yes", Enabled: true}}
				r.Body = collection.Body{Mode: "grpc", Messages: []collection.Message{{Data: `"{{name}}"`, Type: "json"}}}
				s.Runtime["name"] = "Ada"
				if method == "ClientStream" || method == "Bidi" {
					r.Body.Messages = append(r.Body.Messages, collection.Message{Data: `"Grace"`, Type: "json"})
				}
				if source == "proto" {
					r.ProtoFilePath = "echo.proto"
					if err := os.WriteFile(filepath.Join(s.Collection.Dir, "echo.proto"), []byte(grpcTestProto), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var updates atomic.Int32
				s.OnStream = func(response *script.Response) { updates.Add(1) }
				res := s.Execute(context.Background(), r)
				if res.Err != nil || res.Response.Status != 0 || res.Response.Protocol != "grpc" {
					t.Fatalf("gRPC response: %+v", res)
				}
				if updates.Load() == 0 || res.Response.Headers.Get("X-Initial") != "yes" || res.Response.Headers.Get("Grpc-Trailer-X-Trailer") != "yes" {
					t.Fatalf("gRPC progress or metadata missing: %+v", res.Response)
				}
				var body any
				if err := json.Unmarshal(res.Response.Body, &body); err != nil {
					t.Fatal(err)
				}
				if method == "Unary" && body != "reply:Ada" || method == "ClientStream" && body != "Ada,Grace" {
					t.Fatalf("gRPC body: %s", res.Response.Body)
				}
				if method == "ServerStream" || method == "Bidi" {
					if values, ok := body.([]any); !ok || len(values) != 2 {
						t.Fatalf("gRPC messages: %s", res.Response.Body)
					}
				}
			})
		}
	}
}

func TestGRPCTLS(t *testing.T) {
	https := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := https.TLS.Certificates[0]
	root := https.Certificate().Raw
	https.Close()
	_, listener := grpcTestServer(t, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}})))
	s, r := authRequest(t, "grpcs://"+listener.Addr().String(), collection.Auth{Mode: "none"})
	s.Collection.Root.Defaults.Scripts = collection.Scripts{}
	r.Kind, r.Method = "grpc", "/brucli.test.Echo/Unary"
	r.Headers = []collection.KV{{Name: "x-test", Value: "yes", Enabled: true}}
	r.Body = collection.Body{Mode: "grpc", Messages: []collection.Message{{Data: `"TLS"`}}}
	path := filepath.Join(s.Collection.Dir, "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureTransport(TransportOptions{CACert: path}); err != nil {
		t.Fatal(err)
	}
	if res := s.Execute(context.Background(), r); res.Err != nil || string(res.Response.Body) != `"reply:TLS"` {
		t.Fatalf("TLS gRPC response: %+v", res)
	}
}

func TestGRPCProxy(t *testing.T) {
	_, listener := grpcTestServer(t)
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != listener.Addr().String() {
			t.Errorf("unexpected proxy request: %s %s", r.Method, r.Host)
			w.WriteHeader(400)
			return
		}
		remote, err := net.Dial("tcp", r.Host)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			remote.Close()
			t.Error(err)
			return
		}
		defer client.Close()
		defer remote.Close()
		connects.Add(1)
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		go io.Copy(remote, buffer)
		io.Copy(client, remote)
	}))
	defer proxy.Close()
	s, r := authRequest(t, "grpc://"+listener.Addr().String(), collection.Auth{Mode: "none"})
	s.Collection.Root.Defaults.Scripts = collection.Scripts{}
	r.Kind, r.Method = "grpc", "/brucli.test.Echo/Unary"
	r.Headers = []collection.KV{{Name: "x-test", Value: "yes", Enabled: true}}
	r.Body = collection.Body{Mode: "grpc", Messages: []collection.Message{{Data: `"proxy"`}}}
	if err := s.ConfigureTransport(TransportOptions{ProxyURL: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	if res := s.Execute(context.Background(), r); res.Err != nil || string(res.Response.Body) != `"reply:proxy"` {
		t.Fatalf("proxied gRPC response: %+v", res)
	}
	if connects.Load() != 1 {
		t.Fatalf("gRPC did not reuse the proxy tunnel: %d connections", connects.Load())
	}
}
