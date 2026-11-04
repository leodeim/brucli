package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// field is one line of a form: free text, or one of a few choices.
type field struct {
	label   string
	input   textinput.Model
	choices []string
	choice  int
}

func textField(label, value string) *field {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "empty"
	in.PlaceholderStyle = stMuted
	in.Cursor.SetMode(cursor.CursorStatic)
	in.SetValue(value)
	return &field{label: label, input: in}
}

// hint replaces the placeholder shown while the field is empty.
func (f *field) hint(s string) *field {
	f.input.Placeholder = s
	return f
}

// secretField hides what is typed unless masked is false.
func secretField(label, value string, masked bool) *field {
	f := textField(label, value)
	if masked {
		f.input.EchoMode = textinput.EchoPassword
		f.input.EchoCharacter = '•'
	}
	return f
}

func choiceField(label string, choices []string, value string) *field {
	f := &field{label: label, choices: choices}
	if i := slices.Index(choices, value); i >= 0 {
		f.choice = i
	}
	return f
}

func (f *field) value() string {
	if f.choices != nil {
		return f.choices[f.choice]
	}
	return f.input.Value()
}

// formModal edits a few fields at once: tab moves between them and enter applies all of them.
type formModal struct {
	title  string
	note   string
	fields []*field
	focus  int
	width  int
	submit func(vals []string) tea.Cmd
	back   modal
}

// openForm shows a form over the current modal, which comes back when the form closes.
func (m *Model) openForm(title, note string, fields []*field, focus int, submit func([]string) tea.Cmd) tea.Cmd {
	f := &formModal{title: title, note: note, fields: fields, submit: submit, back: m.modal, width: min(modalWidth(m.width), 96)}
	labelW := 0
	for _, fl := range fields {
		labelW = max(labelW, len(fl.label))
	}
	for _, fl := range fields {
		if fl.choices == nil {
			fl.input.Width = max(10, f.width-labelW-10)
			fl.input.CursorEnd()
		}
	}
	f.setFocus(focus)
	m.modal = f
	return nil
}

func (f *formModal) setFocus(i int) {
	f.focus = (i%len(f.fields) + len(f.fields)) % len(f.fields)
	for j, fl := range f.fields {
		if fl.choices != nil {
			continue
		}
		if j == f.focus {
			fl.input.Focus()
		} else {
			fl.input.Blur()
		}
	}
}

func (f *formModal) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	cur := f.fields[f.focus]
	switch msg.String() {
	case "esc":
		m.modal = f.back
		return nil
	case "enter":
		vals := make([]string, len(f.fields))
		for i, fl := range f.fields {
			vals[i] = fl.value()
		}
		m.modal = f.back
		return f.submit(vals)
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return nil
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return nil
	}
	if cur.choices != nil {
		n := len(cur.choices)
		switch {
		case msg.Type == tea.KeyLeft:
			cur.choice = (cur.choice + n - 1) % n
		case msg.Type == tea.KeyRight || msg.Type == tea.KeySpace:
			cur.choice = (cur.choice + 1) % n
		case msg.Type == tea.KeyRunes:
			// A letter jumps to the next choice starting with it.
			typed := strings.ToLower(string(msg.Runes))
			for i := 1; i <= n; i++ {
				if c := (cur.choice + i) % n; strings.HasPrefix(strings.ToLower(cur.choices[c]), typed) {
					cur.choice = c
					break
				}
			}
		}
		return nil
	}
	var cmd tea.Cmd
	cur.input, cmd = cur.input.Update(msg)
	return cmd
}

func (f *formModal) view(_ *Model, _, _ int) string {
	labelW := 0
	for _, fl := range f.fields {
		labelW = max(labelW, len(fl.label))
	}
	var lines []string
	for i, fl := range f.fields {
		label, mark := stMuted.Render(fmt.Sprintf("%-*s", labelW, fl.label)), "  "
		if i == f.focus {
			label, mark = stTitle.Render(fmt.Sprintf("%-*s", labelW, fl.label)), stTitle.Render("› ")
		}
		val := ""
		switch {
		case fl.choices != nil && i == f.focus:
			val = stMuted.Render("‹ ") + stCursor.Render(fl.choices[fl.choice]) + stMuted.Render(" ›")
		case fl.choices != nil:
			val = fl.choices[fl.choice]
		default:
			val = fl.input.View()
		}
		lines = append(lines, mark+label+"  "+val)
	}
	if f.note != "" {
		lines = append(lines, "")
		for _, l := range strings.Split(ansi.Wordwrap(f.note, f.width-4, ""), "\n") {
			lines = append(lines, stMuted.Render(l))
		}
	}
	return box(f.title, lines, f.width)
}

func (f *formModal) help() string {
	if f.fields[f.focus].choices != nil {
		return "←→ or a letter chooses · enter apply · tab next field · esc cancel"
	}
	return "enter apply · tab next field · esc cancel"
}

// click focuses the field on the clicked line; fields start below the title and a blank line.
func (f *formModal) click(_ *Model, _, y int) tea.Cmd {
	if i := y - boxTop; i >= 0 && i < len(f.fields) {
		f.setFocus(i)
	}
	return nil
}

type pickItem struct{ label, detail string }

// pickerModal chooses one item from a list; typing filters it.
type pickerModal struct {
	title  string
	items  []pickItem
	cursor int
	filter string
	// start and drawn are the first item and the item count drawn by the last view.
	start, drawn int
	pick         func(i int) tea.Cmd
	back         modal
}

// choose opens a picker over the current modal with the cursor on current.
func (m *Model) choose(title string, items []pickItem, current int, pick func(int) tea.Cmd) tea.Cmd {
	m.modal = &pickerModal{title: title, items: items, cursor: max(0, min(current, len(items)-1)), pick: pick, back: m.modal}
	return nil
}

func (p *pickerModal) shown() []int {
	q := strings.ToLower(p.filter)
	var idx []int
	for i, it := range p.items {
		if q == "" || strings.Contains(strings.ToLower(it.label), q) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (p *pickerModal) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	shown := p.shown()
	switch msg.Type {
	case tea.KeyEsc:
		if p.filter != "" {
			p.filter, p.cursor = "", 0
			return nil
		}
		m.modal = p.back
	case tea.KeyEnter:
		return p.choose(m, p.cursor)
	case tea.KeyUp, tea.KeyCtrlP:
		p.cursor = max(0, p.cursor-1)
	case tea.KeyDown, tea.KeyCtrlN:
		p.cursor = max(0, min(len(shown)-1, p.cursor+1))
	case tea.KeyPgUp, tea.KeyHome:
		p.cursor = 0
	case tea.KeyPgDown, tea.KeyEnd:
		p.cursor = max(0, len(shown)-1)
	case tea.KeyBackspace:
		if r := []rune(p.filter); len(r) > 0 {
			p.filter, p.cursor = string(r[:len(r)-1]), 0
		}
	case tea.KeyRunes, tea.KeySpace:
		p.filter += string(msg.Runes)
		p.cursor = 0
	}
	return nil
}

func (p *pickerModal) choose(m *Model, at int) tea.Cmd {
	shown := p.shown()
	if at < 0 || at >= len(shown) {
		return nil
	}
	m.modal = p.back
	return p.pick(shown[at])
}

func (p *pickerModal) view(_ *Model, w, h int) string {
	var lines []string
	for n, i := range p.shown() {
		it := p.items[i]
		label := stBold.Render(it.label)
		if it.detail != "" {
			label += "  " + stMuted.Render(it.detail)
		}
		lines = append(lines, line(n == p.cursor, label))
	}
	if len(lines) == 0 {
		lines = append(lines, stMuted.Render("nothing matches"))
	}
	lines, p.start = listView(lines, p.cursor, h-10)
	p.drawn = len(lines)
	if p.filter != "" {
		lines = append([]string{stMuted.Render("filter ") + p.filter}, lines...)
	}
	return box(p.title, lines, min(modalWidth(w), 64))
}

func (p *pickerModal) help() string {
	return "↑↓ move · enter choose · type to filter · esc close"
}

func (p *pickerModal) click(m *Model, _, y int) tea.Cmd {
	first := boxTop
	if p.filter != "" {
		first++
	}
	if y < first {
		return nil
	}
	if y-first >= p.drawn {
		return nil
	}
	return p.choose(m, p.start+y-first)
}

func (p *pickerModal) wheel(_ *Model, d int) {
	p.cursor = max(0, min(p.cursor+d, len(p.shown())-1))
}

func (*pickerModal) dismissible() {}
