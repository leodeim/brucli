package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// mouse handles left clicks and the wheel; everything is mapped through the last frame's layout.
func (m *Model) mouse(ev tea.MouseEvent) tea.Cmd {
	if ev.Action != tea.MouseActionPress || m.width < minWidth || m.height < minHeight {
		return nil
	}
	wheel := 0
	switch ev.Button {
	case tea.MouseButtonWheelUp:
		wheel = -1
	case tea.MouseButtonWheelDown:
		wheel = 1
	case tea.MouseButtonLeft:
	default:
		return nil
	}
	x, y := ev.X, ev.Y
	if m.modal != nil {
		return m.mouseModal(x, y, wheel)
	}
	if p := m.prompt; p != nil {
		if wheel != 0 {
			return nil
		}
		// Clicking away applies the prompt, like leaving a field in a form.
		m.prompt = nil
		return tea.Batch(p.commit(p.input.Value()), m.mouse(ev))
	}
	if y == 0 {
		if wheel == 0 {
			return m.clickHeader(x)
		}
		return nil
	}
	if y == m.height-1 && wheel == 0 && m.footerRight && x >= m.width-ansi.StringWidth(footerRight) {
		return m.openPalette()
	}
	l := m.layout()
	switch {
	case l.tree.has(x, y):
		return m.mouseTree(l.tree, y, wheel)
	case l.req.has(x, y):
		return m.mouseRequest(l.req, x, y, wheel)
	case l.resp.has(x, y):
		return m.mouseResponse(l.resp, x, y, wheel)
	}
	return nil
}

func (m *Model) mouseModal(x, y, wheel int) tea.Cmd {
	r := m.modalRect
	if r.has(x, y) {
		if w, ok := m.modal.(wheeler); ok && wheel != 0 {
			w.wheel(m, wheel)
		}
		if c, ok := m.modal.(clicker); ok && wheel == 0 {
			return c.click(m, x-r.x, y-r.y)
		}
		return nil
	}
	if _, ok := m.modal.(dismisser); !ok || wheel != 0 {
		return nil
	}
	old := m.modal
	m.modal = nil
	if y != 0 {
		return nil
	}
	if mm, ok := old.(*menuModal); ok && menuAt(x) == mm.menu {
		return nil
	}
	return m.clickHeader(x)
}

func menuAt(x int) int {
	for i, t := range menuBar() {
		if x >= t.x && x < t.x+t.w {
			return i
		}
	}
	return -1
}

func (m *Model) clickHeader(x int) tea.Cmd {
	if i := menuAt(x); i >= 0 {
		return m.openMenu(i)
	}
	if x < len(brand) {
		return m.openPalette()
	}
	if m.envX > 0 && x >= m.envX {
		return m.pickEnv()
	}
	return nil
}

func (m *Model) mouseTree(r rect, y, wheel int) tea.Cmd {
	t := m.tree
	t.typing = false
	if wheel != 0 {
		t.move(wheel)
		return nil
	}
	m.focus = focusTree
	in := r.inner()
	i := t.offset + y - in.y
	if y < in.y || y >= in.y+in.h || i < 0 || i >= len(t.nodes) {
		return nil
	}
	t.cursor = i
	n := t.nodes[i]
	if n.req != nil {
		m.open(n.req)
		m.focus = focusTree
		return nil
	}
	t.setOpen(n.folder, t.closed[n.folder])
	return nil
}

func (m *Model) mouseRequest(r rect, x, y, wheel int) tea.Cmd {
	p := &m.req
	in := r.inner()
	cx, cy := x-in.x, y-in.y
	if e := p.editor; e != nil {
		switch {
		case wheel != 0:
			e.scroll(wheel)
			return nil
		case cy >= 2 && cy < in.h:
			e.click(cx, cy-2)
			return nil
		}
		p.editor = nil
	}
	if wheel != 0 {
		if sel := selectable(p.spec); len(sel) > 0 {
			p.cursor[p.tab] = max(0, min(len(sel)-1, p.cursor[p.tab]+wheel))
		} else {
			p.scroll = max(0, p.scroll+wheel)
		}
		return nil
	}
	m.focus = focusRequest
	if cy < 0 || cy >= in.h {
		return nil
	}
	return p.click(m, cx, cy)
}

func (m *Model) mouseResponse(r rect, x, y, wheel int) tea.Cmd {
	p := &m.resp
	if wheel != 0 {
		p.scroll = max(0, p.scroll+3*wheel)
		return nil
	}
	m.focus = focusResponse
	if in := r.inner(); y == in.y {
		if t := tabAt(p.names, x-in.x); t >= 0 {
			p.switchTab(respTab(t))
		}
	}
	return nil
}
