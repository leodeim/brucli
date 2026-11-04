package collection

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOAuthConfigurationSave(t *testing.T) {
	dir := copyFixture(t)
	path := filepath.Join(dir, "oauth.yml")
	source := `info:
  name: OAuth
  type: http
http:
  method: GET
  url: https://example.com
  auth:
    type: oauth2
    flow: client_credentials
    accessTokenUrl: https://example.com/token
    credentials:
      clientId: old
      clientSecret: '{{secret}}'
      placement: body
    settings:
      autoFetchToken: true
      autoRefreshToken: false
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := readRequest(path)
	if err != nil {
		t.Fatal(err)
	}
	clone := r.Clone()
	clone.Auth.Options["credentials.clientId"] = "new"
	if r.Auth.Option("credentials.clientId") != "old" {
		t.Fatal("auth options shared between clones")
	}
	if err := clone.Save(); err != nil {
		t.Fatal(err)
	}
	r, err = readRequest(path)
	if err != nil || r.Auth.Option("credentials.clientId") != "new" || r.Auth.Option("settings.autoRefreshToken") != "false" || r.Auth.Option("credentials.clientSecret") != "{{secret}}" {
		t.Fatalf("saved auth: %+v, %v", r.Auth, err)
	}
	bru := mustLoad(t, bruFixture(t)).Root.AllRequests()[0]
	bru.Auth = r.Auth
	if err := bru.Save(); err != nil {
		t.Fatal(err)
	}
	r, err = readRequest(bru.Path)
	if err != nil || r.Auth.Option("clientId") != "new" || r.Auth.Option("grantType") != "client_credentials" {
		t.Fatalf("saved .bru auth: %+v, %v", r.Auth, err)
	}
}
