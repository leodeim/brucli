package runner

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/youmark/pkcs8"
	"go.yaml.in/yaml/v3"
	"software.sslmate.com/src/go-pkcs12"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/vars"
)

type TransportOptions struct {
	ProxyURL         string
	NoProxy          bool
	CACert           string
	ClientCertConfig string
	Certificates     []collection.ClientCertificate
}

func (s *Session) ConfigureTransport(options TransportOptions) error {
	var rootCAs *x509.CertPool
	if options.NoProxy && options.ProxyURL != "" {
		return errors.New("-proxy and -noproxy cannot be used together")
	}
	if options.ClientCertConfig != "" {
		data, err := os.ReadFile(options.ClientCertConfig)
		if err != nil {
			return err
		}
		var config struct {
			Enabled bool                           `yaml:"enabled"`
			Certs   []collection.ClientCertificate `yaml:"certs"`
		}
		if err := yaml.Unmarshal(data, &config); err != nil {
			return err
		}
		if config.Enabled {
			for _, cert := range config.Certs {
				cert.BaseDir = filepath.Dir(options.ClientCertConfig)
				options.Certificates = append(options.Certificates, cert)
			}
		}
	}
	if options.CACert != "" {
		data, err := os.ReadFile(options.CACert)
		if err != nil {
			return err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(data) {
			return errors.New("CA certificate file contains no certificates")
		}
		rootCAs = roots
	}
	s.Transport = options
	s.rootCAs = rootCAs
	return nil
}

// Each redirect passes through this transport again, selecting certificates
// and proxy bypass rules for the destination host.
type requestTransport struct {
	base    *http.Transport
	session *Session
	scope   *vars.Scope
}

func (t *requestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport := t.base.Clone()
	if err := t.configureProxy(transport); err != nil {
		return nil, err
	}
	config, err := t.tlsConfig(req.URL.Hostname())
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = config
	// HTTPS proxy handshakes must select certificates for the proxy's host,
	// rather than inheriting a certificate selected for the origin server.
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		config, err := t.tlsConfig(host)
		if err != nil {
			return nil, err
		}
		config.ServerName = host
		dial := transport.DialContext
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		secure := tls.Client(conn, config)
		if err := secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		return secure, nil
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	response.Body = &transportBody{ReadCloser: response.Body, transport: transport}
	return response, nil
}

type transportBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b *transportBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return err
}

func (t *requestTransport) configureProxy(transport *http.Transport) error {
	options, config, in := t.session.Transport, t.session.Collection.Config.Proxy, t.scope.Interpolate
	if options.NoProxy || (options.ProxyURL == "" && config.Disabled) {
		transport.Proxy = nil
		return nil
	}
	raw, bypass := options.ProxyURL, ""
	if raw == "" && !config.Inherit && config.Config.Hostname != "" {
		proxy := config.Config
		host := in(proxy.Hostname)
		if port := in(proxy.Port); port != "" {
			host = net.JoinHostPort(host, port)
		}
		u := &url.URL{Scheme: cmp.Or(in(proxy.Protocol), "http"), Host: host}
		if !proxy.Auth.Disabled && proxy.Auth.Username != "" {
			u.User = url.UserPassword(in(proxy.Auth.Username), in(proxy.Auth.Password))
		}
		raw, bypass = u.String(), in(proxy.Bypass)
	}
	if raw == "" {
		return nil
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Hostname() == "" {
		return errors.New("invalid proxy URL")
	}
	switch proxy.Scheme {
	case "http", "https", "socks5", "socks5h":
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			if bypassProxy(req.URL, bypass) {
				return nil, nil
			}
			return proxy, nil
		}
	case "socks4", "socks4a":
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			u := &url.URL{Host: address, Scheme: "http"}
			if bypassProxy(u, bypass) {
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			return dialSOCKS4(ctx, proxy, address)
		}
	default:
		return fmt.Errorf("unsupported proxy protocol %q", proxy.Scheme)
	}
	return nil
}

func bypassProxy(u *url.URL, bypass string) bool {
	host := strings.ToLower(u.Hostname())
	for _, rule := range strings.FieldsFunc(bypass, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if rule == "*" {
			return true
		}
		if prefix, err := netip.ParsePrefix(rule); err == nil {
			if ip, err := netip.ParseAddr(host); err == nil && prefix.Contains(ip) {
				return true
			}
			continue
		}
		if ruleHost, port, err := net.SplitHostPort(rule); err == nil {
			if port != cmp.Or(u.Port(), map[string]string{"http": "80", "https": "443", "ws": "80", "wss": "443"}[u.Scheme]) {
				continue
			}
			rule = ruleHost
		}
		rule = strings.ToLower(rule)
		if host == rule {
			return true
		}
		if strings.HasPrefix(rule, "*.") || strings.HasPrefix(rule, ".") {
			if strings.HasSuffix(host, strings.TrimPrefix(rule, "*")) {
				return true
			}
		}
	}
	return false
}

func (t *requestTransport) tlsConfig(host string) (*tls.Config, error) {
	config := &tls.Config{}
	if t.base.TLSClientConfig != nil {
		config = t.base.TLSClientConfig.Clone()
	}
	if t.session.rootCAs != nil {
		config.RootCAs = t.session.rootCAs
	}
	certs := append(append([]collection.ClientCertificate{}, t.session.Collection.Config.ClientCertificates...), t.session.Transport.Certificates...)
	for _, cert := range certs {
		if cert.Disabled || !certificateDomain(host, t.scope.Interpolate(cert.Domain)) {
			continue
		}
		loaded, err := t.loadCertificate(cert)
		if err != nil {
			return nil, fmt.Errorf("client certificate for %s: %w", host, err)
		}
		config.Certificates, config.GetClientCertificate = []tls.Certificate{loaded}, nil
		break
	}
	return config, nil
}

func certificateDomain(host, pattern string) bool {
	host, pattern = strings.ToLower(host), strings.ToLower(pattern)
	if u, err := url.Parse(pattern); err == nil && u.Hostname() != "" {
		pattern = u.Hostname()
	}
	if h, _, err := net.SplitHostPort(pattern); err == nil {
		pattern = h
	}
	if strings.HasPrefix(pattern, "*.") {
		return host != pattern[2:] && strings.HasSuffix(host, pattern[1:])
	}
	return host == pattern
}

func (t *requestTransport) loadCertificate(cert collection.ClientCertificate) (tls.Certificate, error) {
	in := t.scope.Interpolate
	read := func(path string) ([]byte, error) {
		path = in(path)
		if path == "" {
			return nil, errors.New("missing certificate file path")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cmp.Or(cert.BaseDir, t.session.Collection.Dir), path)
		}
		return os.ReadFile(path)
	}
	if cert.Type == "pkcs12" || cert.Type == "pfx" {
		data, err := read(cmp.Or(cert.PKCS12FilePath, cert.PFXFilePath))
		if err != nil {
			return tls.Certificate{}, err
		}
		key, leaf, chain, err := pkcs12.DecodeChain(data, in(cert.Passphrase))
		if err != nil {
			return tls.Certificate{}, err
		}
		result := tls.Certificate{PrivateKey: key, Leaf: leaf, Certificate: [][]byte{leaf.Raw}}
		for _, ca := range chain {
			result.Certificate = append(result.Certificate, ca.Raw)
		}
		return result, nil
	}
	if cert.Type != "pem" && cert.Type != "cert" && cert.Type != "" {
		return tls.Certificate{}, fmt.Errorf("unsupported certificate type %q", cert.Type)
	}
	certificate, err := read(cmp.Or(cert.CertificateFilePath, cert.CertFilePath))
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := read(cmp.Or(cert.PrivateKeyFilePath, cert.KeyFilePath))
	if err != nil {
		return tls.Certificate{}, err
	}
	if block, _ := pem.Decode(key); block != nil {
		if x509.IsEncryptedPEMBlock(block) {
			plain, err := x509.DecryptPEMBlock(block, []byte(in(cert.Passphrase)))
			if err != nil {
				return tls.Certificate{}, err
			}
			key = pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: plain})
		} else if block.Type == "ENCRYPTED PRIVATE KEY" {
			parsed, err := pkcs8.ParsePKCS8PrivateKey(block.Bytes, []byte(in(cert.Passphrase)))
			if err != nil {
				return tls.Certificate{}, err
			}
			plain, err := x509.MarshalPKCS8PrivateKey(parsed)
			if err != nil {
				return tls.Certificate{}, err
			}
			key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: plain})
		}
	}
	return tls.X509KeyPair(certificate, key)
}

func dialSOCKS4(ctx context.Context, proxy *url.URL, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if proxy.Scheme == "socks4" && net.ParseIP(host).To4() == nil {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		found := false
		for _, address := range addresses {
			if address.IP.To4() != nil {
				host = address.IP.String()
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("SOCKS4 requires an IPv4 destination")
		}
	}
	portNumber, err := net.LookupPort("tcp", port)
	if err != nil {
		return nil, err
	}
	proxyAddress := proxy.Host
	if proxy.Port() == "" {
		proxyAddress = net.JoinHostPort(proxy.Hostname(), "1080")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	request := []byte{4, 1, 0, 0, 0, 0, 0, 1}
	binary.BigEndian.PutUint16(request[2:4], uint16(portNumber))
	if ip := net.ParseIP(host).To4(); ip != nil {
		copy(request[4:8], ip)
	}
	username := ""
	if proxy.User != nil {
		username = proxy.User.Username()
	}
	request = append(request, username...)
	request = append(request, 0)
	if request[4] == 0 && request[5] == 0 && request[6] == 0 && request[7] == 1 {
		request = append(request, host...)
		request = append(request, 0)
	}
	_, err = conn.Write(request)
	var response [8]byte
	if err == nil {
		_, err = io.ReadFull(conn, response[:])
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	if response[1] != 90 {
		conn.Close()
		return nil, fmt.Errorf("SOCKS4 proxy rejected connection (code %d)", response[1])
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}
