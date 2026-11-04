package runner

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leodeim/brucli/internal/collection"
	"software.sslmate.com/src/go-pkcs12"
)

func TestExplicitProxyAndBypass(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			t.Error("missing proxy authentication")
		}
		io.WriteString(w, "proxied")
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	u.User = url.UserPassword("user", "pass")
	c, _ := fixture(t)
	s := NewSession(c, nil, false)
	r := c.Root.AllRequests()[0].Clone()
	r.URL, r.Auth, r.Scripts, r.Assertions = origin.URL, collection.Auth{Mode: "none"}, collection.Scripts{}, nil
	if err := s.ConfigureTransport(TransportOptions{ProxyURL: u.String()}); err != nil {
		t.Fatal(err)
	}
	if res := s.Execute(context.Background(), r); res.Err != nil || string(res.Response.Body) != "proxied" {
		t.Fatalf("proxy response: %+v", res)
	}
	if err := s.ConfigureTransport(TransportOptions{NoProxy: true}); err != nil {
		t.Fatal(err)
	}
	if res := s.Execute(context.Background(), r); res.Err != nil || string(res.Response.Body) != "direct" {
		t.Fatalf("no proxy response: %+v", res)
	}
	s.Transport = TransportOptions{}
	c.Config.Proxy.Config.Hostname, c.Config.Proxy.Config.Port, c.Config.Proxy.Config.Protocol = u.Hostname(), u.Port(), "http"
	c.Config.Proxy.Config.Bypass = "127.0.0.1"
	if res := s.Execute(context.Background(), r); res.Err != nil || string(res.Response.Body) != "direct" {
		t.Fatalf("bypass response: %+v", res)
	}
}

func TestHTTPSProxy(t *testing.T) {
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			t.Error("missing HTTPS proxy credentials")
		}
		io.WriteString(w, "secure proxy")
	}))
	proxy.EnableHTTP2 = true
	proxy.StartTLS()
	defer proxy.Close()
	s, r := authRequest(t, "http://example.test/", collection.Auth{Mode: "none"})
	s.Client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	path := filepath.Join(s.Collection.Dir, "proxy-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(proxy.URL)
	u.User = url.UserPassword("user", "pass")
	if err := s.ConfigureTransport(TransportOptions{ProxyURL: u.String(), CACert: path}); err != nil {
		t.Fatal(err)
	}
	if res := s.Execute(context.Background(), r); res.Err != nil || string(res.Response.Body) != "secure proxy" {
		t.Fatalf("HTTPS proxy response: %+v", res)
	}
}

func TestSOCKS4Proxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			done <- err
			return
		}
		if header[0] != 4 || header[1] != 1 {
			done <- io.ErrUnexpectedEOF
			return
		}
		if _, err := reader.ReadString(0); err != nil {
			done <- err
			return
		}
		if host, err := reader.ReadString(0); err != nil || host != "example.test\x00" {
			done <- io.ErrUnexpectedEOF
			return
		}
		if _, err := conn.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0}); err != nil {
			done <- err
			return
		}
		request, err := http.ReadRequest(reader)
		if err != nil {
			done <- err
			return
		}
		request.Body.Close()
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 7\r\nConnection: close\r\n\r\nproxied")
		done <- err
	}()
	c, _ := fixture(t)
	s := NewSession(c, nil, false)
	if err := s.ConfigureTransport(TransportOptions{ProxyURL: "socks4a://" + listener.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	r := c.Root.AllRequests()[0].Clone()
	r.URL, r.Auth, r.Scripts, r.Assertions = "http://example.test/", collection.Auth{Mode: "none"}, collection.Scripts{}, nil
	if res := s.Execute(context.Background(), r); res.Err != nil || string(res.Response.Body) != "proxied" {
		t.Fatalf("SOCKS4 response: %+v", res)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientCertificates(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	client := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "brucli-test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter}
	der, err := x509.CreateCertificate(rand.Reader, client, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "brucli-test" {
			t.Error("missing client certificate")
		}
		io.WriteString(w, `{}`)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()
	c, _ := fixture(t)
	write := func(name string, data []byte) string {
		path := filepath.Join(c.Dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	certPath := write("client.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPath := write("client-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	rootPath := write("server.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	pfx, err := pkcs12.Modern.Encode(key, client, []*x509.Certificate{ca}, "password")
	if err != nil {
		t.Fatal(err)
	}
	pfxPath := write("client.pfx", pfx)
	for _, cert := range []collection.ClientCertificate{
		{Domain: "127.0.0.1", Type: "pem", CertificateFilePath: certPath, PrivateKeyFilePath: keyPath},
		{Domain: "127.0.0.1", Type: "pkcs12", PKCS12FilePath: pfxPath, Passphrase: "{{passphrase}}"},
	} {
		t.Run(cert.Type, func(t *testing.T) {
			c.Config.ClientCertificates = []collection.ClientCertificate{cert}
			s := NewSession(c, nil, false)
			s.Runtime["passphrase"] = "password"
			if err := s.ConfigureTransport(TransportOptions{CACert: rootPath}); err != nil {
				t.Fatal(err)
			}
			r := c.Root.AllRequests()[0].Clone()
			r.URL, r.Auth, r.Scripts, r.Assertions = srv.URL, collection.Auth{Mode: "none"}, collection.Scripts{}, nil
			if res := s.Execute(context.Background(), r); res.Err != nil {
				t.Fatal(res.Err)
			}
		})
	}
}

func TestCertificateDomainAndProxyRules(t *testing.T) {
	for _, test := range []struct {
		host, pattern string
		want          bool
	}{
		{"api.example.com", "*.example.com", true},
		{"example.com", "*.example.com", false},
		{"attackerexample.com", "*.example.com", false},
		{"api.example.com", "https://api.example.com:443", true},
		{"other.example.com", "api.example.com", false},
	} {
		if got := certificateDomain(test.host, test.pattern); got != test.want {
			t.Errorf("certificateDomain(%q,%q) = %v", test.host, test.pattern, got)
		}
	}
	for _, test := range []struct {
		target, rules string
		want          bool
	}{
		{"https://api.example.com", "*.example.com", true},
		{"https://example.com", "*.example.com", false},
		{"https://other.com", "example.com", false},
		{"http://127.0.0.1:8080", "127.0.0.0/8", true},
		{"https://example.com:443", "example.com:443", true},
		{"https://example.com:8443", "example.com:443", false},
	} {
		u, _ := url.Parse(test.target)
		if got := bypassProxy(u, test.rules); got != test.want {
			t.Errorf("bypassProxy(%q,%q) = %v", test.target, test.rules, got)
		}
	}
}
