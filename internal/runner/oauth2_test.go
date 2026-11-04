package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leodeim/brucli/internal/collection"
)

func TestOAuth2TokensAndRefresh(t *testing.T) {
	for _, flow := range []string{"client_credentials", "resource_owner_password_credentials"} {
		t.Run(flow, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					calls.Add(1)
					r.ParseForm()
					if r.FormValue("client_id") != "client" || r.FormValue("client_secret") != "secret" {
						t.Error("missing OAuth client credentials")
					}
					if r.FormValue("grant_type") == "password" && (r.FormValue("username") != "user" || r.FormValue("password") != "password") {
						t.Error("missing resource owner credentials")
					}
					if r.FormValue("grant_type") == "refresh_token" && r.FormValue("refresh_token") != "refresh" {
						t.Error("missing refresh token")
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"access_token":"access","token_type":"Bearer","refresh_token":"refresh","expires_in":3600}`)
					return
				}
				if r.Header.Get("Authorization") != "Bearer access" {
					t.Error("missing OAuth access token")
				}
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			s, r := authRequest(t, srv.URL, collection.Auth{Mode: "oauth2", Options: map[string]string{
				"flow": flow, "credentials.clientId": "client", "credentials.clientSecret": "secret", "resourceOwner.username": "user", "resourceOwner.password": "password", "accessTokenUrl": srv.URL + "/token",
			}})
			for i := 0; i < 2; i++ {
				if res := s.Execute(context.Background(), r); res.Err != nil {
					t.Fatal(res.Err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("did not cache OAuth token: %d calls", calls.Load())
			}
			for key, token := range s.tokens {
				expired := *token
				expired.Expiry = time.Now().Add(-time.Hour)
				s.tokens[key] = &expired
			}
			if res := s.Execute(context.Background(), r); res.Err != nil {
				t.Fatal(res.Err)
			}
			if calls.Load() != 2 {
				t.Fatalf("did not refresh OAuth token: %d calls", calls.Load())
			}
		})
	}
}

func TestOAuth2BrowserCodeAndPKCE(t *testing.T) {
	var challenge atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			query := r.URL.Query()
			if query.Get("code_challenge_method") != "S256" {
				t.Error("missing PKCE challenge method")
			}
			challenge.Store(query.Get("code_challenge"))
			callback, err := url.Parse(query.Get("redirect_uri"))
			if err != nil {
				t.Error(err)
				return
			}
			values := callback.Query()
			values.Set("state", query.Get("state"))
			values.Set("code", "test-code")
			callback.RawQuery = values.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
		case "/token":
			r.ParseForm()
			hash := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if r.FormValue("code") != "test-code" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge.Load() {
				t.Error("authorization code or PKCE verifier does not match")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"browser-token","token_type":"Bearer","expires_in":3600}`)
		default:
			if r.Header.Get("Authorization") != "Bearer browser-token" {
				t.Error("missing browser token")
			}
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	s, r := authRequest(t, srv.URL, collection.Auth{Mode: "oauth2", Options: map[string]string{
		"flow": "authorization_code", "authorizationUrl": srv.URL + "/authorize", "accessTokenUrl": srv.URL + "/token", "callbackUrl": "http://127.0.0.1:0/callback", "credentials.clientId": "client", "pkce.method": "S256",
	}})
	s.Interactive = true
	s.OpenBrowser = func(target string) error {
		response, err := http.Get(target)
		if err != nil {
			return err
		}
		return response.Body.Close()
	}
	if res := s.Execute(context.Background(), r); res.Err != nil {
		t.Fatal(res.Err)
	}
}

func TestOAuth2QueryPlacementAndHeadlessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_token") != "secret-token" || r.Header.Get("Authorization") != "" {
			t.Error("wrong token placement")
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	s, r := authRequest(t, srv.URL, collection.Auth{Mode: "oauth2", Options: map[string]string{"accessToken": "secret-token", "tokenConfig.placement.query": "api_token"}})
	if res := s.Execute(context.Background(), r); res.Err != nil {
		t.Fatal(res.Err)
	}
	r.Auth.Options = map[string]string{"flow": "authorization_code", "authorizationUrl": "https://example.com/authorize"}
	if res := s.Execute(context.Background(), r); res.Err == nil || !strings.Contains(res.Err.Error(), "browser login requires the UI") {
		t.Fatalf("headless OAuth result: %+v", res)
	}
}
