package runner

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
)

type seen struct {
	Method, URL, Auth, Version, ContentType, Body string
}

// gateway fakes the Petstore API: list routes return rows so the collection's capture scripts fire.
type gateway struct {
	mu     sync.Mutex
	seen   []seen
	status int
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.seen = append(g.seen, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Api-Version"), r.Header.Get("Content-Type"), string(b)})
	status := g.status
	g.mu.Unlock()
	rows := map[string]any{
		"/v1/pets":  map[string]any{"pets": []any{map[string]any{"id": "p-1", "name": "Rex"}}},
		"/v1/users": map[string]any{"users": []any{map[string]any{"id": "u-bob", "name": "Bob"}, map[string]any{"id": "team/ada", "name": "Ada"}}},
	}
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	body := any(map[string]any{})
	if r.Method == http.MethodGet && rows[r.URL.Path] != nil {
		body = rows[r.URL.Path]
	}
	_ = json.NewEncoder(w).Encode(body)
}

func fixture(t *testing.T) (*collection.Collection, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "petstore")
	if err := os.CopyFS(dir, os.DirFS("../collection/testdata/petstore")); err != nil {
		t.Fatal(err)
	}
	env := "PETSTORE_USERNAME=bruno-user\nPETSTORE_PASSWORD=bruno-pass\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := collection.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func session(t *testing.T, g *gateway) *Session {
	t.Helper()
	c, _ := fixture(t)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	s := NewSession(c, c.Environment("local"), false)
	s.Overrides["baseUrl"] = srv.URL
	return s
}

// The whole collection, in order: captured ids feed later requests, auth and headers are
// inherited, and path and query values are encoded like encodeURIComponent.
func TestCollectionRun(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	var results []*Result
	s.Run(t.Context(), s.Collection.Root.AllRequests(), func(r *Result) { results = append(results, r) })

	if len(results) != 5 {
		t.Fatalf("results = %d", len(results))
	}
	for _, r := range results {
		if !r.Passed() {
			t.Errorf("%s failed: err=%v scripts=%v asserts=%+v", r.Request.Name, r.Err, r.ScriptErrors, r.Assertions)
		}
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("bruno-user:bruno-pass"))
	for _, x := range g.seen {
		if x.Auth != wantAuth || x.Version != "2" {
			t.Errorf("%s %s: auth %q version %q", x.Method, x.URL, x.Auth, x.Version)
		}
	}
	const owner = "11111111-2222-4333-8444-555555555555"
	var sent []string
	for _, x := range g.seen {
		sent = append(sent, x.Method+" "+x.URL)
	}
	want := []string{
		"GET /v1/pets?limit=20",
		"GET /v1/pets/p-1",
		"POST /v1/pets",
		"GET /v1/users?created_after=2024-01-01T00%3A00%3A00Z",
		"PATCH /v1/users/team%2Fada",
	}
	if !slices.Equal(sent, want) {
		t.Errorf("sent:\n%s\nwant:\n%s", strings.Join(sent, "\n"), strings.Join(want, "\n"))
	}
	if create := g.seen[2]; !strings.Contains(create.Body, `"owner_id": "`+owner+`"`) || create.ContentType != "application/json" {
		t.Errorf("CreatePet body %q content-type %q", create.Body, create.ContentType)
	}
	if s.Runtime["petId"] != "p-1" || s.Runtime["userId"] != "team/ada" {
		t.Errorf("runtime = %v", s.Runtime)
	}
}

func TestReadsOnlyAndFailures(t *testing.T) {
	g := &gateway{status: http.StatusInternalServerError}
	s := session(t, g)
	reads := Filter(s.Collection.Root.AllRequests(), []string{"read"}, nil)
	if len(reads) != 3 {
		t.Fatalf("reads = %d, want 3", len(reads))
	}
	if n := len(Filter(s.Collection.Root.AllRequests(), nil, []string{"read"})); n != 2 {
		t.Fatalf("excluding reads = %d, want 2", n)
	}
	res := s.Execute(t.Context(), reads[0])
	if res.Passed() || len(res.Assertions) != 1 || res.Assertions[0].Error != "expected 500 to equal 200" {
		t.Fatalf("assertions = %+v", res.Assertions)
	}
	for _, x := range g.seen {
		if x.Method != http.MethodGet {
			t.Fatalf("a read sent %s", x.Method)
		}
	}
}

func TestUnresolvedAndMissingCredentials(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	delete(s.Process, "PETSTORE_USERNAME")
	update := s.Collection.Root.Folders[1].Requests[1] // UpdateUser, before any user id was captured
	res := s.Execute(t.Context(), update)
	if strings.Join(res.Unresolved, ",") != "process.env.PETSTORE_USERNAME,userId" {
		t.Fatalf("unresolved = %v", res.Unresolved)
	}
	if got := g.seen[0].URL; got != "/v1/users/%7B%7BuserId%7D%7D" {
		t.Fatalf("url = %s", got)
	}
}

func TestScriptsSteerRunAndPersistEnv(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	reqs := s.Collection.Root.Folders[0].Requests // ListPets, GetPet, ...
	reqs[0].Scripts.Before = `bru.setEnvVar("port", "9090", { persist: true }); bru.setNextRequest("ListPets")`
	runs := 0
	s.Run(t.Context(), reqs, func(r *Result) {
		runs++
		if runs == 2 {
			reqs[0].Scripts.Before = "bru.runner.stopExecution()"
		}
	})
	if runs != 3 {
		t.Fatalf("runs = %d, want 3 (jump back twice, then stop)", runs)
	}
	reloaded, err := collection.Load(s.Collection.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := reloaded.Environment("local").Vars[0]; v.Name != "port" || v.Value != "9090" {
		t.Fatalf("persisted port = %+v", v)
	}
}

func TestEffectiveAuth(t *testing.T) {
	c, _ := fixture(t)
	r := c.Root.Folders[0].Requests[0]
	if a, from := EffectiveAuth(r); a.Mode != "basic" || from != "collection" {
		t.Fatalf("inherited = %s from %s", a.Mode, from)
	}
	c.Root.Folders[0].Defaults.Auth = collection.Auth{Mode: "bearer", Token: "t"}
	if a, from := EffectiveAuth(r); a.Mode != "bearer" || from != "folder Pets" {
		t.Fatalf("folder = %s from %s", a.Mode, from)
	}
	r.Auth = collection.Auth{Mode: "none"}
	if a, from := EffectiveAuth(r); a.Mode != "none" || from != "request" {
		t.Fatalf("request = %s from %s", a.Mode, from)
	}
}

func TestEncoding(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://h/p?a=x/y&b={{v}}", "http://h/p?a=x%2Fy&b=%7B%7Bv%7D%7D"},
		{"http://h/p?a=already%2Fenc&flag", "http://h/p?a=already%2Fenc&flag"},
		{"http://h/p?q=a b&r=1#frag", "http://h/p?q=a%20b&r=1#frag"},
		{"http://h/p", "http://h/p"},
	}
	for _, tt := range tests {
		if got := encodeQuery(tt.in); got != tt.want {
			t.Errorf("encodeQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := replacePathParam("http://h/e/:id/x?id=:id", "id", "a/b", true); got != "http://h/e/a%2Fb/x?id=:id" {
		t.Errorf("path param = %q", got)
	}
}

func TestCurl(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	res := s.Execute(t.Context(), s.Collection.Root.Folders[0].Requests[2]) // CreatePet
	c := Curl(s.Mask(res.Request, res.Sent))
	for _, want := range []string{"curl -X POST '", "-H 'Authorization: Basic ****'", "-H 'X-Api-Version: 2'", `--data-raw '{`} {
		if !strings.Contains(c, want) {
			t.Errorf("curl lacks %q:\n%s", want, c)
		}
	}
	if strings.Contains(Curl(res.Sent), "****") {
		t.Error("unmasked curl hides the credential")
	}
	// A method from a variable must not reach the shell unquoted.
	evil := Curl(&script.Request{Method: "GET $(touch /tmp/x)", URL: "http://h"})
	if !strings.HasPrefix(evil, `curl -X 'GET $(touch /tmp/x)' 'http://h'`) {
		t.Errorf("method not quoted: %s", evil)
	}
}

func TestMaskHidesSecretsEverywhere(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	s.Env.Vars = append(s.Env.Vars, collection.EnvVar{KV: collection.KV{Name: "apiToken", Value: "tok-123456", Enabled: true}})
	sent := &script.Request{
		Method: "GET",
		URL:    "http://h/x?key=tok-123456",
		Headers: []script.Header{
			{Name: "Authorization", Value: "rawsecrettoken"},
			{Name: "X-Api-Key", Value: "tok-123456"},
			{Name: "Cookie", Value: "session=abc"},
		},
		Body: `{"pw":"tok-123456"}`,
	}
	m := s.Mask(nil, sent)
	got := m.URL + "|" + m.Headers[0].Value + "|" + m.Headers[1].Value + "|" + m.Headers[2].Value + "|" + m.Body
	if got != `http://h/x?key=****|****|****|****|{"pw":"****"}` {
		t.Fatalf("masked = %s", got)
	}
	if sent.Headers[0].Value != "rawsecrettoken" {
		t.Fatal("Mask changed the original")
	}
}

func TestBeforeRequestErrorStopsTheSend(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	r := s.Collection.Root.Folders[0].Requests[2].Clone() // CreatePet
	r.Scripts.Before = `throw new Error("not on prod")`
	res := s.Execute(t.Context(), r)
	if len(g.seen) != 0 || res.Err == nil || !res.Failed() || len(res.ScriptErrors) != 1 {
		t.Fatalf("seen %v err %v scripts %v", g.seen, res.Err, res.ScriptErrors)
	}
}

func TestRunGoesOnPastUnknownNextRequestAndSkipsDoNotFail(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	reqs := []*collection.Request{s.Collection.Root.Folders[0].Requests[0].Clone(), s.Collection.Root.Folders[0].Requests[1].Clone()}
	reqs[0].Scripts.Before = `bru.setNextRequest("NoSuchRequest")`
	reqs[1].Scripts.Before = `bru.runner.skipRequest()`
	var results []*Result
	var counts Counts
	s.Run(t.Context(), reqs, func(r *Result) { results = append(results, r); counts.Add(r) })
	if len(results) != 2 || !strings.Contains(strings.Join(results[0].Warnings, ""), "NoSuchRequest") {
		t.Fatalf("results %d warnings %v", len(results), results[0].Warnings)
	}
	if !results[1].Skipped || results[1].Failed() || results[1].Passed() || counts.String() != "1 passed, 0 failed, 1 skipped" {
		t.Fatalf("skip counted as %s", counts)
	}
}

func TestScriptsSeeAndOverrideBearerAuth(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	r := s.Collection.Root.Folders[0].Requests[0].Clone()
	r.Auth = collection.Auth{Mode: "bearer", Token: "{{tok}}"}
	r.Vars = []collection.KV{{Name: "tok", Value: "from-auth", Enabled: true}}
	r.Scripts.Before = `if (req.getHeader("Authorization") === "Bearer {{tok}}") req.setHeader("Authorization", "Bearer from-script")`
	s.Execute(t.Context(), r)
	if got := g.seen[0].Auth; got != "Bearer from-script" {
		t.Fatalf("auth = %q", got)
	}
}

func TestAPIKeyInQueryKeepsOrder(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	r := s.Collection.Root.Folders[0].Requests[0].Clone()
	r.URL = "{{baseUrl}}/v1/pets?z=1&a=2"
	r.Params = nil
	r.Auth = collection.Auth{Mode: "apikey", Key: "api_key", Value: "k 1", Placement: "query"}
	s.Execute(t.Context(), r)
	if got := g.seen[0].URL; got != "/v1/pets?z=1&a=2&api_key=k%201" {
		t.Fatalf("url = %s", got)
	}
}

func TestScriptEnvVarBeatsOverride(t *testing.T) {
	g := &gateway{}
	s := session(t, g)
	s.Overrides["petId"] = "from-cli"
	r := s.Collection.Root.Folders[0].Requests[1].Clone() // GetPet /v1/pets/{{petId}}
	r.Scripts.After = `bru.setEnvVar("petId", "from-script")`
	s.Execute(t.Context(), r)
	s.Execute(t.Context(), r)
	if got := g.seen[1].URL; got != "/v1/pets/from-script" {
		t.Fatalf("second request = %s", got)
	}
}

func TestForkDoesNotShareObjects(t *testing.T) {
	s := NewSession(&collection.Collection{Root: &collection.Folder{}}, nil, false)
	s.Runtime["obj"] = map[string]any{"list": []any{map[string]any{"k": 1}}}
	f := s.Fork()
	f.Runtime["obj"].(map[string]any)["list"].([]any)[0].(map[string]any)["k"] = 2
	if s.Runtime["obj"].(map[string]any)["list"].([]any)[0].(map[string]any)["k"] != 1 {
		t.Fatal("the fork changed the session's object")
	}
}
