package collection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamRequestSerialization(t *testing.T) {
	for _, kind := range []string{"grpc", "websocket"} {
		for _, format := range []string{".yml", ".bru"} {
			t.Run(kind+format, func(t *testing.T) {
				var c *Collection
				if format == ".bru" {
					c = mustLoad(t, bruFixture(t))
				} else {
					c = mustLoad(t, copyFixture(t))
				}
				r, err := c.Root.NewRequest("Stream", "http://localhost:50051")
				if err != nil {
					t.Fatal(err)
				}
				r.Kind, r.Method, r.MethodType, r.ProtoFilePath = kind, "/test.Echo/Call", "bidi-streaming", "test.proto"
				if kind == "websocket" {
					r.Method = "GET"
				}
				r.Body = Body{Mode: kind, Messages: []Message{{Title: "first", Type: "json", Data: `{"name":"Ada"}`}, {Title: "second", Type: "text", Data: "hello", Selected: true}}}
				if err := r.Save(); err != nil {
					t.Fatal(err)
				}
				r, err = readRequest(r.Path)
				if err != nil {
					t.Fatal(err)
				}
				if r.Kind != kind || len(r.Body.Messages) != 2 || r.Body.Messages[1].Data != "hello" {
					t.Fatalf("stream after save: %+v", r)
				}
				if kind == "grpc" && (r.Method != "/test.Echo/Call" || r.ProtoFilePath != "test.proto") {
					t.Fatalf("gRPC fields lost: %+v", r)
				}
				if kind == "websocket" && (!r.Body.Messages[1].Selected || r.Body.Messages[1].Type != "text") {
					t.Fatalf("WebSocket variant lost: %+v", r.Body.Messages)
				}
				r.Body.Messages[1].Data = "changed"
				if err := r.Save(); err != nil {
					t.Fatal(err)
				}
				reloaded, err := readRequest(r.Path)
				if err != nil || len(reloaded.Body.Messages) != 2 || reloaded.Body.Messages[1].Data != "changed" {
					t.Fatalf("stream edit: %+v, %v", reloaded, err)
				}
			})
		}
	}
}

func TestSingleStreamMessageVariants(t *testing.T) {
	dir := copyFixture(t)
	for name, data := range map[string]string{
		"rpc.yml":    "info:\n  name: RPC\n  type: grpc\ngrpc:\n  url: localhost:50051\n  method: /test.Echo/Call\n  message: '{}'\nruntime:\n  scripts:\n    - type: grpc:before-call-start\n      code: keepHook()\n",
		"socket.yml": "info:\n  name: Socket\n  type: websocket\nwebsocket:\n  url: ws://localhost:8080\n  message:\n    type: text\n    data: hello\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := readRequest(path)
		if err != nil || len(r.Body.Messages) != 1 {
			t.Fatalf("single message: %+v, %v", r, err)
		}
		if err := r.Save(); err != nil {
			t.Fatal(err)
		}
		if name == "rpc.yml" {
			saved, _ := os.ReadFile(path)
			if !strings.Contains(string(saved), "keepHook()") {
				t.Fatal("unsupported gRPC script hook was removed")
			}
		}
	}
}
