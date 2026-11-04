package runner

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/icholy/digest"
	"github.com/leodeim/brucli/internal/collection"
)

func authRequest(t *testing.T, target string, auth collection.Auth) (*Session, *collection.Request) {
	t.Helper()
	c, _ := fixture(t)
	r := c.Root.AllRequests()[0].Clone()
	r.URL, r.Method, r.Auth, r.Scripts, r.Assertions, r.Body = target, "GET", auth, collection.Scripts{}, nil, collection.Body{Mode: "none"}
	return NewSession(c, nil, false), r
}

func TestDigestAuthentication(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if string(data) != `{"ok":true}` {
			t.Errorf("replayed body: %s", data)
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="nonce", algorithm=SHA-256, qop="auth"`)
			w.WriteHeader(401)
			return
		}
		credentials, err := digest.ParseCredentials(r.Header.Get("Authorization"))
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		hash := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
		a1, a2 := hash("user:test:password"), hash(r.Method+":"+credentials.URI)
		want := hash(fmt.Sprintf("%s:nonce:%08x:%s:auth:%s", a1, credentials.Nc, credentials.Cnonce, a2))
		if credentials.Username != "user" || credentials.Response != want {
			t.Errorf("invalid digest: %+v", credentials)
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	s, r := authRequest(t, srv.URL, collection.Auth{Mode: "digest", Username: "user", Password: "password"})
	r.Method, r.Body = "POST", collection.Body{Mode: "json", Data: `{"ok":true}`}
	if res := s.Execute(context.Background(), r); res.Err != nil || res.Response.Status != 200 {
		t.Fatalf("digest result: %+v", res)
	}
}

func TestNTLMAuthentication(t *testing.T) {
	challenge := make([]byte, 48)
	copy(challenge, []byte("NTLMSSP\x00"))
	binary.LittleEndian.PutUint32(challenge[8:], 2)
	binary.LittleEndian.PutUint32(challenge[20:], 1|1<<9|1<<15|1<<19|1<<23|1<<29|1<<31)
	copy(challenge[24:], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	var mu sync.Mutex
	connection := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		header := r.Header.Get("Authorization")
		if header == "" {
			w.Header().Set("WWW-Authenticate", "NTLM")
			w.WriteHeader(401)
			return
		}
		_, data, _ := strings.Cut(header, " ")
		message, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(message) < 12 {
			t.Errorf("invalid NTLM message: %s", header)
			w.WriteHeader(401)
			return
		}
		switch binary.LittleEndian.Uint32(message[8:]) {
		case 1:
			connection = r.RemoteAddr
			w.Header().Set("WWW-Authenticate", "NTLM "+base64.StdEncoding.EncodeToString(challenge))
			w.WriteHeader(401)
		case 3:
			if connection != r.RemoteAddr {
				t.Error("NTLM authentication changed TCP connections")
			}
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected NTLM message type")
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	s, r := authRequest(t, srv.URL, collection.Auth{Mode: "ntlm", Username: "user", Password: "password", Options: map[string]string{"domain": "DOMAIN"}})
	// Exercise the configured transport as well as the NTLM wrapper.
	if err := s.ConfigureTransport(TransportOptions{NoProxy: true}); err != nil {
		t.Fatal(err)
	}
	if res := s.Execute(context.Background(), r); res.Err != nil || res.Response.Status != 200 {
		t.Fatalf("NTLM result: %+v", res)
	}
}

func TestOAuth1ReferenceSignature(t *testing.T) {
	// OAuth Core 1.0a, Appendix A.5: the published HMAC-SHA1 test vector.
	s, r := authRequest(t, "http://photos.example.net/photos?file=vacation.jpg&size=original", collection.Auth{Mode: "oauth1", Options: map[string]string{
		"consumerKey": "dpf43f3p2l4k3l03", "consumerSecret": "kd94hf93k423kf44", "accessToken": "nnch734d00sl2jdk", "accessTokenSecret": "pfkkdhi9sl3r4s00", "nonce": "kllo9940pd9333jh", "timestamp": "1191242096", "signatureMethod": "HMAC-SHA1", "placement": "query",
	}})
	req, err := http.NewRequest("GET", r.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.signOAuth1(req, r.Auth); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.Query().Get("oauth_signature"); got != "tR3+Ty81lMeYAr/Fid0kMTYa/WM=" {
		t.Fatalf("OAuth signature: %s", got)
	}
}

func TestSignedAuthentication(t *testing.T) {
	for _, mode := range []string{"oauth1", "awsv4", "wsse", "akamai-edgegrid"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header := r.Header.Get("Authorization")
				prefix := map[string]string{"oauth1": "OAuth ", "awsv4": "AWS4-HMAC-SHA256 ", "wsse": "WSSE ", "akamai-edgegrid": "EG1-HMAC-SHA256 "}[mode]
				if !strings.HasPrefix(header, prefix) {
					t.Errorf("missing signature: %s", header)
				}
				if strings.Contains(header, "secret-password") {
					t.Error("plain-text password in authorization")
				}
				if mode == "awsv4" && r.Header.Get("X-Amz-Security-Token") != "session-token" {
					t.Error("missing AWS session token")
				}
				if mode == "wsse" {
					fields := map[string]string{}
					for _, field := range strings.Split(strings.TrimPrefix(r.Header.Get("X-WSSE"), "UsernameToken "), ", ") {
						key, value, _ := strings.Cut(field, "=")
						fields[key] = strings.Trim(value, `"`)
					}
					nonce, _ := base64.StdEncoding.DecodeString(fields["Nonce"])
					hash := sha1.New()
					hash.Write(nonce)
					io.WriteString(hash, fields["Created"])
					io.WriteString(hash, "secret-password")
					if fields["PasswordDigest"] != base64.StdEncoding.EncodeToString(hash.Sum(nil)) {
						t.Error("invalid WSSE digest")
					}
				}
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			auth := collection.Auth{Mode: mode, Username: "user", Password: "secret-password", Options: map[string]string{"consumerKey": "client", "consumerSecret": "secret-password", "accessToken": "access", "accessTokenSecret": "secret-password", "accessKeyId": "access-key", "secretAccessKey": "secret-password", "sessionToken": "session-token", "region": "us-east-1", "service": "execute-api", "clientToken": "client", "clientSecret": "secret-password"}}
			s, r := authRequest(t, srv.URL, auth)
			res := s.Execute(context.Background(), r)
			if res.Err != nil || res.Response.Status != 200 {
				t.Fatalf("signed result: %+v", res)
			}
			if authorization, _ := res.Sent.Header("Authorization"); authorization == "" {
				t.Error("sent request omitted authentication")
			}
			masked := s.Mask(r, res.Sent)
			if strings.Contains(Curl(masked), "session-token") || strings.Contains(Curl(masked), "secret-password") {
				t.Fatal("credentials leaked in masked request")
			}
		})
	}
}

func TestEdgeGridSignature(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.example.com/upload?x=1", strings.NewReader("hello"))
	auth := collection.Auth{Mode: "akamai-edgegrid", Options: map[string]string{"clientToken": "client", "clientSecret": "secret", "accessToken": "access", "nonce": "fixed", "timestamp": "20261104T00:00:00+0000"}}
	if err := signEdgeGrid(req, auth); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(req.Header.Get("Authorization"), "signature=vPOsyPjQ+zjZRdbVjUrAIxMMpQJRnjjoJxKLBLxbVRc=") {
		t.Fatalf("EdgeGrid signature: %s", req.Header.Get("Authorization"))
	}
}

func TestAuthDoesNotFollowCredentialsAcrossOrigins(t *testing.T) {
	for _, mode := range []string{"digest", "oauth1", "wsse"} {
		t.Run(mode, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-WSSE") != "" {
					t.Error("credentials crossed origins")
				}
				w.Header().Set("WWW-Authenticate", `Digest realm="target", nonce="nonce", qop="auth"`)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
			defer origin.Close()
			s, r := authRequest(t, origin.URL, collection.Auth{Mode: mode, Username: "user", Password: "password", Options: map[string]string{"consumerKey": "client", "consumerSecret": "secret"}})
			if res := s.Execute(context.Background(), r); res.Err != nil || res.Response.Status != 401 {
				t.Fatalf("redirect result: %+v", res)
			}
		})
	}
}
