// Package runner sends a collection's requests the way Bruno does: variable scopes, inherited auth and
// headers, scripts in sandwich order, post-response variables, assertions and tests.
package runner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
	"golang.org/x/oauth2"
)

// DefaultTimeout applies when a request has no timeout setting.
const DefaultTimeout = 60 * time.Second

// maxBody caps how much of a response body is read.
const maxBody = 64 << 20

// Session is the state shared by the requests of one sitting: the active environment and runtime variables.
type Session struct {
	Collection *collection.Collection
	// Env is the active environment, or nil for none.
	Env       *collection.Environment
	GlobalEnv *collection.Environment
	// Overrides win over the environment's own values, like bru run --env-var.
	Overrides map[string]string
	// Secrets hold secret environment values by environment and variable name; they never reach a file.
	Secrets map[string]map[string]string
	Runtime map[string]any
	// Process is process.env: the OS environment overlaid by the collection's .env.
	Process           map[string]string
	Client            *http.Client
	Transport         TransportOptions
	rootCAs           *x509.CertPool
	tokens            map[string]*oauth2.Token
	Interactive       bool
	OpenBrowser       func(string) error
	StreamInteractive bool
	StreamMessages    int
	StreamTimeout     time.Duration
	StreamInput       <-chan collection.Message
	OnStream          func(*script.Response)
}

// NewSession starts a session on c with environment env (may be nil).
func NewSession(c *collection.Collection, env *collection.Environment, insecure bool) *Session {
	process := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			process[k] = v
		}
	}
	maps.Copy(process, c.DotEnv)
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Session{
		Collection:     c,
		Env:            env,
		Overrides:      map[string]string{},
		Secrets:        map[string]map[string]string{},
		Runtime:        map[string]any{},
		Process:        process,
		Client:         &http.Client{Transport: transport, Jar: jar},
		tokens:         map[string]*oauth2.Token{},
		StreamMessages: 1,
	}
}

// Fork copies the mutable state so requests can run in the background while the original is shown and edited.
func (s *Session) Fork() *Session {
	f := *s
	if s.Env != nil {
		f.Env = s.Env.Clone()
	}
	if s.GlobalEnv != nil {
		f.GlobalEnv = s.GlobalEnv.Clone()
	}
	f.Overrides = maps.Clone(s.Overrides)
	f.Runtime = deepCopy(s.Runtime).(map[string]any)
	f.tokens = maps.Clone(s.tokens)
	f.Secrets = map[string]map[string]string{}
	for env, vals := range s.Secrets {
		f.Secrets[env] = maps.Clone(vals)
	}
	return &f
}

// deepCopy copies the maps and slices of a script value; scripts mutate objects in place, so a
// fork must not share them with the session it came from.
func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(x))
		for k, e := range x {
			c[k] = deepCopy(e)
		}
		return c
	case []any:
		c := make([]any, len(x))
		for i, e := range x {
			c[i] = deepCopy(e)
		}
		return c
	}
	return v
}

// Adopt takes over what a fork's scripts changed: runtime variables, secrets, environment values
// and -env-var overrides a script replaced.
func (s *Session) Adopt(f *Session) {
	s.Runtime = f.Runtime
	s.tokens = f.tokens
	s.Secrets = f.Secrets
	s.Overrides = f.Overrides
	if s.Env != nil && f.Env != nil && s.Env.Path == f.Env.Path {
		s.Env.Vars = f.Env.Vars
	}
}

// Scope builds the variables one request sees.
func (s *Session) Scope(r *collection.Request) *vars.Scope {
	scope := &vars.Scope{
		Global:      map[string]string{},
		Collection:  enabled(s.Collection.Root.Defaults.Vars),
		Environment: s.envVars(),
		Folder:      map[string]string{},
		Request:     map[string]string{},
		Runtime:     s.Runtime,
		Process:     s.Process,
	}
	if s.GlobalEnv != nil {
		for _, value := range s.GlobalEnv.Vars {
			if value.Enabled && value.Secret {
				if secret, ok := s.SecretValues(s.GlobalEnv)[value.Name]; ok {
					scope.Global[value.Name] = secret
				}
			} else if value.Enabled {
				scope.Global[value.Name] = value.Value
			}
		}
	}
	if r != nil {
		for _, f := range r.Chain()[1:] {
			maps.Copy(scope.Folder, enabled(f.Defaults.Vars))
		}
		scope.Request = enabled(r.Vars)
	}
	return scope
}

func (s *Session) envVars() map[string]string {
	out := map[string]string{}
	if s.Env != nil {
		secrets := s.Secrets[s.Env.Name]
		for _, v := range s.Env.Vars {
			if !v.Enabled {
				continue
			}
			if v.Secret {
				if val, ok := secrets[v.Name]; ok {
					out[v.Name] = val
				}
				continue
			}
			out[v.Name] = v.Value
		}
	}
	maps.Copy(out, s.Overrides)
	return out
}

func enabled(kvs []collection.KV) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		if kv.Enabled {
			out[kv.Name] = kv.Value
		}
	}
	return out
}

// Result is the outcome of one request.
type Result struct {
	Request *collection.Request
	// Sent is the request after scripts and interpolation, as it went out.
	Sent     *script.Request
	Response *script.Response
	// Err is a failure to build or send the request; there is no Response then.
	Err          error
	ScriptErrors []string
	Assertions   []script.AssertResult
	Tests        []script.TestResult
	Logs         []string
	// Unresolved lists {{names}} that no scope defines.
	Unresolved []string
	Warnings   []string
	Skipped    bool
	// EnvSet lists bru.setEnvVar calls; Saved reports that the persist: true ones were written to the file.
	EnvSet      []script.EnvSet
	Saved       bool
	NextRequest string
	Stop        bool
}

// Passed reports a sent request whose scripts, assertions and tests all passed.
func (r *Result) Passed() bool { return !r.Skipped && !r.Failed() }

// Failed reports a request that could not be sent, or whose scripts, assertions or tests failed.
// A request skipped by its script has neither passed nor failed, as in bru run.
func (r *Result) Failed() bool {
	if r.Skipped {
		return false
	}
	if r.Err != nil || len(r.ScriptErrors) > 0 {
		return true
	}
	for _, a := range r.Assertions {
		if !a.Passed {
			return true
		}
	}
	for _, t := range r.Tests {
		if !t.Passed {
			return true
		}
	}
	return false
}

// Counts tallies results the way bru run's summary does.
type Counts struct{ Passed, Failed, Skipped int }

// Add counts one result.
func (c *Counts) Add(r *Result) {
	switch {
	case r.Skipped:
		c.Skipped++
	case r.Failed():
		c.Failed++
	default:
		c.Passed++
	}
}

func (c Counts) String() string {
	s := fmt.Sprintf("%d passed, %d failed", c.Passed, c.Failed)
	if c.Skipped > 0 {
		s += fmt.Sprintf(", %d skipped", c.Skipped)
	}
	return s
}

// Execute sends one request with its scripts, post-response variables, assertions and tests.
func (s *Session) Execute(ctx context.Context, r *collection.Request) *Result {
	res := &Result{Request: r}
	scope := s.Scope(r)
	chain := r.Chain()
	sc := &script.Context{Scope: scope, Ctx: ctx, CollectionName: s.Collection.Name, EnvName: s.envName()}
	sc.Req = s.draft(r, chain, res)

	// Bruno runs the before-request scripts as one script: an error stops it and nothing is sent.
	ok := true
	for _, f := range chain {
		if ok = s.runScript(sc, res, f.Defaults.Scripts.Before, "before-request", f); !ok {
			break
		}
	}
	if ok {
		ok = s.runScript(sc, res, r.Scripts.Before, "before-request", nil)
	}
	if !ok {
		res.Err = errors.New("not sent: a before-request script failed")
		s.finish(sc, res)
		return res
	}
	if sc.Skip {
		res.Skipped = true
		s.finish(sc, res)
		return res
	}

	sent, err := s.finalize(r, chain, sc.Req, scope, res)
	res.Sent = sent
	if err != nil {
		res.Err = err
		s.finish(sc, res)
		return res
	}
	if err := s.prepareAuth(ctx, r, sent, scope); err != nil {
		res.Err = err
		s.finish(sc, res)
		return res
	}
	switch r.Kind {
	case "websocket":
		res.Response, res.Err = s.sendWebSocket(ctx, r, sent, res, scope)
	case "grpc":
		res.Response, res.Err = s.sendGRPC(ctx, r, sent, res, scope)
	default:
		res.Response, res.Err = s.send(ctx, sent, r.Settings, res, scope)
	}
	if res.Err != nil {
		s.finish(sc, res)
		return res
	}
	sc.Res = res.Response

	for _, a := range r.Actions {
		if !a.Enabled {
			continue
		}
		v, defined, err := sc.Eval(a.Expression)
		switch {
		case err != nil:
			res.ScriptErrors = append(res.ScriptErrors, fmt.Sprintf("post-response var %s: %v", a.Name, err))
		case !defined:
			// Undefined leaves {{name}} unresolved, as in Bruno.
			delete(scope.Request, a.Name)
			delete(scope.Runtime, a.Name)
		case a.Scope == "request":
			scope.Request[a.Name] = vars.Format(v)
		default:
			scope.Runtime[a.Name] = v
		}
	}
	s.runScript(sc, res, r.Scripts.After, "after-response", nil)
	for _, f := range slices.Backward(chain) {
		s.runScript(sc, res, f.Defaults.Scripts.After, "after-response", f)
	}
	for _, a := range r.Assertions {
		if a.Enabled {
			res.Assertions = append(res.Assertions, sc.Assert(a))
		}
	}
	for _, f := range chain {
		s.runTests(sc, res, f.Defaults.Scripts.Tests, f)
	}
	s.runTests(sc, res, r.Scripts.Tests, nil)
	s.finish(sc, res)
	return res
}

func (s *Session) envName() string {
	if s.Env == nil {
		return ""
	}
	return s.Env.Name
}

// runScript runs one script and reports whether it finished without an error.
func (s *Session) runScript(sc *script.Context, res *Result, code, phase string, f *collection.Folder) bool {
	if err := sc.Run(code); err != nil {
		res.ScriptErrors = append(res.ScriptErrors, fmt.Sprintf("%s script (%s): %v", phase, s.owner(f), err))
		return false
	}
	return true
}

func (s *Session) runTests(sc *script.Context, res *Result, code string, f *collection.Folder) {
	if err := sc.Run(code); err != nil {
		res.ScriptErrors = append(res.ScriptErrors, fmt.Sprintf("tests (%s): %v", s.owner(f), err))
	}
}

func (s *Session) owner(f *collection.Folder) string {
	switch {
	case f == nil:
		return "request"
	case f == s.Collection.Root:
		return "collection"
	}
	return "folder " + f.Name
}

// finish copies what scripts produced into the result and applies bru.setEnvVar to the environment.
func (s *Session) finish(sc *script.Context, res *Result) {
	res.Tests = sc.Tests
	res.Logs = sc.Logs
	res.NextRequest, res.Stop = sc.NextRequest, sc.Stop
	if len(sc.EnvSet) == 0 || s.Env == nil {
		return
	}
	persist := false
	for _, set := range sc.EnvSet {
		persist = persist || set.Persist
		if _, ok := s.Overrides[set.Name]; ok {
			s.Overrides[set.Name] = set.Value
		}
		i := slices.IndexFunc(s.Env.Vars, func(v collection.EnvVar) bool { return v.Name == set.Name })
		if i < 0 {
			s.Env.Vars = append(s.Env.Vars, collection.EnvVar{KV: collection.KV{Name: set.Name, Value: set.Value, Enabled: true}})
			continue
		}
		if s.Env.Vars[i].Secret {
			s.SetSecret(set.Name, set.Value)
			continue
		}
		s.Env.Vars[i].Value = set.Value
	}
	res.EnvSet = sc.EnvSet
	if !persist {
		return
	}
	for _, set := range sc.EnvSet {
		if !set.Persist {
			continue
		}
		if err := s.Env.Persist(set.Name, set.Value); err != nil {
			res.ScriptErrors = append(res.ScriptErrors, "saving environment: "+err.Error())
			return
		}
	}
	res.Saved = true
}

// SetSecret stores a secret value of the active environment for this session.
func (s *Session) SetSecret(name, value string) {
	if s.Env == nil {
		return
	}
	s.SetEnvironmentSecret(s.Env, name, value)
}

func (s *Session) environmentSecretKey(env *collection.Environment) string {
	if env == s.Env {
		return env.Name
	}
	if s.GlobalEnv != nil && (env == s.GlobalEnv || env.Path != "" && env.Path == s.GlobalEnv.Path) {
		return "global:" + env.Name
	}
	return env.Name
}

func (s *Session) SecretValues(env *collection.Environment) map[string]string {
	if env == nil {
		return nil
	}
	return s.Secrets[s.environmentSecretKey(env)]
}

func (s *Session) SetEnvironmentSecret(env *collection.Environment, name, value string) {
	if env == nil {
		return
	}
	key := s.environmentSecretKey(env)
	if s.Secrets == nil {
		s.Secrets = map[string]map[string]string{}
	}
	if s.Secrets[key] == nil {
		s.Secrets[key] = map[string]string{}
	}
	s.Secrets[key][name] = value
}

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

// EffectiveAuth is the auth a request uses after inheritance, and where it came from.
func EffectiveAuth(r *collection.Request) (collection.Auth, string) {
	if r.Auth.Mode != "inherit" {
		return r.Auth, "request"
	}
	chain := r.Chain()
	for i := len(chain) - 1; i >= 0; i-- {
		if a := chain[i].Defaults.Auth; a.Mode != "inherit" {
			if i == 0 {
				return a, "collection"
			}
			return a, "folder " + chain[i].Name
		}
	}
	return collection.Auth{Mode: "none"}, "collection"
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

// Curl renders the sent request as a curl command; Mask it first to hide credentials.
func Curl(sent *script.Request) string {
	var b strings.Builder
	method := sent.Method
	if !token.MatchString(method) {
		method = shellQuote(method)
	}
	fmt.Fprintf(&b, "curl -X %s %s", method, shellQuote(sent.URL))
	for _, h := range sent.Headers {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(h.Name+": "+h.Value))
	}
	if sent.Body != "" {
		fmt.Fprintf(&b, " \\\n  --data-raw %s", shellQuote(sent.Body))
	}
	return b.String()
}

var token = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// LooksSecret reports a variable name that suggests a credential.
func LooksSecret(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "credential", "private"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Mask returns a copy of sent with credentials replaced by ****: Authorization, Proxy-Authorization
// and Cookie headers (keeping the auth scheme), and every value of the request's auth, of secret
// environment variables and of variables whose names look secret, wherever it appears.
func (s *Session) Mask(r *collection.Request, sent *script.Request) *script.Request {
	scope := s.Scope(r)
	queryCredentials := map[string]bool{}
	headerCredentials := map[string]bool{}
	if r != nil {
		auth := s.resolvedAuth(r, scope)
		if auth.Mode == "apikey" {
			if auth.Placement == "query" {
				queryCredentials[scope.Interpolate(auth.Key)] = true
			} else {
				headerCredentials[strings.ToLower(scope.Interpolate(auth.Key))] = true
			}
		} else if auth.Mode == "oauth2" {
			if key, queryPlacement := oauth2QueryKey(auth); queryPlacement {
				queryCredentials[key] = true
			}
		}
	}
	var values []string
	note := func(v string) {
		if v = scope.Interpolate(v); len(v) >= 4 && !slices.Contains(values, v) {
			values = append(values, v, encodeComponent(v), url.QueryEscape(v))
		}
	}
	if r != nil {
		if a, _ := EffectiveAuth(r); a.Mode != "none" {
			note(a.Password)
			note(a.Token)
			note(a.Value)
			for key, value := range a.Options {
				if LooksSecret(key) {
					note(value)
				}
			}
		}
	}
	if s.Env != nil {
		for _, v := range s.Env.Vars {
			if v.Secret {
				note(s.Secrets[s.Env.Name][v.Name])
			}
		}
	}
	if s.GlobalEnv != nil {
		for _, variable := range s.GlobalEnv.Vars {
			if variable.Secret {
				note(s.SecretValues(s.GlobalEnv)[variable.Name])
			}
		}
	}
	for _, v := range scope.All() {
		if LooksSecret(v.Name) {
			note(v.Raw)
		}
	}
	maskedURL := sent.URL
	if u, err := url.Parse(sent.URL); err == nil {
		parts := strings.Split(u.RawQuery, "&")
		for i, part := range parts {
			key, value, found := strings.Cut(part, "=")
			name, _ := url.QueryUnescape(key)
			if found && queryCredentials[name] {
				decoded, _ := url.QueryUnescape(value)
				note(decoded)
				parts[i] = key + "=****"
			}
		}
		u.RawQuery = strings.Join(parts, "&")
		maskedURL = u.String()
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	hide := func(str string) string {
		for _, v := range values {
			if len(v) >= 4 {
				str = strings.ReplaceAll(str, v, "****")
			}
		}
		return str
	}
	c := *sent
	c.URL, c.Body = hide(maskedURL), hide(sent.Body)
	c.Headers = slices.Clone(sent.Headers)
	for i, h := range c.Headers {
		switch strings.ToLower(h.Name) {
		case "authorization", "proxy-authorization":
			scheme, _, found := strings.Cut(h.Value, " ")
			if !found || !token.MatchString(scheme) {
				scheme = ""
			}
			c.Headers[i].Value = strings.TrimSpace(scheme + " ****")
		case "cookie", "x-wsse":
			c.Headers[i].Value = "****"
		default:
			if headerCredentials[strings.ToLower(h.Name)] {
				c.Headers[i].Value = "****"
			} else {
				c.Headers[i].Value = hide(h.Value)
			}
		}
	}
	return &c
}

// Filter keeps requests like bru run --tags/--exclude-tags: any tag of include (or include empty), none of exclude.
func Filter(reqs []*collection.Request, include, exclude []string) []*collection.Request {
	var out []*collection.Request
	for _, r := range reqs {
		if slices.ContainsFunc(exclude, r.HasTag) {
			continue
		}
		if len(include) > 0 && !slices.ContainsFunc(include, r.HasTag) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// maxJumps caps bru.setNextRequest jumps in one run, like bru run, so a script that jumps back
// forever cannot hang it.
const maxJumps = 10000

// Run executes reqs in order, honouring bru.setNextRequest and bru.runner.stopExecution; each result goes to
// report as soon as it is known. It stops early when ctx is done.
func (s *Session) Run(ctx context.Context, reqs []*collection.Request, report func(*Result)) {
	jumps := 0
	for i := 0; i < len(reqs) && ctx.Err() == nil; i++ {
		res := s.Execute(ctx, reqs[i])
		next := -1
		if res.NextRequest != "" && !res.Stop {
			next = slices.IndexFunc(reqs, func(r *collection.Request) bool { return r.Name == res.NextRequest })
			switch {
			case next < 0:
				res.Warnings = append(res.Warnings, fmt.Sprintf("bru.setNextRequest(%q): no such request in this run; going on in order", res.NextRequest))
			case jumps >= maxJumps:
				res.Warnings = append(res.Warnings, fmt.Sprintf("bru.setNextRequest: stopped after %d jumps", maxJumps))
				res.Stop = true
			default:
				jumps++
			}
		}
		report(res)
		if res.Stop {
			return
		}
		if next >= 0 {
			i = next - 1
		}
	}
}
