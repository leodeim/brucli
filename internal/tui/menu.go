package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const brand = "brucli "

type menuTitle struct {
	name string
	x, w int
}

// menuBar is where each menu title sits on the header line.
func menuBar() []menuTitle {
	out := make([]menuTitle, len(menus))
	x := len(brand)
	for i, name := range menus {
		out[i] = menuTitle{name: name, x: x, w: len(name) + 2}
		x += len(name) + 2
	}
	return out
}

// menuModal is one open drop-down of the menu bar.
type menuModal struct {
	menu   int
	cursor int
}

func (m *Model) openMenu(i int) tea.Cmd {
	mm := &menuModal{menu: i}
	mm.cursor = mm.next(m, -1, 1)
	m.modal = mm
	return nil
}

func (mm *menuModal) items() []*action { return menuActions(menus[mm.menu]) }

// next is the first enabled item after from in direction d, or from when there is none.
func (mm *menuModal) next(m *Model, from, d int) int {
	items := mm.items()
	for i := from + d; i >= 0 && i < len(items); i += d {
		if items[i].enabled(m) {
			return i
		}
	}
	return max(from, 0)
}

func (mm *menuModal) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "f10":
		m.modal = nil
	case "left":
		return m.openMenu((mm.menu + len(menus) - 1) % len(menus))
	case "right":
		return m.openMenu((mm.menu + 1) % len(menus))
	case "up":
		mm.cursor = mm.next(m, mm.cursor, -1)
	case "down":
		mm.cursor = mm.next(m, mm.cursor, 1)
	case "enter", " ":
		return mm.run(m, mm.cursor)
	default:
		// Any other key closes the menu and does what it does anywhere else, so shortcuts shown in
		// the menu work while it is open. With the editor open the key would be typed, so it is dropped.
		for _, a := range mm.items() {
			if hasKey(a, msg.String()) && a.enabled(m) && (a.scope == anywhere || a.scope == m.scope()) {
				m.modal = nil
				return m.runAction(a)
			}
		}
		m.modal = nil
		if m.req.editor != nil {
			return nil
		}
		return m.key(msg)
	}
	return nil
}

func (mm *menuModal) run(m *Model, i int) tea.Cmd {
	items := mm.items()
	if i < 0 || i >= len(items) || !items[i].enabled(m) {
		return nil
	}
	m.modal = nil
	return m.runAction(items[i])
}

func (mm *menuModal) view(m *Model, _, _ int) string {
	items := mm.items()
	labelW, keyW := 0, 0
	for _, a := range items {
		labelW = max(labelW, ansi.StringWidth(a.title(m)))
		keyW = max(keyW, ansi.StringWidth(a.keyFor(m)))
	}
	var lines []string
	for i, a := range items {
		if a.sep && i > 0 {
			lines = append(lines, stMuted.Render(strings.Repeat("─", labelW+keyW+4)))
		}
		label := fmt.Sprintf("%-*s  %*s", labelW, a.title(m), keyW, a.keyFor(m))
		switch {
		case i == mm.cursor && a.enabled(m):
			label = stCursor.Render(" " + label + " ")
		case a.enabled(m):
			label = " " + fmt.Sprintf("%-*s  ", labelW, a.title(m)) + stKey.Render(fmt.Sprintf("%*s", keyW, a.keyFor(m))) + " "
		default:
			label = stMuted.Render(" " + label + " ")
		}
		lines = append(lines, label)
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Render(strings.Join(lines, "\n"))
}

// at puts the drop-down under its title.
func (mm *menuModal) at(_ *Model, w, _, bw, _ int) (int, int) {
	return max(0, min(menuBar()[mm.menu].x, w-bw)), 1
}

func (mm *menuModal) help() string {
	return "↑↓ move · ←→ other menus · enter run · esc close · shortcuts work as shown"
}

// click runs the item on the clicked line; separators shift the lines below them.
func (mm *menuModal) click(m *Model, _, y int) tea.Cmd {
	row := 1
	for i, a := range mm.items() {
		if a.sep && i > 0 {
			row++
		}
		if row == y {
			return mm.run(m, i)
		}
		row++
	}
	return nil
}

func (*menuModal) dismissible() {}

// paletteModal lists every action with its keys; typing filters by name, menu or key.
type paletteModal struct {
	filter string
	cursor int
	// start and shown are the first row and the row count drawn by the last view.
	start, shown int
}

type paletteRow struct {
	a      *action
	header string
}

func (m *Model) openPalette() tea.Cmd {
	p := &paletteModal{}
	rows := p.rows(m)
	p.cursor = p.next(m, rows, -1, 1)
	m.modal = p
	return nil
}

func (p *paletteModal) rows(m *Model) []paletteRow {
	terms := strings.Fields(strings.ToLower(p.filter))
	var rows []paletteRow
	if len(terms) == 0 {
		for _, menu := range menus {
			rows = append(rows, paletteRow{header: menu})
			for _, a := range menuActions(menu) {
				rows = append(rows, paletteRow{a: a})
			}
		}
		return rows
	}
	var off []paletteRow
	for _, a := range actions {
		hay := strings.ToLower(a.menu + " " + a.title(m) + " " + strings.Join(a.keys, " "))
		match := true
		for _, t := range terms {
			match = match && strings.Contains(hay, t)
		}
		switch {
		case !match:
		case a.enabled(m):
			rows = append(rows, paletteRow{a: a})
		default:
			off = append(off, paletteRow{a: a})
		}
	}
	return append(rows, off...)
}

// next is the first enabled action row after from in direction d, or from when there is none.
func (p *paletteModal) next(m *Model, rows []paletteRow, from, d int) int {
	for i := from + d; i >= 0 && i < len(rows); i += d {
		if rows[i].a != nil && rows[i].a.enabled(m) {
			return i
		}
	}
	return max(from, 0)
}

func (p *paletteModal) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	rows := p.rows(m)
	switch msg.Type {
	case tea.KeyEsc:
		if p.filter != "" {
			p.setFilter(m, "")
			return nil
		}
		m.modal = nil
	case tea.KeyEnter:
		return p.run(m, rows, p.cursor)
	case tea.KeyUp, tea.KeyCtrlP:
		p.cursor = p.next(m, rows, p.cursor, -1)
	case tea.KeyDown, tea.KeyCtrlN:
		p.cursor = p.next(m, rows, p.cursor, 1)
	case tea.KeyPgUp:
		for range 10 {
			p.cursor = p.next(m, rows, p.cursor, -1)
		}
	case tea.KeyPgDown:
		for range 10 {
			p.cursor = p.next(m, rows, p.cursor, 1)
		}
	case tea.KeyF10:
		return m.openMenu(0)
	case tea.KeyBackspace:
		if r := []rune(p.filter); len(r) > 0 {
			p.setFilter(m, string(r[:len(r)-1]))
		}
	case tea.KeyRunes, tea.KeySpace:
		if p.filter == "" && msg.String() == "?" {
			m.modal = nil
			return nil
		}
		p.setFilter(m, p.filter+string(msg.Runes))
	}
	return nil
}

func (p *paletteModal) setFilter(m *Model, f string) {
	p.filter = f
	p.cursor = p.next(m, p.rows(m), -1, 1)
}

func (p *paletteModal) run(m *Model, rows []paletteRow, i int) tea.Cmd {
	if i < 0 || i >= len(rows) || rows[i].a == nil || !rows[i].a.enabled(m) {
		return nil
	}
	m.modal = nil
	return m.runAction(rows[i].a)
}

func (p *paletteModal) view(m *Model, w, h int) string {
	width := min(modalWidth(w), 84)
	rows := p.rows(m)
	keyW := 0
	for _, r := range rows {
		if r.a != nil {
			keyW = max(keyW, ansi.StringWidth(keysText(m, r.a)))
		}
	}
	labelW := width - keyW - 8
	var lines []string
	for i, r := range rows {
		if r.a == nil {
			lines = append(lines, stTitle.Render(r.header))
			continue
		}
		label := r.a.title(m)
		if p.filter != "" {
			label += stMuted.Render("  " + r.a.menu)
		}
		text := fit(label, labelW) + " " + fmt.Sprintf("%*s", keyW, keysText(m, r.a))
		switch {
		case i == p.cursor && r.a.enabled(m):
			lines = append(lines, stCursor.Render("› "+stripStyles(text)))
		case r.a.enabled(m):
			lines = append(lines, "  "+fit(label, labelW)+" "+stKey.Render(fmt.Sprintf("%*s", keyW, keysText(m, r.a))))
		default:
			lines = append(lines, stMuted.Render("  "+stripStyles(text)))
		}
	}
	if len(rows) == 0 {
		lines = append(lines, stMuted.Render("no action matches"))
	}
	lines, p.start = listView(lines, p.cursor, h-12)
	p.shown = len(lines)
	search := stMuted.Render("type to search")
	if p.filter != "" {
		search = p.filter + stCaret.Render(" ")
	}
	head := []string{stTitle.Render("› ") + search, ""}
	foot := []string{"", stMuted.Render("Lists: ↑↓ move · Tabs: ←→ or 1-8 · Panes: tab · Mouse: click and scroll")}
	return box("Actions", append(append(head, lines...), foot...), width)
}

// keysText lists an action's first two keys, and the pane they need when it is not the focused one.
func keysText(m *Model, a *action) string {
	var names []string
	for _, k := range a.keys[:min(2, len(a.keys))] {
		names = append(names, keyLabel(k))
	}
	text := strings.Join(names, " ")
	if text != "" && a.scope != anywhere && a.scope != m.scope() {
		text = a.keyFor(m)
	}
	return text
}

func (p *paletteModal) help() string {
	return "type to search · ↑↓ move · enter run · esc close"
}

// at keeps the palette near the top, so it does not jump while the list shrinks.
func (p *paletteModal) at(_ *Model, w, _, bw, _ int) (int, int) { return max(0, (w-bw)/2), 2 }

// click runs the action on the clicked line; the list starts below the search line.
func (p *paletteModal) click(m *Model, _, y int) tea.Cmd {
	row := y - boxTop - 2
	if row < 0 || row >= p.shown {
		return nil
	}
	i := p.start + row
	rows := p.rows(m)
	if i < 0 || i >= len(rows) {
		return nil
	}
	return p.run(m, rows, i)
}

func (p *paletteModal) wheel(m *Model, d int) {
	rows := p.rows(m)
	for range max(d, -d) {
		p.cursor = p.next(m, rows, p.cursor, map[bool]int{true: 1, false: -1}[d > 0])
	}
}

func (*paletteModal) dismissible() {}
