package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

// draft is the request before interpolation, as before-request scripts see it.
func (s *Session) draft(r *collection.Request, chain []*collection.Folder, res *Result) *script.Request {
	d := &script.Request{Name: r.Name, Method: r.Method, URL: r.URL}
	if r.Settings.TimeoutMS > 0 {
		d.Timeout = time.Duration(r.Settings.TimeoutMS) * time.Millisecond
	}
	for _, f := range chain {
		mergeHeaders(d, f.Defaults.Headers)
	}
	mergeHeaders(d, r.Headers)
	// Scripts can inspect and replace bearer and header API key authentication.
	// Query credentials are added after interpolation so delimiters stay in the value.
	switch auth, _ := EffectiveAuth(r); auth.Mode {
	case "none", "basic", "digest", "ntlm", "oauth1", "oauth2", "awsv4", "wsse", "akamai-edgegrid":
	case "bearer":
		d.SetHeader("Authorization", "Bearer "+auth.Token)
	case "apikey":
		if auth.Placement != "query" {
			d.SetHeader(auth.Key, auth.Value)
		}
	default:
		res.Warnings = append(res.Warnings, fmt.Sprintf("auth %q is not supported; request will not be sent", auth.Mode))
	}
	switch r.Body.Mode {
	case "json", "text", "xml", "sparql", "graphql":
		d.Body = r.Body.Data
		if _, ok := d.Header("Content-Type"); !ok {
			d.Headers = append(d.Headers, script.Header{Name: "Content-Type", Value: contentTypes[r.Body.Mode]})
		}
	case "form-urlencoded":
		if _, ok := d.Header("Content-Type"); !ok {
			d.Headers = append(d.Headers, script.Header{Name: "Content-Type", Value: "application/x-www-form-urlencoded"})
		}
	case "multipart-form", "grpc", "websocket", "none", "":
	default:
		res.Warnings = append(res.Warnings, fmt.Sprintf("body type %q is not supported; sent without a body", r.Body.Mode))
	}
	return d
}

var contentTypes = map[string]string{
	"json":    "application/json",
	"text":    "text/plain",
	"xml":     "application/xml",
	"sparql":  "application/sparql-query",
	"graphql": "application/json",
}

func mergeHeaders(d *script.Request, kvs []collection.KV) {
	for _, h := range kvs {
		if h.Enabled && h.Name != "" {
			d.SetHeader(h.Name, h.Value)
		}
	}
}

// finalize interpolates the draft and applies auth, path parameters, the form body and URL encoding.
func (s *Session) finalize(r *collection.Request, chain []*collection.Folder, d *script.Request, scope *vars.Scope, res *Result) (*script.Request, error) {
	in := scope.Interpolate
	out := &script.Request{Name: d.Name, Method: strings.ToUpper(in(d.Method)), Timeout: d.Timeout, Body: in(d.Body)}
	if r.Kind == "grpc" {
		out.Method = in(d.Method)
	}
	unresolved := scope.Unresolved(d.Method + "\n" + d.URL + "\n" + d.Body)
	for _, h := range d.Headers {
		out.Headers = append(out.Headers, script.Header{Name: in(h.Name), Value: in(h.Value)})
		unresolved = append(unresolved, scope.Unresolved(h.Name+"\n"+h.Value)...)
	}
	// A form body is encoded from its fields in their order, unless a script replaced the body.
	if r.Body.Mode == "form-urlencoded" && d.Body == "" {
		var form []string
		for _, f := range r.Body.Form {
			if f.Enabled {
				form = append(form, url.QueryEscape(in(f.Name))+"="+url.QueryEscape(in(f.Value)))
				unresolved = append(unresolved, scope.Unresolved(f.Name+"\n"+f.Value)...)
			}
		}
		out.Body = strings.Join(form, "&")
	}
	if r.Body.Mode == "multipart-form" && d.Body == "" {
		if err := s.multipartBody(r, out, scope); err != nil {
			return out, err
		}
		for _, field := range r.Body.Form {
			if field.Enabled {
				unresolved = append(unresolved, scope.Unresolved(field.Name+"\n"+field.Value+"\n"+field.ContentType)...)
			}
		}
	}

	raw := in(d.URL)
	for _, p := range r.Params {
		if p.Type == "path" && p.Enabled {
			raw = replacePathParam(raw, p.Name, in(p.Value), r.Settings.EncodeURL)
			unresolved = append(unresolved, scope.Unresolved(p.Value)...)
		}
	}
	if !hasScheme.MatchString(raw) {
		raw = "http://" + raw
	}
	if r.Settings.EncodeURL {
		raw = encodeQuery(raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return out, fmt.Errorf("invalid URL %q: %w", raw, err)
	}

	if auth, _ := EffectiveAuth(r); auth.Mode == "basic" {
		cred := base64.StdEncoding.EncodeToString([]byte(in(auth.Username) + ":" + in(auth.Password)))
		out.SetHeader("Authorization", "Basic "+cred)
		unresolved = append(unresolved, scope.Unresolved(auth.Username+"\n"+auth.Password)...)
	}
	out.URL = u.String()
	if auth, _ := EffectiveAuth(r); auth.Mode == "apikey" && auth.Placement == "query" {
		query := encodeComponent(in(auth.Key)) + "=" + encodeComponent(in(auth.Value))
		if u.RawQuery != "" {
			u.RawQuery += "&" + query
		} else {
			u.RawQuery = query
		}
		out.URL = u.String()
		unresolved = append(unresolved, scope.Unresolved(auth.Key+"\n"+auth.Value)...)
	}
	if r.Body.Mode == "graphql" {
		if err := graphqlBody(r, out, scope); err != nil {
			return out, err
		}
		unresolved = append(unresolved, scope.Unresolved(r.Body.Variables+"\n"+r.Body.OperationName)...)
	}
	slices.Sort(unresolved)
	res.Unresolved = slices.Compact(unresolved)
	return out, nil
}

var hasScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

// replacePathParam fills the :name segment of the URL path.
func replacePathParam(raw, name, value string, encode bool) string {
	if encode {
		value = encodeComponent(value)
	}
	head, query, hasQuery := strings.Cut(raw, "?")
	segs := strings.Split(head, "/")
	for i, seg := range segs {
		if seg == ":"+name {
			segs[i] = value
		}
	}
	head = strings.Join(segs, "/")
	if hasQuery {
		return head + "?" + query
	}
	return head
}

// encodeQuery re-encodes each query key and value like encodeURIComponent, without double-encoding.
func encodeQuery(raw string) string {
	head, query, ok := strings.Cut(raw, "?")
	if !ok || query == "" {
		return raw
	}
	query, frag, hasFrag := strings.Cut(query, "#")
	parts := strings.Split(query, "&")
	for i, part := range parts {
		k, v, hasValue := strings.Cut(part, "=")
		parts[i] = encodeComponent(unescape(k))
		if hasValue {
			parts[i] += "=" + encodeComponent(unescape(v))
		}
	}
	out := head + "?" + strings.Join(parts, "&")
	if hasFrag {
		out += "#" + frag
	}
	return out
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// encodeComponent is JavaScript's encodeURIComponent.
func encodeComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func (s *Session) send(ctx context.Context, sent *script.Request, settings collection.Settings, res *Result, scope *vars.Scope) (*script.Response, error) {
	timeout := sent.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if sent.Body != "" {
		body = strings.NewReader(sent.Body)
	}
	req, err := http.NewRequestWithContext(ctx, sent.Method, sent.URL, body)
	if err != nil {
		return nil, err
	}
	for _, h := range sent.Headers {
		req.Header.Add(h.Name, h.Value)
	}
	if v, ok := sent.Header("Host"); ok {
		req.Host = v
	}
	client, cleanup, err := s.clientForScope(scope)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	authorized, err := s.authTransport(ctx, res.Request, req, base, scope)
	if err != nil {
		return nil, err
	}
	if signer, ok := authorized.(signingTransport); ok {
		recorded := false
		signer.record = func(actual *http.Request) {
			if recorded {
				return
			}
			recorded = true
			sent.URL = actual.URL.String()
			for name, values := range actual.Header {
				if len(values) > 0 {
					sent.SetHeader(name, strings.Join(values, ", "))
				}
			}
			if body, err := requestBody(actual); err == nil {
				sent.Body = string(body)
			}
		}
		authorized = signer
	}
	client.Transport = originAuthTransport{base: base, authorized: authorized, origin: req.URL, sensitiveHeaders: s.authHeaders(res.Request, scope)}
	client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		if !sameOrigin(redirect.URL, req.URL) {
			redirect.Header.Del("Authorization")
			redirect.Header.Del("X-WSSE")
		}
		switch {
		case !settings.FollowRedirects || settings.MaxRedirects <= 0:
			return http.ErrUseLastResponse
		case len(via) > settings.MaxRedirects:
			return fmt.Errorf("stopped after %d redirects (maxRedirects)", settings.MaxRedirects)
		}
		return nil
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if len(b) > maxBody {
		b = b[:maxBody]
		res.Warnings = append(res.Warnings, fmt.Sprintf("response body cut at %d MiB", maxBody>>20))
	}
	return &script.Response{
		Status:     resp.StatusCode,
		StatusText: strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode))),
		Headers:    resp.Header,
		Body:       b,
		Duration:   time.Since(start),
		URL:        resp.Request.URL.String(),
	}, nil
}
