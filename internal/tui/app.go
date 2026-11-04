// Package tui is brucli's terminal UI for Bruno collections.
package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/runner"
)

const (
	minWidth  = 80
	minHeight = 20
)

type focus int

const (
	focusTree focus = iota
	focusRequest
	focusResponse
)

// modal takes the keys and draws over the panes until it clears Model.modal.
type modal interface {
	key(m *Model, msg tea.KeyMsg) tea.Cmd
	view(m *Model, w, h int) string
	help() string
}

// Optional modal behaviour: a fixed position instead of the center, mouse clicks and the wheel
// (x and y relative to the modal's box), and closing on a click outside.
type (
	placed interface {
		at(m *Model, w, h, bw, bh int) (x, y int)
	}
	clicker interface {
		click(m *Model, x, y int) tea.Cmd
	}
	wheeler interface {
		wheel(m *Model, d int)
	}
	dismisser interface{ dismissible() }
)

// prompt is a one-line input; commit receives the text on enter. An inline prompt is drawn in
// place of the request URL instead of the footer.
type prompt struct {
	label  string
	input  textinput.Model
	commit func(string) tea.Cmd
	inline bool
}

// Config is what main hands the TUI.
type Config struct {
	Session *runner.Session
	// Editor is the external editor command for ctrl+e, usually $VISUAL or $EDITOR.
	Editor string
	// Mouse says the program was started with mouse reporting on.
	Mouse bool
}

// Model is the root Bubble Tea model.
type Model struct {
	c      *collection.Collection
	s      *runner.Session
	editor string

	tree    *tree
	current *collection.Request
	focus   focus
	req     requestPane
	resp    responsePane

	results  map[*collection.Request]*runner.Result
	dirty    map[*collection.Request]bool
	envDirty map[*collection.Environment]bool
	// pristine is each opened request as last loaded or saved, for reverting edits.
	pristine map[*collection.Request]*collection.Request
	// busy is set while a request or run is in flight; env and variable edits wait for it.
	busy bool
	run  *runState
	// lastRun is the last folder run, which Show last run reopens.
	lastRun *runState

	modal   modal
	prompt  *prompt
	reveal  bool
	zoom    bool
	mouseOn bool

	// Where the last frame drew the modal and the environment label, for mouse clicks.
	modalRect rect
	envX      int
	// footerRight says the footer's right hint, which a click opens the palette from, was drawn.
	footerRight bool

	width, height int
	status        string
	statusErr     bool
	quitting      bool
}

// New returns the root model.
func New(cfg Config) *Model {
	c := cfg.Session.Collection
	m := &Model{
		c:        c,
		s:        cfg.Session,
		editor:   cfg.Editor,
		mouseOn:  cfg.Mouse,
		tree:     newTree(c.Root),
		results:  map[*collection.Request]*runner.Result{},
		dirty:    map[*collection.Request]bool{},
		envDirty: map[*collection.Environment]bool{},
		pristine: map[*collection.Request]*collection.Request{},
		req:      requestPane{cursor: map[reqTab]int{}},
	}
	if all := c.Root.AllRequests(); len(all) > 0 {
		m.open(all[0])
		m.focus = focusTree
	}
	if len(c.Warnings) > 0 {
		m.setStatus(fmt.Errorf("%s", strings.Join(c.Warnings, "; ")), "skipped %d file(s)", len(c.Warnings))
	} else {
		m.setStatus(nil, "? lists every action · F10 or a click opens the menus")
	}
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) setStatus(err error, format string, args ...any) {
	m.statusErr = err != nil
	m.status = fmt.Sprintf(format, args...)
	if err != nil {
		m.status += ": " + err.Error()
	}
}

// open shows r in the request pane.
func (m *Model) open(r *collection.Request) {
	if m.current != r {
		m.req.scroll = 0
		m.req.editor = nil
		m.resp.scroll = 0
	}
	m.current = r
	if _, ok := m.pristine[r]; !ok && !m.dirty[r] {
		m.pristine[r] = r.Clone()
	}
	m.tree.reveal(r)
	m.focus = focusRequest
}

func (m *Model) changed(r *collection.Request) { m.dirty[r] = true }

func (m *Model) requestMarks(r *collection.Request) string {
	var marks string
	if m.dirty[r] {
		marks += stWarn.Render(" ●")
	}
	if m.run == nil || !m.run.active {
		if res, ok := m.results[r]; ok {
			switch {
			case res.Skipped:
				marks += stMuted.Render(" -")
			case res.Failed():
				marks += stErr.Render(" ✗")
			default:
				marks += stOK.Render(" ✓")
			}
		}
	}
	if m.sending(r) {
		marks += stInfo.Render(" …")
	}
	return marks
}

func (m *Model) sending(r *collection.Request) bool {
	return m.run != nil && m.run.active && m.run.current == r
}

// ask opens the one-line prompt in the footer.
func (m *Model) ask(label, value string, commit func(string) tea.Cmd) tea.Cmd {
	in := textinput.New()
	in.Prompt = ""
	in.Cursor.SetMode(cursor.CursorStatic)
	in.Width = max(10, m.width-len(label)-4)
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	m.prompt = &prompt{label: label, input: in, commit: commit}
	return nil
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case sentMsg:
		cmd = m.onSent(msg)
	case streamProgressMsg:
		cmd = m.onStream(msg)
	case runStepMsg:
		cmd = m.onRunStep(msg)
	case editedMsg:
		cmd = m.onEdited(msg)
	case tea.KeyMsg:
		// Some terminals and multiplexers send LF for enter, which arrives as ctrl+j.
		if msg.Type == tea.KeyCtrlJ {
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		}
		cmd = m.key(msg)
	case tea.MouseMsg:
		cmd = m.mouse(tea.MouseEvent(msg))
	}
	// The editor lives in the request pane; leaving the pane by any route ends it.
	if m.req.editor != nil && (m.focus != focusRequest || m.current == nil) {
		m.req.editor = nil
	}
	return m, cmd
}

func (m *Model) key(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		return m.quit()
	}
	if p := m.prompt; p != nil {
		switch k {
		case "esc":
			m.prompt = nil
			return nil
		case "enter":
			m.prompt = nil
			return p.commit(p.input.Value())
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd
	}
	if m.modal != nil {
		return m.modal.key(m, msg)
	}
	if m.req.editor != nil {
		return m.editorKey(msg)
	}
	if m.focus == focusTree && m.tree.filtering() {
		return m.tree.filterKey(m, msg)
	}
	if cmd, ok := m.dispatch(k); ok {
		return cmd
	}
	switch m.focus {
	case focusTree:
		return m.treeKey(msg)
	case focusRequest:
		return m.req.key(m, msg)
	case focusResponse:
		return m.resp.key(m, msg)
	}
	return nil
}

// editorKey handles the keys of the inline editor; everything not listed here edits text.
func (m *Model) editorKey(msg tea.KeyMsg) tea.Cmd {
	e := m.req.editor
	switch msg.String() {
	case "esc":
		m.req.editor = nil
		if m.dirty[m.current] {
			m.setStatus(nil, "kept in memory · ctrl+s saves the file")
		}
		return nil
	case "ctrl+s":
		return m.save(m.current)
	case "ctrl+r":
		return m.send(m.current)
	case "ctrl+e":
		m.req.editor = nil
		commit := e.commit
		return m.external(e.text(), e.ext(), func(v string) tea.Cmd { commit(v); return nil })
	case "ctrl+p":
		return m.openPalette()
	case "f10":
		return m.openMenu(1)
	}
	e.key(msg)
	return nil
}

func (m *Model) openEditor(what, lang, text string, commit func(string)) {
	m.focus = focusRequest
	m.req.editor = newEditor(what, lang, text, commit)
}

func (m *Model) treeKey(msg tea.KeyMsg) tea.Cmd {
	t := m.tree
	n := t.selected()
	switch msg.String() {
	case "up", "k":
		t.move(-1)
	case "down", "j":
		t.move(1)
	case "pgup":
		t.move(-10)
	case "pgdown":
		t.move(10)
	case "home", "g":
		t.cursor = 0
	case "end", "G":
		t.cursor = max(0, len(t.nodes)-1)
	case "enter", " ", "l", "right", "left", "h":
		if n == nil {
			return nil
		}
		if n.req != nil {
			if k := msg.String(); k == "enter" || k == " " || k == "l" || k == "right" {
				m.open(n.req)
			}
			return nil
		}
		switch msg.String() {
		case "left", "h":
			t.setOpen(n.folder, false)
		case "right", "l":
			t.setOpen(n.folder, true)
		default:
			t.setOpen(n.folder, t.closed[n.folder])
		}
	}
	return nil
}

func (m *Model) quit() tea.Cmd {
	if len(m.dirty) == 0 && len(m.envDirty) == 0 {
		m.quitting = true
		return tea.Quit
	}
	lines := []string{fmt.Sprintf("Unsaved: %d request(s), %d environment(s).", len(m.dirty), len(m.envDirty)), ""}
	lines = append(lines, "y save all and quit · n quit without saving · esc stay")
	m.modal = &confirmModal{
		title: "Unsaved changes",
		lines: lines,
		yes: func() tea.Cmd {
			if cmd := m.saveAll(); m.statusErr {
				return cmd
			}
			m.quitting = true
			return tea.Quit
		},
		no: func() tea.Cmd {
			m.quitting = true
			return tea.Quit
		},
	}
	return nil
}

func (m *Model) save(r *collection.Request) tea.Cmd {
	if r == nil {
		return nil
	}
	if err := r.Save(); err != nil {
		m.setStatus(err, "saving %s", r.Name)
		return nil
	}
	delete(m.dirty, r)
	m.pristine[r] = r.Clone()
	m.setStatus(nil, "saved %s", m.rel(r.Path))
	return nil
}

func (m *Model) saveAll() tea.Cmd {
	n := 0
	for r := range m.dirty {
		if err := r.Save(); err != nil {
			m.setStatus(err, "saving %s", r.Name)
			return nil
		}
		delete(m.dirty, r)
		m.pristine[r] = r.Clone()
		n++
	}
	for env := range m.envDirty {
		if m.busy {
			// A running script may persist to the same file; its changes are adopted when it ends.
			m.setStatus(errors.New("save again when it finishes"), "saved %d file(s); environments wait for the running request", n)
			return nil
		}
		if err := env.Save(); err != nil {
			m.setStatus(err, "saving environment %s", env.Name)
			return nil
		}
		delete(m.envDirty, env)
		n++
	}
	m.setStatus(nil, "saved %d file(s)", n)
	return nil
}

func (m *Model) revertRequest() tea.Cmd {
	r := m.current
	saved := m.pristine[r]
	if r == nil || saved == nil {
		return nil
	}
	m.modal = &confirmModal{
		title: "Revert " + r.Name + "?",
		lines: []string{"Drops the unsaved edits and goes back to " + m.rel(r.Path) + " as last saved.", "", "y revert · n or esc keep editing"},
		yes: func() tea.Cmd {
			*r = *saved.Clone()
			delete(m.dirty, r)
			m.req.editor = nil
			m.setStatus(nil, "reverted %s", r.Name)
			return nil
		},
	}
	return nil
}

func (m *Model) rel(path string) string {
	return strings.TrimPrefix(strings.TrimPrefix(path, m.c.Dir), string(os.PathSeparator))
}

func (m *Model) folderName(f *collection.Folder) string {
	if f == m.c.Root {
		return "whole collection"
	}
	return f.Name
}

func (m *Model) cyclePane(d int) tea.Cmd {
	m.focus = focus((int(m.focus) + 3 + d) % 3)
	return nil
}

func (m *Model) toggleZoom() tea.Cmd {
	m.zoom = !m.zoom
	return nil
}

func (m *Model) toggleSecrets() tea.Cmd {
	m.reveal = !m.reveal
	m.setStatus(nil, "secrets %s", map[bool]string{true: "shown", false: "masked"}[m.reveal])
	return nil
}

func (m *Model) toggleMouse() tea.Cmd {
	m.mouseOn = !m.mouseOn
	if m.mouseOn {
		m.setStatus(nil, "mouse on: click and scroll")
		return tea.EnableMouseCellMotion
	}
	m.setStatus(nil, "mouse off: the terminal selects text again")
	return tea.DisableMouse
}

// cycleTab switches the tab of the response pane when it has focus, else of the request pane.
func (m *Model) cycleTab(d int) tea.Cmd {
	if m.focus == focusResponse {
		m.resp.switchTab(respTab((int(m.resp.tab) + len(respTabNames) + d) % len(respTabNames)))
		return nil
	}
	m.req.switchTab(reqTab((int(m.req.tab) + len(reqTabNames) + d) % len(reqTabNames)))
	return nil
}

type rect struct{ x, y, w, h int }

func (r rect) has(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

// inner is the content area of a framed pane: inside the border, one column of padding each side.
func (r rect) inner() rect { return rect{r.x + 2, r.y + 1, max(0, r.w-4), max(0, r.h-2)} }

// layout is where the framed panes go in this frame; a zero rect is a hidden pane. View draws
// with it and mouse clicks are mapped through it.
type layout struct{ tree, req, resp rect }

func (m *Model) layout() layout {
	w, bodyH := m.width, m.height-3
	full := rect{0, 1, w, bodyH}
	if m.zoom {
		switch m.focus {
		case focusTree:
			return layout{tree: full}
		case focusRequest:
			return layout{req: full}
		}
		return layout{resp: full}
	}
	treeW := min(44, max(30, w*3/10))
	reqH := max(9, bodyH*9/20)
	if m.req.editor != nil {
		reqH = max(reqH, bodyH*3/4)
	}
	return layout{
		tree: rect{0, 1, treeW, bodyH},
		req:  rect{treeW, 1, w - treeW, reqH},
		resp: rect{treeW, 1 + reqH, w - treeW, bodyH - reqH},
	}
}

// View implements tea.Model.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width < minWidth || m.height < minHeight {
		return fmt.Sprintf("brucli needs at least %dx%d (now %dx%d)", minWidth, minHeight, m.width, m.height)
	}
	w, h := m.width, m.height
	l := m.layout()
	type pane struct {
		r     rect
		lines []string
	}
	var panes []pane
	if r := l.tree; r.w > 0 {
		in := r.inner()
		panes = append(panes, pane{r, frame(m.c.Name, m.focus == focusTree, m.tree.view(m, in.w, in.h), r.w, r.h)})
	}
	if r := l.req; r.w > 0 {
		in := r.inner()
		panes = append(panes, pane{r, frame(m.requestTitle(), m.focus == focusRequest, m.req.view(m, in.w, in.h), r.w, r.h)})
	}
	if r := l.resp; r.w > 0 {
		in := r.inner()
		panes = append(panes, pane{r, frame(m.responseTitle(in.w), m.focus == focusResponse, m.resp.view(m, in.w, in.h), r.w, r.h)})
	}
	lines := make([]string, 0, h)
	lines = append(lines, m.header(w))
	for y := 1; y < h-2; y++ {
		var b strings.Builder
		for _, p := range panes {
			if y >= p.r.y && y < p.r.y+p.r.h {
				b.WriteString(p.lines[y-p.r.y])
			}
		}
		lines = append(lines, b.String())
	}
	lines = append(lines, m.footer(w)...)
	screen := strings.Join(lines, "\n")
	m.modalRect = rect{}
	if m.modal != nil {
		box := m.modal.view(m, w, h)
		bw, bh := lipgloss.Width(box), lipgloss.Height(box)
		x, y := max(0, (w-bw)/2), max(0, (h-bh)/2)
		if p, ok := m.modal.(placed); ok {
			x, y = p.at(m, w, h, bw, bh)
		}
		screen = overlay(screen, box, x, y)
		m.modalRect = rect{x, y, bw, bh}
	}
	return screen
}

// frame draws a pane of w×h cells: a rounded border with the title set into its top edge, and
// lines inside with one column of padding. The focused pane gets the accent border.
func frame(title string, focused bool, lines []string, w, h int) []string {
	border, label := tintMuted, tintBold
	if focused {
		border, label = tintAccent, tintLit
	}
	in := max(0, w-4)
	t := label.over(ansi.Truncate(title, max(0, w-6), "…"))
	top := border.wrap("╭─") + " " + t + " " + border.wrap(strings.Repeat("─", max(0, w-5-ansi.StringWidth(t)))+"╮")
	side := border.wrap("│")
	out := make([]string, 0, h)
	out = append(out, top)
	for i := range max(0, h-2) {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, side+" "+fit(l, in)+" "+side)
	}
	return append(out, border.wrap("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
}

// header is the menu bar on the left and the environment on the right.
func (m *Model) header(w int) string {
	open := -1
	if mm, ok := m.modal.(*menuModal); ok {
		open = mm.menu
	}
	var b strings.Builder
	b.WriteString(stTitle.Render(strings.TrimSpace(brand)) + " ")
	for i, t := range menuBar() {
		if i == open {
			b.WriteString(stTabOn.Render(t.name))
		} else {
			b.WriteString(stMenu.Render(t.name))
		}
	}
	left := b.String()
	env := stMuted.Render("no environment ▾")
	if m.s.Env != nil {
		env = "env " + stTitle.Render(m.s.Env.Name) + stMuted.Render(" ▾")
		if m.envDirty[m.s.Env] {
			env += stWarn.Render(" ●")
		}
		if base := m.s.Scope(m.current).Interpolate("{{baseUrl}}"); base != "{{baseUrl}}" {
			env += stMuted.Render(" · " + base)
		}
	}
	if n := len(m.dirty); n > 0 {
		env += stWarn.Render(fmt.Sprintf("  %d unsaved", n))
	}
	room := w - ansi.StringWidth(left) - 2
	env = ansi.Truncate(env, max(0, room), "…")
	m.envX = w - ansi.StringWidth(env)
	return fit(left+strings.Repeat(" ", max(2, w-ansi.StringWidth(left)-ansi.StringWidth(env)))+env, w)
}

// footerRight is the footer's pointer to the menus; a click on it opens the palette.
const footerRight = "? all actions · F10 menu"

func (m *Model) footer(w int) []string {
	if p := m.prompt; p != nil && !p.inline {
		return []string{fit(stTitle.Render(p.label+": ")+p.input.View(), w), fit(stMuted.Render("enter apply · esc cancel"), w)}
	}
	status := m.status
	if m.statusErr {
		status = stErr.Render(status)
	} else if status != "" {
		status = stOK.Render(status)
	}
	var help string
	right := stMuted.Render(footerRight)
	switch {
	case m.prompt != nil:
		help = "enter apply · esc cancel"
	case m.modal != nil:
		help, right = m.modal.help(), ""
	case m.req.editor != nil:
		help = "esc done · ctrl+s save · ctrl+r send · ctrl+z undo · ctrl+y redo · tab indent · ctrl+e $EDITOR"
		if m.req.editor.lang == "json" {
			help = strings.Replace(help, "ctrl+z undo", "ctrl+f format · ctrl+z undo", 1)
		}
		right = ""
	case m.focus == focusTree && m.tree.filtering():
		help = "type to filter · enter keep · esc clear"
	default:
		parts := []string{map[focus]string{
			focusTree:     "↑↓ move · enter open",
			focusRequest:  "↑↓ rows · ←→ tabs",
			focusResponse: "↑↓ scroll · ←→ tabs",
		}[m.focus]}
		help = strings.Join(append(parts, m.hints()...), " · ")
	}
	m.footerRight = right != ""
	room := w - ansi.StringWidth(right) - 2
	hint := stMuted.Render(ansi.Truncate(help, max(0, room), "…"))
	gap := max(1, w-ansi.StringWidth(hint)-ansi.StringWidth(right))
	return []string{fit(status, w), fit(hint+strings.Repeat(" ", gap)+right, w)}
}

func (m *Model) requestTitle() string {
	r := m.current
	if r == nil {
		return "Request"
	}
	path := make([]string, 0, 4)
	for _, f := range r.Chain()[1:] {
		path = append(path, f.Name)
	}
	title := strings.Join(append(path, r.Name), " / ")
	if m.dirty[r] {
		title += " ●"
	}
	if e := m.req.editor; e != nil {
		title += " · editing " + e.what
	}
	return title
}

func (m *Model) responseTitle(w int) string {
	res := m.results[m.current]
	switch {
	case m.current == nil:
		return "Response"
	case m.sending(m.current) && res != nil && res.Response != nil && res.Response.Protocol == "websocket":
		return "Response · connected (s sends a message · esc disconnects)"
	case m.sending(m.current) && res != nil && res.Response != nil && res.Response.Protocol == "grpc":
		return "Response · receiving gRPC messages (esc cancels)"
	case m.sending(m.current):
		return "Response · sending… (esc cancels)"
	case res == nil:
		return "Response"
	case res.Skipped:
		return "Response · skipped by bru.runner.skipRequest()"
	case res.Err != nil:
		return ansi.Truncate("Response · "+res.Err.Error(), w, "…")
	}
	r := res.Response
	parts := []string{
		responseStyle(r).Render(fmt.Sprintf("%d %s", r.Status, r.StatusText)),
		elapsed(r.Duration),
		size(len(r.Body)),
	}
	if summary := checks(res); summary != "" {
		parts = append(parts, summary)
	}
	return "Response · " + strings.Join(parts, " · ")
}

func checks(res *runner.Result) string {
	total, passed := len(res.Assertions)+len(res.Tests), 0
	for _, a := range res.Assertions {
		if a.Passed {
			passed++
		}
	}
	for _, t := range res.Tests {
		if t.Passed {
			passed++
		}
	}
	var parts []string
	if total > 0 {
		st := stOK
		if passed < total {
			st = stErr
		}
		parts = append(parts, st.Render(fmt.Sprintf("%d/%d checks", passed, total)))
	}
	if n := len(res.ScriptErrors); n > 0 {
		parts = append(parts, stErr.Render(fmt.Sprintf("%d script error(s)", n)))
	}
	return strings.Join(parts, " · ")
}

// elapsed shows a duration in milliseconds, the way Bruno does.
func elapsed(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	return d.Round(time.Millisecond).String()
}

func size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// overlay draws box over screen with its top-left corner at x, y.
func overlay(screen, box string, x, y int) string {
	lines := strings.Split(screen, "\n")
	boxLines := strings.Split(box, "\n")
	bw := 0
	for _, l := range boxLines {
		bw = max(bw, ansi.StringWidth(l))
	}
	for i, bl := range boxLines {
		row := y + i
		if row >= len(lines) {
			break
		}
		base := lines[row]
		lines[row] = ansi.Truncate(base, x, "") + fit(bl, bw) + ansi.TruncateLeft(base, x+bw, "")
	}
	return strings.Join(lines, "\n")
}

// boxTop is the first content line inside a box: below the border, the title and a blank line.
const boxTop = 3

func box(title string, lines []string, w int) string {
	fitted := make([]string, len(lines))
	for i, l := range lines {
		fitted[i] = ansi.Truncate(l, w-4, "…")
	}
	body := stTitle.Render(ansi.Truncate(title, w-4, "…")) + "\n\n" + strings.Join(fitted, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colAccent).
		Padding(0, 1).
		Width(w).
		Render(body)
}

func modalWidth(w int) int { return min(max(w*2/3, 60), w-4) }
