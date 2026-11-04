package runner

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
)

func websocketEcho(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing WebSocket auth")
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		for {
			typ, data, err := connection.Read(r.Context())
			if err != nil {
				return
			}
			if err := connection.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	}))
}

func TestWebSocketMessages(t *testing.T) {
	srv := websocketEcho(t)
	defer srv.Close()
	for _, typ := range []string{"text", "json", "binary"} {
		t.Run(typ, func(t *testing.T) {
			s, r := authRequest(t, strings.Replace(srv.URL, "http://", "ws://", 1), collection.Auth{Mode: "bearer", Token: "token"})
			s.Collection.Root.Defaults.Scripts = collection.Scripts{}
			r.Kind = "websocket"
			value := "hello {{name}}"
			if typ == "json" {
				value = `{"name":"{{name}}"}`
			}
			if typ == "binary" {
				value = base64.StdEncoding.EncodeToString([]byte{0, 1, 255})
			}
			s.Runtime["name"] = "Ada"
			r.Body = collection.Body{Mode: "websocket", Messages: []collection.Message{{Type: "text", Data: "not selected"}, {Type: typ, Data: value, Selected: true}}}
			res := s.Execute(context.Background(), r)
			if res.Err != nil || res.Response.Status != 101 || res.Response.Protocol != "websocket" {
				t.Fatalf("WebSocket response: %+v", res)
			}
			if strings.Contains(string(res.Response.Body), "not selected") || typ != "binary" && !strings.Contains(string(res.Response.Body), "Ada") {
				t.Fatalf("WebSocket messages: %s", res.Response.Body)
			}
		})
	}
}

func TestInteractiveWebSocket(t *testing.T) {
	srv := websocketEcho(t)
	defer srv.Close()
	s, r := authRequest(t, strings.Replace(srv.URL, "http://", "ws://", 1), collection.Auth{Mode: "bearer", Token: "token"})
	s.Collection.Root.Defaults.Scripts = collection.Scripts{}
	r.Kind, r.Body = "websocket", collection.Body{Mode: "websocket", Messages: []collection.Message{{Type: "text", Data: "first"}}}
	input := make(chan collection.Message, 1)
	progress := make(chan *script.Response, 10)
	s.StreamInteractive, s.StreamInput, s.OnStream = true, input, func(response *script.Response) { progress <- response }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan *Result, 1)
	go func() { done <- s.Execute(ctx, r) }()
	for {
		select {
		case response := <-progress:
			if strings.Contains(string(response.Body), "first") {
				input <- collection.Message{Type: "text", Data: "second"}
				goto sent
			}
		case <-ctx.Done():
			t.Fatal("did not receive first live message")
		}
	}
sent:
	for {
		select {
		case response := <-progress:
			if strings.Contains(string(response.Body), "second") {
				cancel()
				result := <-done
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("did not receive second live message")
		}
	}
}
