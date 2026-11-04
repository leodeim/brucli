package collection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphQLAndMultipartFiles(t *testing.T) {
	dir := copyFixture(t)
	for name, source := range map[string]string{
		"graphql.yml": `info:
  name: Query
  type: graphql

graphql:
  method: POST
  url: https://example.com/graphql
  body:
    query: query User { user { id } }
    variables: '{}'
  auth: inherit

extensions:
  preserve: true
`,
		"multipart.yml": `info:
  name: Upload
  type: http

http:
  method: POST
  url: https://example.com/upload
  body:
    type: multipart-form
    data:
      - name: upload
        type: file
        value:
          - first.txt
          - second.txt
        contentType: text/plain
      - name: caption
        type: text
        value: hello
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := mustLoad(t, dir)
	query, upload := find(t, c, "Query"), find(t, c, "Upload")
	if query.Kind != "graphql" || query.Body.Mode != "graphql" || query.Body.Variables != "{}" {
		t.Fatalf("GraphQL: %+v", query)
	}
	if len(upload.Body.Form) != 2 || upload.Body.Form[0].Type != "file" || upload.Body.Form[0].Value != "first.txt\nsecond.txt" {
		t.Fatalf("multipart: %+v", upload.Body)
	}
	query.Body.Data = "query User { user { name } }"
	upload.Body.Form[0].Value = "third.txt\nfourth.txt"
	for _, r := range []*Request{query, upload} {
		if err := r.Save(); err != nil {
			t.Fatal(err)
		}
	}
	c = mustLoad(t, dir)
	if got := find(t, c, "Query"); !strings.Contains(got.Body.Data, "name") || got.Kind != "graphql" {
		t.Fatalf("GraphQL after save: %+v", got)
	}
	if got := find(t, c, "Upload").Body.Form[0]; got.Value != "third.txt\nfourth.txt" || got.ContentType != "text/plain" {
		t.Fatalf("attachment after save: %+v", got)
	}
	data, err := os.ReadFile(query.Path)
	if err != nil || !strings.Contains(string(data), "preserve: true") || strings.Contains(string(data), "\nhttp:") {
		t.Fatalf("GraphQL file: %s, %v", data, err)
	}
}
