package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const indentUnit = 2

type edSnap struct {
	text     string
	row, col int
}

// editor is the multi-line code editor the request pane opens for bodies, scripts and docs.
// Every change is written back through commit at once, so a send always uses what is on screen.
type editor struct {
	what   string
	lang   string
	lines  [][]rune
	row    int
	col    int
	goal   int // column kept across vertical moves; -1 when unset
	top    int
	shift  int // first visible column
	follow bool

	undo, redo []edSnap
	// kind is the last edit's kind; consecutive edits of the same kind share an undo step.
	kind   string
	commit func(string)

	note    string
	noteErr bool
	problem error
	checked bool

	height int // text rows at the last view, for paging and clicks
	gutter int
}

func newEditor(what, lang, text string, commit func(string)) *editor {
	e := &editor{what: what, lang: lang, commit: commit, goal: -1, follow: true}
	e.setText(text)
	return e
}

func (e *editor) setText(s string) {
	parts := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	e.lines = make([][]rune, len(parts))
	for i, p := range parts {
		e.lines[i] = []rune(p)
	}
	e.clamp()
	e.checked = false
}

func (e *editor) text() string {
	var b strings.Builder
	for i, l := range e.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(l))
	}
	return b.String()
}

func (e *editor) code() bool { return e.lang == "json" || e.lang == "js" }

func (e *editor) clamp() {
	e.row = max(0, min(e.row, len(e.lines)-1))
	e.col = max(0, min(e.col, len(e.lines[e.row])))
}

// ext is the file extension for $EDITOR.
func (e *editor) ext() string {
	if x, ok := map[string]string{"json": ".json", "js": ".js", "md": ".md", "xml": ".xml"}[e.lang]; ok {
		return x
	}
	return ".txt"
}

// apply runs an edit; f reports whether it changed anything.
func (e *editor) apply(kind string, f func() bool) {
	before := edSnap{e.text(), e.row, e.col}
	if !f() {
		return
	}
	if kind == "" || kind != e.kind {
		e.undo = append(e.undo, before)
		if len(e.undo) > 500 {
			e.undo = e.undo[1:]
		}
	}
	e.redo = nil
	e.kind = kind
	e.changed()
}

func (e *editor) changed() {
	e.follow, e.goal, e.note, e.checked = true, -1, "", false
	if e.commit != nil {
		e.commit(e.text())
	}
}

// move runs a cursor movement; it ends the current undo step.
func (e *editor) move(f func()) {
	f()
	e.kind, e.follow, e.note = "", true, ""
	e.clamp()
}

func (e *editor) key(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyRunes:
		if msg.Alt {
			switch string(msg.Runes) {
			case "b":
				e.move(e.wordLeft)
			case "f":
				e.move(e.wordRight)
			}
			return
		}
		if msg.Paste {
			s := strings.ReplaceAll(strings.ReplaceAll(string(msg.Runes), "\r\n", "\n"), "\r", "\n")
			e.apply("", func() bool { e.insert(s); return true })
			return
		}
		e.apply("type", func() bool {
			for _, r := range msg.Runes {
				e.typeRune(r)
			}
			return true
		})
	case tea.KeySpace:
		e.apply("type", func() bool { e.insert(" "); return true })
	case tea.KeyEnter:
		e.apply("", func() bool { e.newline(); return true })
	case tea.KeyTab:
		e.apply("", func() bool { e.insert(strings.Repeat(" ", indentUnit-e.col%indentUnit)); return true })
	case tea.KeyShiftTab:
		e.apply("", e.dedent)
	case tea.KeyBackspace, tea.KeyCtrlH:
		e.apply("delete", e.backspace)
	case tea.KeyDelete:
		e.apply("delete", e.del)
	case tea.KeyCtrlK:
		e.apply("", e.deleteLine)
	case tea.KeyCtrlD:
		e.apply("", e.duplicateLine)
	case tea.KeyCtrlZ:
		e.step(&e.undo, &e.redo, "nothing to undo")
	case tea.KeyCtrlY:
		e.step(&e.redo, &e.undo, "nothing to redo")
	case tea.KeyCtrlF:
		e.format()
	case tea.KeyUp:
		e.move(func() { e.vmove(-1) })
	case tea.KeyDown:
		e.move(func() { e.vmove(1) })
	case tea.KeyPgUp:
		e.move(func() { e.vmove(-max(1, e.height-1)) })
	case tea.KeyPgDown:
		e.move(func() { e.vmove(max(1, e.height-1)) })
	case tea.KeyLeft, tea.KeyCtrlLeft:
		if msg.Alt || msg.Type == tea.KeyCtrlLeft {
			e.move(e.wordLeft)
		} else {
			e.move(e.charLeft)
		}
	case tea.KeyRight, tea.KeyCtrlRight:
		if msg.Alt || msg.Type == tea.KeyCtrlRight {
			e.move(e.wordRight)
		} else {
			e.move(e.charRight)
		}
	case tea.KeyHome:
		e.move(e.home)
	case tea.KeyEnd:
		e.move(func() { e.col, e.goal = len(e.lines[e.row]), -1 })
	case tea.KeyCtrlHome:
		e.move(func() { e.row, e.col, e.goal = 0, 0, -1 })
	case tea.KeyCtrlEnd:
		e.move(func() { e.row = len(e.lines) - 1; e.col, e.goal = len(e.lines[e.row]), -1 })
	}
}

// insert puts s, which may span lines, at the cursor.
func (e *editor) insert(s string) {
	parts := strings.Split(s, "\n")
	line := e.lines[e.row]
	before, after := slices.Clone(line[:e.col]), slices.Clone(line[e.col:])
	if len(parts) == 1 {
		r := []rune(s)
		e.lines[e.row] = append(append(before, r...), after...)
		e.col += len(r)
		return
	}
	repl := make([][]rune, 0, len(parts))
	repl = append(repl, append(before, []rune(parts[0])...))
	for _, p := range parts[1 : len(parts)-1] {
		repl = append(repl, []rune(p))
	}
	last := []rune(parts[len(parts)-1])
	e.col = len(last)
	repl = append(repl, append(last, after...))
	e.lines = slices.Replace(e.lines, e.row, e.row+1, repl...)
	e.row += len(parts) - 1
}

// typeRune inserts r; in code a closing bracket typed on a blank indented line steps back one level.
func (e *editor) typeRune(r rune) {
	if e.code() && strings.ContainsRune("}])", r) && e.col >= indentUnit && blank(e.lines[e.row][:e.col]) {
		e.lines[e.row] = slices.Delete(slices.Clone(e.lines[e.row]), e.col-indentUnit, e.col)
		e.col -= indentUnit
	}
	e.insert(string(r))
}

// newline splits the line at the cursor and indents the new line like the current one, one level
// deeper after an opening bracket; between a bracket pair the closing one moves to its own line.
func (e *editor) newline() {
	line := e.lines[e.row]
	before, after := slices.Clone(line[:e.col]), trimLeft(line[e.col:])
	indent := leading(line)
	if len(indent) > len(before) {
		indent = indent[:len(before)]
	}
	if blank(before) {
		before = nil
	}
	if len(before) == 0 {
		// Enter in a line's indentation opens a line above and leaves this one as it was.
		e.lines = slices.Insert(e.lines, e.row, []rune{})
		e.row++
		return
	}
	inner := slices.Clone(indent)
	opener := lastNonSpace(before)
	open := e.code() && opener != 0 && strings.ContainsRune("{[(", opener)
	if open {
		inner = append(inner, []rune(strings.Repeat(" ", indentUnit))...)
	}
	repl := [][]rune{before, append(slices.Clone(inner), after...)}
	if open && len(after) > 0 && pairs(opener, after[0]) {
		repl = [][]rune{before, slices.Clone(inner), append(slices.Clone(indent), after...)}
	}
	e.lines = slices.Replace(e.lines, e.row, e.row+1, repl...)
	e.row++
	e.col = len(inner)
}

func (e *editor) backspace() bool {
	if e.col == 0 {
		if e.row == 0 {
			return false
		}
		prev := e.lines[e.row-1]
		e.col = len(prev)
		e.lines[e.row-1] = append(slices.Clone(prev), e.lines[e.row]...)
		e.lines = slices.Delete(e.lines, e.row, e.row+1)
		e.row--
		return true
	}
	line := e.lines[e.row]
	n := 1
	if blank(line[:e.col]) {
		n = (e.col-1)%indentUnit + 1
	}
	e.lines[e.row] = slices.Delete(slices.Clone(line), e.col-n, e.col)
	e.col -= n
	return true
}

func (e *editor) del() bool {
	line := e.lines[e.row]
	switch {
	case e.col < len(line):
		e.lines[e.row] = slices.Delete(slices.Clone(line), e.col, e.col+1)
	case e.row < len(e.lines)-1:
		e.lines[e.row] = append(slices.Clone(line), e.lines[e.row+1]...)
		e.lines = slices.Delete(e.lines, e.row+1, e.row+2)
	default:
		return false
	}
	return true
}

func (e *editor) dedent() bool {
	line := e.lines[e.row]
	n := min(indentUnit, len(leading(line)))
	if n == 0 {
		return false
	}
	e.lines[e.row] = slices.Clone(line[n:])
	e.col = max(0, e.col-n)
	return true
}

func (e *editor) deleteLine() bool {
	if len(e.lines) == 1 {
		if len(e.lines[0]) == 0 {
			return false
		}
		e.lines[0], e.col = nil, 0
		return true
	}
	e.lines = slices.Delete(e.lines, e.row, e.row+1)
	e.clamp()
	return true
}

func (e *editor) duplicateLine() bool {
	e.lines = slices.Insert(e.lines, e.row+1, slices.Clone(e.lines[e.row]))
	e.row++
	return true
}

// step undoes (or redoes) one edit, keeping the inverse on the other stack.
func (e *editor) step(from, to *[]edSnap, empty string) {
	if len(*from) == 0 {
		e.note, e.noteErr = empty, false
		return
	}
	s := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	*to = append(*to, edSnap{e.text(), e.row, e.col})
	e.setText(s.text)
	e.row, e.col = s.row, s.col
	e.clamp()
	e.kind = ""
	e.changed()
}

func (e *editor) format() {
	if e.lang != "json" {
		e.note, e.noteErr = "only JSON can be formatted", true
		return
	}
	out, err := formatJSON(e.text())
	switch {
	case err != nil:
		e.note, e.noteErr = err.Error(), true
	case out == e.text():
		e.note, e.noteErr = "already formatted", false
	default:
		e.apply("", func() bool { e.setText(out); return true })
		e.note, e.noteErr = "formatted", false
	}
}

func (e *editor) vmove(d int) {
	if e.goal < 0 {
		e.goal = e.col
	}
	e.row = max(0, min(e.row+d, len(e.lines)-1))
	e.col = min(e.goal, len(e.lines[e.row]))
}

func (e *editor) charLeft() {
	e.goal = -1
	switch {
	case e.col > 0:
		e.col--
	case e.row > 0:
		e.row--
		e.col = len(e.lines[e.row])
	}
}

func (e *editor) charRight() {
	e.goal = -1
	switch {
	case e.col < len(e.lines[e.row]):
		e.col++
	case e.row < len(e.lines)-1:
		e.row++
		e.col = 0
	}
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func (e *editor) wordLeft() {
	e.goal = -1
	if e.col == 0 {
		e.charLeft()
		return
	}
	line := e.lines[e.row]
	for e.col > 0 && !isWord(line[e.col-1]) {
		e.col--
	}
	for e.col > 0 && isWord(line[e.col-1]) {
		e.col--
	}
}

func (e *editor) wordRight() {
	e.goal = -1
	line := e.lines[e.row]
	if e.col == len(line) {
		e.charRight()
		return
	}
	for e.col < len(line) && !isWord(line[e.col]) {
		e.col++
	}
	for e.col < len(line) && isWord(line[e.col]) {
		e.col++
	}
}

// home jumps to the first non-blank column, or to column 0 when already there.
func (e *editor) home() {
	e.goal = -1
	if n := len(leading(e.lines[e.row])); e.col != n {
		e.col = n
	} else {
		e.col = 0
	}
}

// click moves the cursor to a cell of the last view; x counts from the pane's left edge.
func (e *editor) click(x, y int) {
	if y < 0 || y >= e.height {
		return
	}
	e.move(func() {
		e.row = e.top + y
		e.clamp()
		line, target, w := e.lines[e.row], e.shift+x-e.gutter, 0
		e.col = 0
		for e.col < len(line) {
			cw := cells(line[e.col : e.col+1])
			if w+cw > target {
				break
			}
			w += cw
			e.col++
		}
		e.goal = -1
	})
}

// cells is the width of rs as the editor draws it: tabs take one cell, wide characters two.
func cells(rs []rune) int { return ansi.StringWidth(strings.ReplaceAll(string(rs), "\t", " ")) }

// scroll moves the view without moving the cursor.
func (e *editor) scroll(d int) {
	e.top = max(0, min(e.top+d, len(e.lines)-1))
}

func (e *editor) jsonProblem() error {
	if !e.checked {
		e.problem, e.checked = nil, true
		if e.lang == "json" {
			e.problem = jsonProblem(e.text())
		}
	}
	return e.problem
}

// view draws the editor in w×h cells: line numbers, the text with the cursor, and a status line.
func (e *editor) view(w, h int) []string {
	textH := max(1, h-1)
	e.height = textH
	digits := max(2, len(strconv.Itoa(len(e.lines))))
	e.gutter = digits + 1
	tw := max(1, w-e.gutter)
	if e.follow {
		if e.row < e.top {
			e.top = e.row
		} else if e.row >= e.top+textH {
			e.top = e.row - textH + 1
		}
		if cx := cells(e.lines[e.row][:e.col]); cx < e.shift {
			e.shift = cx
		} else if cx >= e.shift+tw {
			e.shift = cx - tw + 1
		}
		e.follow = false
	}
	e.top = max(0, min(e.top, len(e.lines)-1))
	out := make([]string, 0, h)
	for i := range textH {
		n := e.top + i
		if n >= len(e.lines) {
			out = append(out, stMuted.Render(fmt.Sprintf("%*s", digits, "~")))
			continue
		}
		num := stMuted
		if n == e.row {
			num = stTitle
		}
		line := e.lines[n]
		styled := highlight(e.lang, strings.NewReplacer("\t", " ", "\r", " ").Replace(string(line)))
		vis := ansi.Cut(styled, e.shift, e.shift+tw)
		// Columns are runes, the screen is cells: wide characters take two.
		if cx := cells(line[:min(e.col, len(line))]); n == e.row && cx >= e.shift && cx < e.shift+tw {
			ch, cw := " ", 1
			if e.col < len(line) && line[e.col] != '\t' && line[e.col] != '\r' {
				ch = string(line[e.col])
				cw = max(1, ansi.StringWidth(ch))
			}
			vis = ansi.Cut(styled, e.shift, cx) + stCaret.Render(ch) + ansi.Cut(styled, cx+cw, e.shift+tw)
		}
		out = append(out, num.Render(fmt.Sprintf("%*d", digits, n+1))+" "+vis)
	}
	return append(out, e.status(w))
}

func (e *editor) status(w int) string {
	left := stBold.Render(e.what) + stMuted.Render(fmt.Sprintf("  ln %d, col %d", e.row+1, e.col+1))
	right := ""
	switch {
	case e.note != "" && e.noteErr:
		right = stErr.Render(e.note)
	case e.note != "":
		right = stOK.Render(e.note)
	case e.lang == "json" && len(e.text()) > maxJSONCheck:
		right = stMuted.Render("JSON not checked: over 1 MiB")
	case e.jsonProblem() != nil:
		right = stErr.Render("✗ " + e.problem.Error())
	case e.lang == "json" && strings.TrimSpace(e.text()) != "":
		right = stOK.Render("✓ valid JSON")
	}
	return fit(left+"   "+right, w)
}

func blank(rs []rune) bool {
	for _, r := range rs {
		if r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func leading(rs []rune) []rune {
	n := 0
	for n < len(rs) && (rs[n] == ' ' || rs[n] == '\t') {
		n++
	}
	return rs[:n]
}

func trimLeft(rs []rune) []rune { return slices.Clone(rs[len(leading(rs)):]) }

func lastNonSpace(rs []rune) rune {
	for _, r := range slices.Backward(rs) {
		if r != ' ' && r != '\t' {
			return r
		}
	}
	return 0
}

func pairs(open, close rune) bool {
	return open == '{' && close == '}' || open == '[' && close == ']' || open == '(' && close == ')'
}

// varPattern matches a {{variable}}; it stops at quotes and newlines, so it never spans two JSON strings.
var varPattern = regexp.MustCompile(`\{\{[^{}"\n]*\}\}`)

// maxJSONCheck is the largest body checked as JSON on every key press.
const maxJSONCheck = 1 << 20

// withPlaceholders swaps every {{variable}} for a number that is valid JSON both as a value and
// inside a string, so bodies with variables can be checked and formatted; restore swaps them back.
func withPlaceholders(s string) (string, func(string) string) {
	if !strings.Contains(s, "{{") {
		return s, func(x string) string { return x }
	}
	seen := map[string]string{}
	next := 0
	out := varPattern.ReplaceAllStringFunc(s, func(v string) string {
		if p, ok := seen[v]; ok {
			return p
		}
		for {
			next++
			// Fixed width, so no placeholder is a prefix of another.
			p := fmt.Sprintf("97%010d", next)
			if !strings.Contains(s, p) {
				seen[v] = p
				return p
			}
		}
	})
	return out, func(x string) string {
		for v, p := range seen {
			x = strings.ReplaceAll(x, p, v)
		}
		return x
	}
}

// formatJSON indents s with two spaces, keeping {{variables}}.
func formatJSON(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return s, nil
	}
	src, restore := withPlaceholders(s)
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(src), "", "  "); err != nil {
		return "", jsonError(src, err)
	}
	return restore(buf.String()), nil
}

// jsonProblem reports why s is not valid JSON, treating {{variables}} as values; nil when it is
// valid, empty, or too big to check on every key press.
func jsonProblem(s string) error {
	if strings.TrimSpace(s) == "" || len(s) > maxJSONCheck {
		return nil
	}
	if s == lastChecked.text {
		return lastChecked.err
	}
	src, _ := withPlaceholders(s)
	var err error
	if !json.Valid([]byte(src)) {
		var v any
		err = jsonError(src, json.Unmarshal([]byte(src), &v))
	}
	lastChecked.text, lastChecked.err = s, err
	return err
}

// lastChecked remembers the last jsonProblem answer: the body tab asks for the same text several
// times per frame.
var lastChecked struct {
	text string
	err  error
}

func jsonError(src string, err error) error {
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		line := strings.Count(src[:min(int(se.Offset), len(src))], "\n") + 1
		return fmt.Errorf("invalid JSON on line %d: %s", line, strings.TrimPrefix(se.Error(), "invalid character "))
	}
	return fmt.Errorf("invalid JSON: %w", err)
}
