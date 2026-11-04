package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leodeim/brucli/internal/collection"
)

func TestGraphQL(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var payload map[string]any
				if request.Method == "GET" {
					payload = map[string]any{"query": request.URL.Query().Get("query"), "operationName": request.URL.Query().Get("operationName")}
					var variables map[string]any
					if err := json.Unmarshal([]byte(request.URL.Query().Get("variables")), &variables); err != nil {
						t.Error(err)
					}
					payload["variables"] = variables
					if request.URL.Query().Get("existing") != "yes" {
						t.Error("existing query lost")
					}
				} else {
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if request.Header.Get("Content-Type") != "application/json" {
						t.Error("missing JSON content type")
					}
				}
				variables, ok := payload["variables"].(map[string]any)
				if !ok || variables["id"] != "42" || payload["operationName"] != "User" || !strings.Contains(payload["query"].(string), "user(id: $id)") {
					t.Errorf("payload: %#v", payload)
				}
				io.WriteString(w, `{"data":{"user":{"id":"42"}}}`)
			}))
			defer srv.Close()
			c, _ := fixture(t)
			r := c.Root.AllRequests()[0].Clone()
			r.Kind, r.Method, r.URL, r.Auth = "graphql", method, srv.URL+"?existing=yes", collection.Auth{Mode: "none"}
			r.Scripts, r.Assertions = collection.Scripts{}, nil
			r.Body = collection.Body{Mode: "graphql", Data: `query User($id: ID!) { user(id: $id) { id } }`, Variables: `{"id":"{{userId}}"}`, OperationName: "User"}
			s := NewSession(c, nil, false)
			s.Runtime["userId"] = "42"
			res := s.Execute(context.Background(), r)
			if res.Err != nil {
				t.Fatal(res.Err)
			}
			r.Body.Variables = "[]"
			if res := s.Execute(context.Background(), r); res.Err == nil {
				t.Fatal("accepted invalid GraphQL variables")
			}
		})
	}
}

func TestMultipart(t *testing.T) {
	c, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(c.Dir, "first.txt"), []byte("first file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir, "second.txt"), []byte("second file"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer request.MultipartForm.RemoveAll()
		if request.FormValue("name") != "Ada" || request.FormValue("off") != "" {
			t.Errorf("fields: %v", request.MultipartForm.Value)
		}
		files := request.MultipartForm.File["attachments"]
		if len(files) != 2 {
			t.Errorf("files: %v", files)
			return
		}
		for i, want := range []string{"first file", "second file"} {
			file, err := files[i].Open()
			if err != nil {
				t.Error(err)
				continue
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil || string(data) != want || files[i].Header.Get("Content-Type") != "text/custom" {
				t.Errorf("attachment: %q, %v", data, err)
			}
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	r := c.Root.AllRequests()[0].Clone()
	r.Method, r.URL, r.Auth, r.Scripts, r.Assertions = "POST", srv.URL, collection.Auth{Mode: "none"}, collection.Scripts{}, nil
	r.Body = collection.Body{Mode: "multipart-form", Form: []collection.KV{
		{Name: "name", Value: "{{name}}", Enabled: true},
		{Name: "off", Value: "hidden", Enabled: false},
		{Name: "attachments", Type: "file", Value: "{{file}}\nsecond.txt", ContentType: "text/custom", Enabled: true},
	}}
	s := NewSession(c, nil, false)
	s.Runtime["name"], s.Runtime["file"] = "Ada", "first.txt"
	if res := s.Execute(context.Background(), r); res.Err != nil {
		t.Fatal(res.Err)
	}
	r.Body.Form[2].Value = "missing.txt"
	if res := s.Execute(context.Background(), r); res.Err == nil || !strings.Contains(res.Err.Error(), "missing.txt") {
		t.Fatalf("missing attachment: %v", res.Err)
	}
}
