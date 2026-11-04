package collection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bruFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"bruno.json":       `{"name":"Native Bru", "version":"1", "ignore":["ignored"]}`,
		"collection.bru":   "meta {\n  type: collection\n}\n\nauth {\n  mode: basic\n}\n\nauth:basic {\n  username: user\n  password: {{password}}\n}\n",
		"Users/folder.bru": "meta {\n  name: Users\n  seq: 1\n}\n\nheaders {\n  X-Folder: inherited\n}\n",
		"Users/GetUser.bru": `meta {
  name: Get user
  type: http
  seq: 1
  tags: [
    read
  ]
  custom: preserve meta
}

get {
  url: https://example.com/users/:id
  body: json
  auth: inherit
  custom: preserve method
}

params:path {
  id: 42
}

headers {
  @custom(keep-me)
  @description("a: useful header")
  "X-Name:Quoted": first
  ~X-Disabled: hidden
}

body:json {
  {
    "name": "Ada"
  }
}

script:pre-request {
  bru.setVar('before', 'yes');
}

assert {
  res.status: 200
}

extension:future {
  unknown: keep exactly
}
`,
		"environments/local.bru": `vars {
  password: secret
  multiline: '''
    line 1
    line 2
  '''
  ~disabled: off
}

vars:secret [
  token
]
`,
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBruLoadAndSave(t *testing.T) {
	dir := bruFixture(t)
	c := mustLoad(t, dir)
	r := find(t, c, "Get user")
	if c.Name != "Native Bru" || r.Body.Mode != "json" || r.Params[0].Value != "42" || r.Folder.Defaults.Auth.Mode != "inherit" || c.Root.Defaults.Auth.Mode != "basic" {
		t.Fatalf("collection: %+v; request: %+v", c, r)
	}
	original, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(r.Path)
	if string(data) != string(original) {
		t.Fatalf("unchanged .bru file changed:\n%s", data)
	}
	r.URL = "https://example.com/new/:id"
	r.Method = "POST"
	r.Headers[0].Value = "second"
	r.Body.Data = `{"name":"Grace"}`
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(r.Path)
	for _, preserved := range []string{"@custom(keep-me)", "custom: preserve meta", "custom: preserve method", "unknown: keep exactly"} {
		if !strings.Contains(string(data), preserved) {
			t.Errorf("lost %q:\n%s", preserved, data)
		}
	}
	r = find(t, mustLoad(t, dir), "Get user")
	if r.Method != "POST" || r.URL != "https://example.com/new/:id" || r.Headers[0].Value != "second" || r.Body.Data != `{"name":"Grace"}` || r.Headers[0].Description != "a: useful header" {
		t.Fatalf("edited request: %+v", r)
	}
	copy, err := r.Duplicate("Copy")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(copy.Path) != ".bru" {
		t.Fatalf("wrong duplicate format: %s", copy.Path)
	}
	if err := copy.Rename("Renamed"); err != nil {
		t.Fatal(err)
	}
	if find(t, mustLoad(t, dir), "Renamed").Seq != 2 {
		t.Fatal("duplicate sequence lost")
	}
	created, err := c.Root.NewRequest("Created", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(created.Path) != ".bru" || find(t, mustLoad(t, dir), "Created").URL != "https://example.com" {
		t.Fatal("new request did not use .bru")
	}
}

func TestBruEnvironment(t *testing.T) {
	c := mustLoad(t, bruFixture(t))
	env := c.Environment("local")
	if env == nil || len(env.Vars) != 4 || env.Vars[1].Value != "line 1\nline 2" || !env.Vars[3].Secret {
		t.Fatalf("environment: %+v", env)
	}
	original, _ := os.ReadFile(env.Path)
	if err := env.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(env.Path)
	if string(original) != string(data) {
		t.Fatalf("unchanged environment changed:\n%s", data)
	}
	env.Vars[3].Value = "never-persist"
	if err := env.Persist("password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if err := env.Persist("token", "never-persist"); err != nil {
		t.Fatal(err)
	}
	disk, err := readEnvironment(env.Path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(env.Path)
	if disk.Vars[0].Value != "new-password" || strings.Contains(string(data), "never-persist") {
		t.Fatalf("environment save: %s", data)
	}
}

func TestMalformedBru(t *testing.T) {
	for _, source := range []string{"not a request", "meta {\n  name: Missing closing brace\n", "meta {\n  type: http\n}\n\nget {\n  missing-colon\n}\n"} {
		if _, err := bruNode("test.bru", []byte(source)); err == nil {
			t.Errorf("accepted malformed .bru: %q", source)
		}
	}
}
