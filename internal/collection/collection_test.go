package collection

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// copyFixture copies testdata/petstore, a collection in the exact layout Bruno's serializer writes, so tests may write to it.
func copyFixture(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "petstore")
	if err := os.CopyFS(dst, os.DirFS("testdata/petstore")); err != nil {
		t.Fatal(err)
	}
	return dst
}

func mustLoad(t *testing.T, dir string) *Collection {
	t.Helper()
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func find(t *testing.T, c *Collection, name string) *Request {
	t.Helper()
	for _, r := range c.Root.AllRequests() {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no request %q", name)
	return nil
}

func TestLoadTree(t *testing.T) {
	c := mustLoad(t, "testdata/petstore")
	var folders []string
	for _, f := range c.Root.Folders {
		folders = append(folders, f.Name)
	}
	want := []string{"Pets", "Users"}
	if !slices.Equal(folders, want) {
		t.Fatalf("folders = %v, want %v", folders, want)
	}
	if n := len(c.Root.AllRequests()); n != 5 {
		t.Fatalf("requests = %d, want 5", n)
	}
	// Requests keep their seq order, which is not alphabetical.
	var pets []string
	for _, r := range c.Root.Folders[0].Requests {
		pets = append(pets, r.Name)
	}
	if want := []string{"ListPets", "GetPet", "CreatePet"}; !slices.Equal(pets, want) {
		t.Fatalf("pets order = %v, want %v", pets, want)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings: %v", c.Warnings)
	}
}

func TestLoadDefaultsAndEnvironments(t *testing.T) {
	c := mustLoad(t, "testdata/petstore")
	d := c.Root.Defaults
	if d.Auth.Mode != "basic" || d.Auth.Username != "{{username}}" || d.Auth.Password != "{{password}}" {
		t.Fatalf("collection auth = %+v", d.Auth)
	}
	if len(d.Headers) != 1 || d.Headers[0].Name != "X-Api-Version" || d.Headers[0].Value != "2" || !d.Headers[0].Enabled {
		t.Fatalf("collection headers = %+v", d.Headers)
	}
	if len(d.Vars) != 2 || d.Vars[1].Name != "sellerId" || d.Vars[1].Value != "{{ownerId}}" {
		t.Fatalf("collection vars = %+v", d.Vars)
	}
	if !strings.Contains(c.Docs, "# Petstore") {
		t.Fatalf("docs not read: %q", c.Docs[:min(40, len(c.Docs))])
	}
	if got := []string{c.Environments[0].Name, c.Environments[1].Name}; !slices.Equal(got, []string{"local", "staging"}) {
		t.Fatalf("environments = %v", got)
	}
	local := c.Environment("local")
	if local.Vars[0].Name != "port" || local.Vars[0].Value != "8080" || local.Vars[1].Value != "http://localhost:{{port}}" {
		t.Fatalf("local vars = %+v", local.Vars)
	}
	if c.Root.Folders[0].Defaults.Auth.Mode != "inherit" {
		t.Fatalf("folder auth = %q", c.Root.Folders[0].Defaults.Auth.Mode)
	}
}

func TestLoadRequest(t *testing.T) {
	c := mustLoad(t, "testdata/petstore")
	r := find(t, c, "ListPets")
	if r.Method != "GET" || r.URL != "{{baseUrl}}/v1/pets?limit=20" {
		t.Fatalf("method/url = %s %s", r.Method, r.URL)
	}
	if !r.Params[0].Enabled || r.Params[0].Value != "20" || r.Params[1].Enabled || r.Params[1].Description != "name filters pets by a case-insensitive substring of their name.\nEmpty = no filter." {
		t.Fatalf("params = %+v", r.Params[:2])
	}
	if !r.HasTag("read") || r.Auth.Mode != "inherit" {
		t.Fatalf("tags %v auth %q", r.Tags, r.Auth.Mode)
	}
	if len(r.Assertions) != 1 || r.Assertions[0] != (Assertion{Expression: "res.status", Operator: "eq", Value: "200", Enabled: true}) {
		t.Fatalf("assertions = %+v", r.Assertions)
	}
	if !strings.Contains(r.Scripts.After, "bru.setVar('petId', pet.id)") {
		t.Fatalf("after script = %q", r.Scripts.After)
	}
	if got := r.Chain(); len(got) != 2 || got[0] != c.Root || got[1].Name != "Pets" {
		t.Fatalf("chain = %v", got)
	}

	create := find(t, c, "CreatePet")
	if create.Body.Mode != "json" || !strings.Contains(create.Body.Data, `"species": "dog"`) {
		t.Fatalf("body = %+v", create.Body)
	}
	get := find(t, c, "GetPet")
	if len(get.Params) != 1 || get.Params[0].Type != "path" || get.Params[0].Value != "{{petId}}" {
		t.Fatalf("path params = %+v", get.Params)
	}
	if !get.Settings.EncodeURL || !get.Settings.FollowRedirects || get.Settings.MaxRedirects != 5 {
		t.Fatalf("settings = %+v", get.Settings)
	}
}

// Saving unchanged files must reproduce Bruno's own output byte for byte, or every save is a noisy diff.
func TestSaveRoundTrip(t *testing.T) {
	dir := copyFixture(t)
	c := mustLoad(t, dir)
	for _, r := range c.Root.AllRequests() {
		if err := r.Save(); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range c.Environments {
		if err := e.Save(); err != nil {
			t.Fatal(err)
		}
	}
	err := fs.WalkDir(os.DirFS("testdata/petstore"), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, _ := os.ReadFile(filepath.Join("testdata/petstore", path))
		got, _ := os.ReadFile(filepath.Join(dir, path))
		if string(got) != string(want) {
			t.Errorf("%s changed on save:\n--- got\n%s\n--- want\n%s", path, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSaveEdits(t *testing.T) {
	dir := copyFixture(t)
	c := mustLoad(t, dir)

	r := find(t, c, "ListPets")
	r.Params[0].Enabled = false
	r.Params[1].Enabled, r.Params[1].Value = true, "rex"
	r.Headers = append(r.Headers, KV{Name: "X-Trace", Value: "{{traceId}}", Enabled: true})
	r.Assertions = append(r.Assertions, Assertion{Expression: "res.body.pets", Operator: "isArray", Enabled: true})
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	s := find(t, c, "CreatePet")
	s.Body.Data = `{"name": "Max"}`
	s.Auth = Auth{Mode: "bearer", Token: "{{token}}"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	env := c.Environment("local")
	env.Vars[0].Value = "8081"
	env.Vars = append(env.Vars, EnvVar{KV: KV{Name: "apiToken", Enabled: true}, Secret: true})
	if err := env.Save(); err != nil {
		t.Fatal(err)
	}

	c2 := mustLoad(t, dir)
	r2 := find(t, c2, "ListPets")
	if r2.Params[0].Enabled || !r2.Params[1].Enabled || r2.Params[1].Value != "rex" {
		t.Fatalf("params not saved: %+v", r2.Params[:2])
	}
	if len(r2.Headers) != 1 || r2.Headers[0].Value != "{{traceId}}" || r2.Assertions[1].Operator != "isArray" {
		t.Fatalf("headers %+v assertions %+v", r2.Headers, r2.Assertions)
	}
	s2 := find(t, c2, "CreatePet")
	if s2.Body.Data != `{"name": "Max"}` || s2.Auth.Mode != "bearer" || s2.Auth.Token != "{{token}}" {
		t.Fatalf("body %+v auth %+v", s2.Body, s2.Auth)
	}
	env2 := c2.Environment("local")
	if env2.Vars[0].Value != "8081" || !env2.Vars[5].Secret || env2.Vars[5].Value != "" {
		t.Fatalf("env vars = %+v", env2.Vars)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "Pets", "ListPets.yml"))
	if i, j := strings.Index(string(b), "  headers:"), strings.Index(string(b), "  auth:"); i < 0 || i > j {
		t.Fatalf("headers must be written before auth:\n%s", b)
	}
}

func TestSortItems(t *testing.T) {
	type item struct {
		name string
		seq  int
	}
	key := func(i item) (string, int) { return i.name, i.seq }
	got := sortItems([]item{{"d", 0}, {"b", 2}, {"c", 0}, {"a", 1}, {"z", 9}}, key)
	var names []string
	for _, i := range got {
		names = append(names, i.name)
	}
	if want := []string{"a", "b", "c", "d", "z"}; !slices.Equal(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
}

func TestParseDotEnv(t *testing.T) {
	got := ParseDotEnv([]byte(`
# comment
PETSTORE_USERNAME=petstore-user
export PETSTORE_PASSWORD='pa$$ #word'
QUOTED="line1\nline2"
TRAILING=value # note
EMPTY=
=nokey
broken
`))
	want := map[string]string{
		"PETSTORE_USERNAME": "petstore-user",
		"PETSTORE_PASSWORD": "pa$$ #word",
		"QUOTED":            "line1\nline2",
		"TRAILING":          "value",
		"EMPTY":             "",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNotCollection) {
		t.Fatalf("err = %v, want ErrNotCollection", err)
	}

	dir := copyFixture(t)
	ws := "info:\n  name: Socket\n  type: websocket\n"
	if err := os.WriteFile(filepath.Join(dir, "Users", "Socket.yml"), []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PETSTORE_USERNAME=u\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, dir)
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], `Users/Socket.yml: unsupported item type "websocket"`) {
		t.Fatalf("warnings = %v", c.Warnings)
	}
	if c.DotEnv["PETSTORE_USERNAME"] != "u" {
		t.Fatalf("dotenv = %v", c.DotEnv)
	}
}

func TestRequestFiles(t *testing.T) {
	dir := copyFixture(t)
	c := mustLoad(t, dir)
	users := c.Root.Folders[1]

	r, err := users.NewRequest("Search users", "{{baseUrl}}/v1/users/search")
	if err != nil {
		t.Fatal(err)
	}
	want := `info:
  name: Search users
  type: http
  seq: 3

http:
  method: GET
  url: "{{baseUrl}}/v1/users/search"
  auth: inherit

settings:
  encodeUrl: true
  timeout: 0
`
	if b, _ := os.ReadFile(filepath.Join(dir, "Users", "Search users.yml")); string(b) != want {
		t.Fatalf("new request file:\n%s", b)
	}
	if _, err := users.NewRequest("Search users", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := users.NewRequest("folder", ""); err == nil {
		t.Fatal("folder.yml must not be taken by a request")
	}

	dup, err := find(t, c, "UpdateUser").Duplicate("UpdateUser copy")
	if err != nil {
		t.Fatal(err)
	}
	if err := dup.Rename("Update user/v2"); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dup.Path) != "Update user-v2.yml" {
		t.Fatalf("renamed path = %s", dup.Path)
	}
	if err := find(t, c, "ListUsers").Delete(); err != nil {
		t.Fatal(err)
	}

	c2 := mustLoad(t, dir)
	var names []string
	for _, r := range c2.Root.Folders[1].Requests {
		names = append(names, fmt.Sprintf("%s:%d", r.Name, r.Seq))
	}
	if want := []string{"UpdateUser:2", "Search users:3", "Update user/v2:4"}; !slices.Equal(names, want) {
		t.Fatalf("users = %v, want %v", names, want)
	}
	copied := find(t, c2, "Update user/v2")
	if !strings.Contains(copied.Body.Data, `"display_name": "Ada L."`) || !copied.HasTag("write") {
		t.Fatalf("duplicate lost content: %+v", copied)
	}
	if r.Name != "Search users" || find(t, c2, "Search users").Auth.Mode != "inherit" {
		t.Fatal("new request not reloaded")
	}
}

func TestDotEnvLikeDotenv(t *testing.T) {
	got := ParseDotEnv([]byte("\uFEFFA=\"secret\" # prod\r\nC=abc#def\nexport\tG=1\nB=`x`\nPEM=\"line1\nline2\"\nD='keep \\n'\nE=\"esc\\nape\"\n# comment\nF = spaced \n"))
	want := map[string]string{"A": "secret", "C": "abc", "G": "1", "B": "x", "PEM": "line1\nline2", "D": `keep \n`, "E": "esc\nape", "F": "spaced"}
	if !maps.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestSaveKeepsOutsideEditsTypesAndQuoting(t *testing.T) {
	dir := copyFixture(t)
	c := mustLoad(t, dir)
	r := find(t, c, "GetPet")
	// Another app changes a key brucli does not model after brucli loaded the file.
	b, _ := os.ReadFile(r.Path)
	if err := os.WriteFile(r.Path, []byte(strings.Replace(string(b), "maxRedirects: 5", "maxRedirects: 7", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Docs = "yes"
	r.Headers = append(r.Headers, KV{Name: "X-Date", Value: "2024-01-01", Enabled: true}, KV{Name: "X-Bool", Value: "true", Enabled: true})
	r.Auth = Auth{Mode: "apikey", Key: "k", Value: "v", Placement: "query"}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(r.Path)
	for _, want := range []string{"maxRedirects: 7", "docs: yes\n", "value: 2024-01-01\n", `value: "true"`, "placement: query"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("saved file lacks %q:\n%s", want, saved)
		}
	}
	if info, _ := os.Stat(r.Path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", info.Mode())
	}
	typed := KV{Name: "port", Value: "8080", Enabled: true}
	typed.typed = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("type"), scalar("number"), scalar("data"), scalar("8080")}}
	typed.data = "8080"
	typed.Value = "9090"
	if n := valueNode(typed); n.Kind != yaml.MappingNode || n.Content[1].Value != "number" || n.Content[3].Value != "9090" {
		t.Errorf("typed value saved as %+v", n)
	}
}

func TestBadFileIsAWarningAndCaseRename(t *testing.T) {
	dir := copyFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "Pets", "Broken.yml"), []byte("<<<<<<< HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, dir)
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "Broken.yml") {
		t.Fatalf("warnings = %v", c.Warnings)
	}
	r := find(t, c, "GetPet")
	if err := r.Rename("getpet"); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	if _, err := c.Root.Folders[0].NewRequest(".hidden", ""); err == nil {
		t.Fatal("a leading-dot name was accepted")
	}
}
