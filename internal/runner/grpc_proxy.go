package runner

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

func (t *requestTransport) dialGRPCProxy(ctx context.Context, transport *http.Transport, request *http.Request, address string) (net.Conn, error) {
	var target *url.URL
	if transport.Proxy != nil {
		var err error
		target, err = transport.Proxy(request)
		if err != nil {
			return nil, err
		}
	}
	if target == nil {
		if transport.DialContext != nil {
			return transport.DialContext(ctx, "tcp", address)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}
	if target.Scheme == "socks5" || target.Scheme == "socks5h" {
		dialer, err := proxy.FromURL(target, &net.Dialer{})
		if err != nil {
			return nil, err
		}
		return dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", address)
	}
	proxyAddress := target.Host
	if target.Port() == "" {
		port := "80"
		if target.Scheme == "https" {
			port = "443"
		}
		proxyAddress = net.JoinHostPort(target.Hostname(), port)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, err
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if target.Scheme == "https" {
		config, err := t.tlsConfig(target.Hostname())
		if err != nil {
			conn.Close()
			return nil, err
		}
		config.ServerName = target.Hostname()
		config.NextProtos = []string{"http/1.1"}
		secure := tls.Client(conn, config)
		if err := secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = secure
	}
	connect := &http.Request{Method: "CONNECT", URL: &url.URL{Opaque: address}, Host: address, Header: http.Header{}}
	if target.User != nil {
		password, _ := target.User.Password()
		connect.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(target.User.Username()+":"+password)))
	}
	if err := connect.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, connect)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT returned %s", response.Status)
	}
	conn.SetDeadline(time.Time{})
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }
