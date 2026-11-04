package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
	"github.com/leodeim/brucli/internal/collection"
)

func TestLiveWebSocketUI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer srv.Close()
	h := newHarness(t)
	r := h.open("GetPet")
	h.m.s.Collection.Root.Defaults.Scripts = collection.Scripts{}
	h.m.s.StreamTimeout = 2 * time.Second
	r.Kind, r.URL, r.Auth, r.Scripts, r.Assertions = "websocket", strings.Replace(srv.URL, "http://", "ws://", 1), collection.Auth{Mode: "none"}, collection.Scripts{}, nil
	r.Body = collection.Body{Mode: "websocket", Messages: []collection.Message{{Type: "text", Data: "live reply"}}}
	messages := make(chan tea.Msg, 32)
	launch := func(command tea.Cmd) {
		if command != nil {
			go func() { messages <- command() }()
		}
	}
	command := h.m.send(r)
	for _, cmd := range command().(tea.BatchMsg) {
		launch(cmd)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	live := false
	for {
		select {
		case msg := <-messages:
			if msg == nil {
				continue
			}
			_, next := h.m.Update(msg)
			launch(next)
			if progress, ok := msg.(streamProgressMsg); ok && strings.Contains(string(progress.response.Body), "live reply") {
				live = true
				if !strings.Contains(h.screen(), "connected") || !strings.Contains(h.screen(), "live reply") {
					t.Fatal("live reply was not rendered")
				}
				h.m.cancelSend()
			}
			if _, ok := msg.(sentMsg); ok {
				if !live || h.m.busy || h.m.results[r].Err != nil {
					t.Fatal("WebSocket did not disconnect cleanly")
				}
				return
			}
		case <-ctx.Done():
			h.m.cancelSend()
			t.Fatal("live WebSocket UI timed out")
		}
	}
}
