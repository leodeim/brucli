package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/leodeim/brucli/internal/collection"
)

func selectedMessage(r *collection.Request) int {
	for i, message := range r.Body.Messages {
		if message.Selected {
			return i
		}
	}
	return 0
}

func (m *Model) editProtocolMessage(r *collection.Request, index int) tea.Cmd {
	if len(r.Body.Messages) == 0 {
		r.Body.Messages = append(r.Body.Messages, collection.Message{Title: "message 1", Type: "json", Data: "{}"})
		m.changed(r)
	}
	index = min(index, len(r.Body.Messages)-1)
	if r.Kind == "websocket" {
		for i := range r.Body.Messages {
			if r.Body.Messages[i].Selected != (i == index) {
				m.changed(r)
			}
			r.Body.Messages[i].Selected = i == index
		}
	}
	message := &r.Body.Messages[index]
	language := "text"
	if r.Kind == "grpc" || message.Type == "json" {
		language = "json"
	}
	m.openEditor(message.Title, language, message.Data, func(value string) {
		if value != message.Data {
			message.Data = value
			m.changed(r)
		}
	})
	return nil
}

func protocolBodyTab(m *Model, r *collection.Request, spec tabSpec) tabSpec {
	if r.Kind == "grpc" {
		spec.rows = append(spec.rows, row{line: "method  " + r.Method, sel: true, what: "gRPC method", edit: m.pickMethod})
		spec.rows = append(spec.rows, row{line: "proto   " + r.ProtoFilePath + stMuted.Render(" (blank uses collection protos or reflection)"), sel: true, what: "proto file", edit: func() tea.Cmd {
			return m.ask("Proto file", r.ProtoFilePath, func(value string) tea.Cmd { r.ProtoFilePath = value; m.changed(r); return nil })
		}})
		if r.MethodType != "" {
			spec.rows = append(spec.rows, text(stMuted.Render("stream  "+r.MethodType)))
		}
	} else {
		spec.rows = append(spec.rows, text(stMuted.Render("s connects and sends the selected message; s while connected sends another; esc disconnects.")))
	}
	for i := range r.Body.Messages {
		message := &r.Body.Messages[i]
		label := message.Title
		if r.Kind == "websocket" {
			label = checkbox(message.Selected) + " " + label + "  " + message.Type
		}
		spec.rows = append(spec.rows, row{line: stBold.Render(label), sel: true, what: "message", edit: func() tea.Cmd { return m.editProtocolMessage(r, i) }, rename: func() tea.Cmd {
			return m.ask("Message name", message.Title, func(value string) tea.Cmd { message.Title = value; m.changed(r); return nil })
		}, del: func() { r.Body.Messages = slices.Delete(r.Body.Messages, i, i+1); m.changed(r) }, toggle: func() {
			if r.Kind == "websocket" {
				for j := range r.Body.Messages {
					r.Body.Messages[j].Selected = j == i
				}
				m.changed(r)
			}
		}})
		if r.Kind == "websocket" {
			spec.rows = append(spec.rows, row{line: stMuted.Render("  message type: ") + message.Type, sel: true, what: "message type", edit: func() tea.Cmd {
				types := []string{"text", "json", "binary"}
				return m.choose("Message type", []pickItem{{"text", "text frame"}, {"json", "JSON text frame"}, {"binary", "base64 data"}}, slices.Index(types, message.Type), func(index int) tea.Cmd { message.Type = types[index]; m.changed(r); return nil })
			}})
		}
		for _, line := range strings.Split(message.Data, "\n") {
			spec.rows = append(spec.rows, row{line: "  " + line, click: func() tea.Cmd { return m.editProtocolMessage(r, i) }})
		}
	}
	spec.edit = func() tea.Cmd { return m.editProtocolMessage(r, selectedMessage(r)) }
	spec.addLabel = "Add message"
	spec.add = func() tea.Cmd {
		r.Body.Messages = append(r.Body.Messages, collection.Message{Title: fmt.Sprintf("message %d", len(r.Body.Messages)+1), Type: "json", Data: "{}"})
		m.changed(r)
		return m.editProtocolMessage(r, len(r.Body.Messages)-1)
	}
	return spec
}
