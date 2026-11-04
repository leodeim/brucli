package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "petstore")
	if err := os.CopyFS(dir, os.DirFS("internal/collection/testdata/petstore")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunHeadless(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	dir := fixture(t)

	var out, errOut bytes.Buffer
	code, err := run([]string{"run", "-env", "local", "-env-var", "baseUrl=" + srv.URL, "-tags", "read", dir}, &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "3 passed, 0 failed, 3 requests") || !strings.Contains(out.String(), "✓ GET    Pets/ListPets  200") {
		t.Fatalf("output:\n%s", out.String())
	}
	for _, m := range methods {
		if m != http.MethodGet && m != http.MethodPost {
			t.Fatalf("read run sent %s", m)
		}
	}

	out.Reset()
	code, _ = run([]string{"run", "-env", "local", "-env-var", "baseUrl=" + srv.URL, "-folder", "Pets", "-exclude-tags", "write", dir}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "2 passed, 0 failed, 2 requests") {
		t.Fatalf("folder run code %d:\n%s", code, out.String())
	}
}

func TestRunHeadlessFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	dir := fixture(t)
	var out, errOut bytes.Buffer
	code, _ := run([]string{"run", "-env", "local", "-env-var", "baseUrl=" + srv.URL, "-tags", "read", "-folder", "Pets", dir}, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), "✗ res.status eq 200: expected 401 to equal 200") {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	for _, args := range [][]string{
		{"run", "-env", "nope", dir},
		{"run", "-folder", "Nope", dir},
		{"run", t.TempDir()},
		{"run", "-env-var", "novalue", dir},
	} {
		if code, _ := run(args, &out, &errOut); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
}
