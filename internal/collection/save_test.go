package collection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavePreservesUnsupportedActions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.yml")
	source := `info:
  name: Actions
  type: http
http:
  method: GET
  url: https://example.com
runtime:
  actions:
    - type: custom-action
      custom: keep this action
    - type: set-variable
      phase: before-request
      selector:
        method: script
        expression: keep this selector
      variable:
        name: ignored
    - type: set-variable
      phase: after-response
      selector:
        method: jsonq
        expression: res.body.id
      variable:
        name: id
        scope: runtime
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := readRequest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Actions) != 1 || r.Actions[0].Name != "id" {
		t.Fatalf("unsupported actions interpreted: %+v", r.Actions)
	}
	r.Actions[0].Expression = "res.body.newId"
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keep this action", "keep this selector", "res.body.newId"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("save removed %q: %s", want, saved)
		}
	}
	r.Actions = nil
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r, err = readRequest(path)
	if err != nil || len(r.Actions) != 0 {
		t.Fatalf("removed action reappeared: %+v, %v", r, err)
	}
	saved, _ = os.ReadFile(path)
	if !strings.Contains(string(saved), "keep this action") || !strings.Contains(string(saved), "keep this selector") {
		t.Fatalf("deleting known actions removed unknown actions: %s", saved)
	}
}

func TestSaveRejectsInvalidExternalChanges(t *testing.T) {
	c := mustLoad(t, copyFixture(t))
	r := c.Root.AllRequests()[0]
	e := c.Environments[0]
	for _, item := range []struct {
		path string
		save func() error
	}{{r.Path, r.Save}, {e.Path, e.Save}} {
		broken := "external: [unfinished\n"
		if err := os.WriteFile(item.path, []byte(broken), 0600); err != nil {
			t.Fatal(err)
		}
		if err := item.save(); err == nil {
			t.Errorf("save overwrote invalid external changes in %s", item.path)
		}
		saved, err := os.ReadFile(item.path)
		if err != nil || string(saved) != broken {
			t.Errorf("external file changed: %q, %v", saved, err)
		}
	}
}

func TestBruGRPCMetadataEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call.bru")
	source := "meta {\n  name: Call\n  type: grpc\n}\ngrpc {\n  url: grpc://localhost:50051\n  method: /test.Echo/Call\n  auth: none\n}\nmetadata {\n  x-token: old\n}\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := readRequest(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Headers[0].Value = "new"
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r, err = readRequest(path)
	if err != nil || len(r.Headers) != 1 || r.Headers[0].Value != "new" {
		t.Fatalf("metadata edit lost: %+v, %v", r, err)
	}
}

func TestBruRequestCannotUseCollectionManifestName(t *testing.T) {
	dir := bruFixture(t)
	if err := os.Remove(filepath.Join(dir, "collection.bru")); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, dir)
	if _, err := c.Root.NewRequest("collection", "http://localhost"); err == nil {
		t.Fatal("created a request file that loading would treat as collection defaults")
	}
}
