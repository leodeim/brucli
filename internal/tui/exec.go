package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/runner"
	"github.com/leodeim/brucli/internal/script"
)

// runState is the request or run in flight. Requests run on clones and a forked session, so the
// UI never shares state with the goroutine; the fork is adopted when it finishes.
type runState struct {
	name    string
	reqs    []*collection.Request
	origin  map[*collection.Request]*collection.Request
	results []*runner.Result
	current *collection.Request
	active  bool
	cancel  context.CancelFunc
	events  chan runEvent
	stream  chan *script.Response
	input   chan collection.Message
}

type streamProgressMsg struct {
	state    *runState
	response *script.Response
}

func (s *runState) waitStream() tea.Cmd {
	return func() tea.Msg {
		response, ok := <-s.stream
		if !ok {
			return nil
		}
		return streamProgressMsg{state: s, response: response}
	}
}

type runEvent struct {
	res  *runner.Result
	fork *runner.Session
}

type sentMsg struct {
	req  *collection.Request
	res  *runner.Result
	fork *runner.Session
}

type runStepMsg runEvent

type editedMsg struct {
	text      string
	err       error
	unchanged bool
	commit    func(string) tea.Cmd
}

// sendTarget is what s sends: the request under the sidebar cursor while the sidebar has focus,
// else the open one.
func (m *Model) sendTarget() *collection.Request {
	if m.focus == focusTree {
		if n := m.tree.selected(); n != nil && n.req != nil {
			return n.req
		}
	}
	return m.current
}

func (m *Model) sendSelected() tea.Cmd {
	if m.busy {
		if m.run != nil && m.run.input != nil && m.sendTarget() == m.run.current {
			return m.composeWebSocketMessage()
		}
		m.setStatus(errors.New("esc cancels it"), "a request is already running")
		return nil
	}
	r := m.sendTarget()
	if r != m.current {
		m.open(r)
		m.focus = focusTree
	}
	return m.send(r)
}

func (m *Model) send(r *collection.Request) tea.Cmd {
	if r == nil {
		return nil
	}
	if m.busy {
		m.setStatus(fmt.Errorf("esc cancels it"), "a request is already running")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	clone, fork := r.Clone(), m.s.Fork()
	m.busy = true
	m.run = &runState{name: r.Name, current: r, active: true, cancel: cancel}
	state := m.run
	if r.Kind == "websocket" || r.Kind == "grpc" {
		state.stream = make(chan *script.Response, 16)
		fork.OnStream = func(response *script.Response) {
			select {
			case state.stream <- response:
			case <-ctx.Done():
			}
		}
		if r.Kind == "websocket" {
			state.input = make(chan collection.Message, 8)
			fork.StreamInput = state.input
			fork.StreamInteractive = true
		}
	}
	m.setStatus(nil, "sending %s…", r.Name)
	execute := func() tea.Msg {
		defer cancel()
		if state.stream != nil {
			defer close(state.stream)
		}
		return sentMsg{req: r, res: fork.Execute(ctx, clone), fork: fork}
	}
	if state.stream != nil {
		return tea.Batch(execute, state.waitStream())
	}
	return execute
}

func (m *Model) onStream(msg streamProgressMsg) tea.Cmd {
	if m.run != msg.state || !m.busy {
		return nil
	}
	m.results[msg.state.current] = &runner.Result{Request: msg.state.current, Response: msg.response}
	if msg.response.Protocol == "websocket" {
		m.setStatus(nil, "connected · s sends a message · esc disconnects")
	} else {
		m.setStatus(nil, "receiving gRPC messages · esc cancels")
	}
	return msg.state.waitStream()
}

func (m *Model) composeWebSocketMessage() tea.Cmd {
	state := m.run
	fields := []*field{choiceField("Type", []string{"text", "json", "binary"}, "text"), textField("Message", "")}
	return m.openForm("Send WebSocket message", "Binary messages use base64 data.", fields, 1, func(values []string) tea.Cmd {
		if m.run != state || !m.busy {
			m.setStatus(fmt.Errorf("connect again to send a message"), "WebSocket is closed")
			return nil
		}
		select {
		case state.input <- collection.Message{Type: values[0], Data: values[1]}:
			m.setStatus(nil, "message queued · esc disconnects")
		default:
			m.setStatus(fmt.Errorf("wait for queued messages to send"), "WebSocket queue is full")
		}
		return nil
	})
}

func (m *Model) onSent(msg sentMsg) tea.Cmd {
	m.busy, m.run = false, nil
	m.s.Adopt(msg.fork)
	m.results[msg.req] = msg.res
	m.noteEnv(msg.res)
	if msg.req == m.current {
		m.resp.scroll = 0
	}
	res := msg.res
	switch {
	case res.Err != nil:
		m.setStatus(res.Err, "%s failed", msg.req.Name)
	case res.Skipped:
		m.setStatus(nil, "%s skipped by its script", msg.req.Name)
	case res.Failed():
		m.setStatus(fmt.Errorf("%s", firstFailure(res)), "%s %d", msg.req.Name, res.Response.Status)
	default:
		m.setStatus(nil, "%s %d %s in %s", msg.req.Name, res.Response.Status, res.Response.StatusText, elapsed(res.Response.Duration))
	}
	return nil
}

// noteEnv marks the environment unsaved when scripts changed it without persist: true.
func (m *Model) noteEnv(res *runner.Result) {
	if m.s.Env == nil {
		return
	}
	for _, set := range res.EnvSet {
		if !set.Persist {
			m.envDirty[m.s.Env] = true
		}
	}
}

func (m *Model) startRun(f *collection.Folder, reqs []*collection.Request, include, exclude []string) tea.Cmd {
	name := f.Name
	if f == m.c.Root {
		name = "collection"
	}
	if len(include) > 0 {
		name += " · tags " + strings.Join(include, ",")
	}
	if len(exclude) > 0 {
		name += " · skip " + strings.Join(exclude, ",")
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &runState{name: name, reqs: reqs, origin: map[*collection.Request]*collection.Request{}, active: true, cancel: cancel, events: make(chan runEvent)}
	clones := make([]*collection.Request, len(reqs))
	for i, r := range reqs {
		clones[i] = r.Clone()
		st.origin[clones[i]] = r
	}
	if len(reqs) > 0 {
		st.current = reqs[0]
	}
	fork := m.s.Fork()
	go func() {
		defer cancel()
		fork.Run(ctx, clones, func(res *runner.Result) { st.events <- runEvent{res: res} })
		st.events <- runEvent{fork: fork}
		close(st.events)
	}()
	m.busy, m.run, m.lastRun = true, st, st
	m.modal = &runModal{}
	return st.wait()
}

func (st *runState) wait() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-st.events
		if !ok {
			return nil
		}
		return runStepMsg(ev)
	}
}

func (m *Model) onRunStep(msg runStepMsg) tea.Cmd {
	st := m.run
	if st == nil {
		return nil
	}
	if msg.fork != nil {
		m.s.Adopt(msg.fork)
		st.active, st.current, m.busy = false, nil, false
		var counts runner.Counts
		for _, r := range st.results {
			counts.Add(r)
		}
		m.setStatus(nil, "run finished: %s", counts)
		return nil
	}
	res := msg.res
	orig := st.origin[res.Request]
	st.results = append(st.results, res)
	m.results[orig] = res
	m.noteEnv(res)
	st.current = nil
	for i, r := range st.reqs {
		if r == orig && i+1 < len(st.reqs) {
			st.current = st.reqs[i+1]
		}
	}
	return st.wait()
}

func (m *Model) cancelSend() tea.Cmd {
	if m.run != nil && m.run.active {
		m.run.cancel()
		m.setStatus(nil, "cancelling")
	}
	return nil
}

// external edits text in $EDITOR; commit gets the saved text.
func (m *Model) external(text, ext string, commit func(string) tea.Cmd) tea.Cmd {
	f, err := os.CreateTemp("", "brucli-*"+ext)
	if err != nil {
		m.setStatus(err, "editor")
		return nil
	}
	path := f.Name()
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		m.setStatus(err, "editor")
		return nil
	}
	return tea.ExecProcess(editorCommand(cmp.Or(m.editor, "vi"), path), func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return editedMsg{err: err}
		}
		b, err := os.ReadFile(path)
		edited := string(b)
		// Editors end the file with a newline; drop it unless the text had one.
		if !strings.HasSuffix(text, "\n") {
			edited = strings.TrimSuffix(edited, "\n")
		}
		if err == nil && edited == text {
			return editedMsg{unchanged: true}
		}
		return editedMsg{text: edited, err: err, commit: commit}
	})
}

// editorCommand runs $EDITOR through the shell, as git does, so it may hold arguments and quoted paths.
func editorCommand(editor, path string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		args := strings.Fields(editor)
		return exec.Command(args[0], append(args[1:], path)...)
	}
	return exec.Command("sh", "-c", editor+` "$1"`, "brucli-editor", path)
}

func (m *Model) onEdited(msg editedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(msg.err, "editor")
		return nil
	}
	if msg.unchanged {
		m.setStatus(nil, "no changes from the editor")
		return nil
	}
	return msg.commit(msg.text)
}

func (m *Model) copyCurl() tea.Cmd {
	if m.current != nil && (m.current.Kind == "grpc" || m.current.Kind == "websocket") {
		m.setStatus(fmt.Errorf("select an HTTP or GraphQL request"), "curl export is available for HTTP requests")
		return nil
	}
	res := m.results[m.current]
	if res == nil || res.Sent == nil {
		m.setStatus(fmt.Errorf("send it first"), "no request to copy")
		return nil
	}
	return m.copy(runner.Curl(res.Sent), "curl (with credentials)")
}

func (m *Model) copyBody() tea.Cmd {
	res := m.results[m.current]
	if res == nil || res.Response == nil {
		m.setStatus(fmt.Errorf("send it first"), "no response to copy")
		return nil
	}
	return m.copy(pretty(res.Response.Body), "response body")
}

func (m *Model) copy(text, what string) tea.Cmd {
	if err := clipboard.WriteAll(text); err != nil {
		m.setStatus(err, "copying %s", what)
		return nil
	}
	m.setStatus(nil, "copied %s", what)
	return nil
}
