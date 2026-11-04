package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

func (s *Session) sendWebSocket(ctx context.Context, r *collection.Request, sent *script.Request, res *Result, scope *vars.Scope) (*script.Response, error) {
	ctx, cancel := s.streamContext(ctx, r)
	defer cancel()
	client, cleanup, err := s.clientForScope(scope)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	target := sent.URL
	if strings.HasPrefix(target, "http://") {
		target = "ws://" + strings.TrimPrefix(target, "http://")
	}
	if strings.HasPrefix(target, "https://") {
		target = "wss://" + strings.TrimPrefix(target, "https://")
	}
	httpTarget := strings.Replace(strings.Replace(target, "wss://", "https://", 1), "ws://", "http://", 1)
	handshake, err := http.NewRequestWithContext(ctx, "GET", httpTarget, nil)
	if err != nil {
		return nil, err
	}
	for _, header := range sent.Headers {
		handshake.Header.Add(header.Name, header.Value)
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	authorized, err := s.authTransport(ctx, r, handshake, base, scope)
	if err != nil {
		return nil, err
	}
	client.Transport = originAuthTransport{base: base, authorized: authorized, origin: handshake.URL}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !r.Settings.FollowRedirects || len(via) > r.Settings.MaxRedirects {
			return http.ErrUseLastResponse
		}
		if !sameOrigin(req.URL, handshake.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("X-WSSE")
		}
		return nil
	}
	options := &websocket.DialOptions{HTTPClient: client, HTTPHeader: handshake.Header, Host: handshake.Header.Get("Host")}
	for _, protocol := range strings.Split(handshake.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if protocol = strings.TrimSpace(protocol); protocol != "" {
			options.Subprotocols = append(options.Subprotocols, protocol)
		}
	}
	options.HTTPHeader.Del("Sec-WebSocket-Protocol")
	options.HTTPHeader.Del("Host")
	connection, response, err := websocket.Dial(ctx, target, options)
	if err != nil {
		return nil, fmt.Errorf("WebSocket handshake: %w", err)
	}
	defer connection.CloseNow()
	connection.SetReadLimit(maxBody)
	collector := newStreamCollector("websocket", target, s.OnStream)
	collector.response.Status, collector.response.StatusText = 101, "Connected"
	if response != nil {
		collector.response.Headers = response.Header.Clone()
	}
	collector.emit()
	writeErrors := make(chan error, 1)
	write := func(message collection.Message) error {
		typ := websocket.MessageText
		data := []byte(scope.Interpolate(message.Data))
		if message.Type == "binary" {
			typ = websocket.MessageBinary
			var err error
			data, err = base64.StdEncoding.DecodeString(string(data))
			if err != nil {
				return fmt.Errorf("binary WebSocket messages require base64 data: %w", err)
			}
		}
		return connection.Write(ctx, typ, data)
	}
	var selected *collection.Message
	for i := range r.Body.Messages {
		if selected == nil || r.Body.Messages[i].Selected {
			selected = &r.Body.Messages[i]
		}
		if r.Body.Messages[i].Selected {
			break
		}
	}
	if sent.Body != "" {
		message := collection.Message{Type: "text", Data: sent.Body}
		selected = &message
	}
	if selected != nil {
		if err := write(*selected); err != nil {
			return collector.snapshot(), err
		}
		sent.Body = scope.Interpolate(selected.Data)
	}
	if s.StreamInput != nil || r.Settings.KeepAliveIntervalMS > 0 {
		go func() {
			var ticker *time.Ticker
			var ticks <-chan time.Time
			if r.Settings.KeepAliveIntervalMS > 0 {
				ticker = time.NewTicker(time.Duration(r.Settings.KeepAliveIntervalMS) * time.Millisecond)
				ticks = ticker.C
				defer ticker.Stop()
			}
			for {
				select {
				case <-ctx.Done():
					return
				case message, ok := <-s.StreamInput:
					if !ok {
						return
					}
					if err := write(message); err != nil {
						writeErrors <- err
						cancel()
						return
					}
				case <-ticks:
					pingContext, stop := context.WithTimeout(ctx, 10*time.Second)
					err := connection.Ping(pingContext)
					stop()
					if err != nil {
						writeErrors <- err
						cancel()
						return
					}
				}
			}
		}()
	}
	for {
		typ, data, err := connection.Read(ctx)
		if err != nil {
			collector.response.StatusText = "Disconnected"
			select {
			case writeError := <-writeErrors:
				return collector.snapshot(), writeError
			default:
			}
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway || s.StreamInteractive && errors.Is(ctx.Err(), context.Canceled) {
				return collector.snapshot(), nil
			}
			return collector.snapshot(), err
		}
		message := struct {
			Type string `json:"type"`
			Data any    `json:"data"`
		}{Type: "text", Data: string(data)}
		if typ == websocket.MessageBinary {
			message.Type, message.Data = "binary", base64.StdEncoding.EncodeToString(data)
		} else if json.Valid(data) {
			message.Type = "json"
			message.Data = json.RawMessage(data)
		}
		encoded, _ := json.Marshal(message)
		if err := collector.append(encoded); err != nil {
			return collector.snapshot(), err
		}
		if !s.StreamInteractive && s.StreamMessages > 0 && len(collector.messages) >= s.StreamMessages {
			connection.Close(websocket.StatusNormalClosure, "")
			collector.response.StatusText = "Closed"
			return collector.snapshot(), nil
		}
	}
}
