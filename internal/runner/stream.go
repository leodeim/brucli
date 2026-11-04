package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
)

type streamCollector struct {
	response script.Response
	messages []json.RawMessage
	size     int
	start    time.Time
	onStream func(*script.Response)
}

func newStreamCollector(protocol, target string, onStream func(*script.Response)) *streamCollector {
	return &streamCollector{response: script.Response{Protocol: protocol, URL: target, Headers: http.Header{}, StatusText: "OK"}, start: time.Now(), onStream: onStream}
}

func (c *streamCollector) append(data json.RawMessage) error {
	if c.size+len(data) > maxBody || len(c.messages) >= 10000 {
		return errors.New("stream exceeds the retained response limit (64 MiB or 10000 messages)")
	}
	c.size += len(data)
	c.messages = append(c.messages, append(json.RawMessage(nil), data...))
	if c.onStream != nil {
		c.emit()
	}
	return nil
}

func (c *streamCollector) snapshot() *script.Response {
	response := c.response
	response.Headers = c.response.Headers.Clone()
	response.Duration = time.Since(c.start)
	if len(c.messages) == 1 && response.Protocol == "grpc" {
		response.Body = append([]byte(nil), c.messages[0]...)
	} else if len(c.messages) == 0 {
		response.Body = []byte("[]")
	} else {
		response.Body, _ = json.Marshal(c.messages)
	}
	return &response
}

func (c *streamCollector) emit() {
	if c.onStream != nil {
		c.onStream(c.snapshot())
	}
}

func (s *Session) streamContext(ctx context.Context, r *collection.Request) (context.Context, context.CancelFunc) {
	timeout := s.StreamTimeout
	if r.Settings.TimeoutMS > 0 {
		timeout = time.Duration(r.Settings.TimeoutMS) * time.Millisecond
	}
	if timeout <= 0 && !s.StreamInteractive {
		timeout = DefaultTimeout
	}
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}
