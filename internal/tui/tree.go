package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/leodeim/brucli/internal/collection"
)

// node is one visible sidebar line: a folder or a request.
type node struct {
	folder *collection.Folder
	req    *collection.Request
	depth  int
}

type tree struct {
	root   *collection.Folder
	closed map[*collection.Folder]bool
	filter string
	// typing is set while keys go to the filter.
	typing bool
	nodes  []node
	cursor int
	offset int
}

func newTree(root *collection.Folder) *tree {
	t := &tree{root: root, closed: map[*collection.Folder]bool{}}
	t.refresh()
	return t
}

// refresh rebuilds the visible lines; a filter shows matching requests with their folders, all open.
func (t *tree) refresh() {
	var keep node
	if n := t.selected(); n != nil {
		keep = *n
	}
	t.nodes = nil
	t.walk(t.root, 0)
	t.focus(keep.folder, keep.req)
	t.cursor = min(t.cursor, max(len(t.nodes)-1, 0))
}

func (t *tree) walk(f *collection.Folder, depth int) bool {
	found := false
	for _, sub := range f.Folders {
		at := len(t.nodes)
		t.nodes = append(t.nodes, node{folder: sub, depth: depth})
		open := t.filter != "" || !t.closed[sub]
		var shown bool
		if open {
			shown = t.walk(sub, depth+1)
		} else {
			shown = t.matchesBelow(sub)
		}
		if t.filter != "" && !shown {
			t.nodes = t.nodes[:at]
			continue
		}
		found = true
	}
	for _, r := range f.Requests {
		if t.matches(r) {
			t.nodes = append(t.nodes, node{req: r, depth: depth})
			found = true
		}
	}
	return found
}

func (t *tree) matches(r *collection.Request) bool {
	return t.filter == "" || strings.Contains(strings.ToLower(r.Name), strings.ToLower(t.filter))
}

func (t *tree) matchesBelow(f *collection.Folder) bool {
	for _, r := range f.AllRequests() {
		if t.matches(r) {
			return true
		}
	}
	return false
}

func (t *tree) selected() *node {
	if t.cursor < 0 || t.cursor >= len(t.nodes) {
		return nil
	}
	return &t.nodes[t.cursor]
}

func (t *tree) move(d int) {
	t.cursor = max(0, min(len(t.nodes)-1, t.cursor+d))
}

func (t *tree) focus(f *collection.Folder, r *collection.Request) bool {
	for i, n := range t.nodes {
		if (r != nil && n.req == r) || (r == nil && f != nil && n.folder == f) {
			t.cursor = i
			return true
		}
	}
	return false
}

// reveal opens the folders above r and moves the cursor to it.
func (t *tree) reveal(r *collection.Request) {
	for _, f := range r.Chain() {
		delete(t.closed, f)
	}
	t.refresh()
	t.focus(nil, r)
}

func (t *tree) setOpen(f *collection.Folder, open bool) {
	if open {
		delete(t.closed, f)
	} else {
		t.closed[f] = true
	}
	t.refresh()
	t.focus(f, nil)
}

// target is the folder a run from the cursor covers: the folder under it, or the collection.
func (t *tree) target() *collection.Folder {
	if n := t.selected(); n != nil && n.folder != nil {
		return n.folder
	}
	return t.root
}

func (t *tree) view(m *Model, w, h int) []string {
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	lines := make([]string, 0, h)
	for i := t.offset; i < len(t.nodes) && len(lines) < h; i++ {
		n := t.nodes[i]
		indent := strings.Repeat("  ", n.depth)
		var line string
		if n.folder != nil {
			arrow := "▾"
			if t.closed[n.folder] && t.filter == "" {
				arrow = "▸"
			}
			line = indent + stMuted.Render(arrow) + " " + stBold.Render(n.folder.Name)
		} else {
			line = indent + "  " + methodTag(n.req.Method) + " " + n.req.Name + m.requestMarks(n.req)
		}
		line = fit(line, w)
		switch {
		case i == t.cursor && m.focus == focusTree:
			line = stCursor.Render(fit(stripStyles(line), w))
		case n.req != nil && n.req == m.current:
			line = stTitle.Render(fit(stripStyles(line), w))
		}
		lines = append(lines, line)
	}
	if len(t.nodes) == 0 {
		lines = append(lines, stMuted.Render(fit("no requests match", w)))
	}
	return lines
}

func (t *tree) filtering() bool { return t.typing }

func (t *tree) startFilter() { t.typing = true }

func (t *tree) filterKey(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		t.typing, t.filter = false, ""
	case tea.KeyEnter:
		t.typing = false
		if n := t.selected(); n != nil && n.req != nil {
			m.open(n.req)
		}
		return nil
	case tea.KeyBackspace:
		if r := []rune(t.filter); len(r) > 0 {
			t.filter = string(r[:len(r)-1])
		}
	case tea.KeyUp:
		t.move(-1)
		return nil
	case tea.KeyDown:
		t.move(1)
		return nil
	case tea.KeyRunes, tea.KeySpace:
		t.filter += string(msg.Runes)
	default:
		return nil
	}
	t.refresh()
	for i, n := range t.nodes {
		if n.req != nil {
			t.cursor = i
			break
		}
	}
	return nil
}

// fileTarget is the request the File actions work on: the one under the sidebar cursor while the
// sidebar has focus, else the open one.
func (m *Model) fileTarget() *collection.Request {
	if m.focus == focusTree {
		if n := m.tree.selected(); n != nil {
			return n.req
		}
	}
	return m.current
}

func (m *Model) filterTree() tea.Cmd {
	m.focus = focusTree
	m.tree.startFilter()
	return nil
}

// newRequestForm asks for a new request in the folder under the sidebar cursor.
func (m *Model) newRequestForm() tea.Cmd {
	f := m.tree.target()
	if n := m.tree.selected(); n != nil && n.req != nil {
		f = n.req.Folder
	}
	fields := []*field{textField("Name", ""), choiceField("Method", methods, "GET"), textField("URL", "{{baseUrl}}/")}
	return m.openForm("New request in "+m.folderName(f), "The file is created right away, named after the request.", fields, 0, func(v []string) tea.Cmd {
		name := strings.TrimSpace(v[0])
		if name == "" {
			m.setStatus(errors.New("a name is required"), "no request created")
			return nil
		}
		r, err := f.NewRequest(name, strings.TrimSpace(v[2]))
		if err != nil {
			m.setStatus(err, "creating %s", name)
			return nil
		}
		r.Method = v[1]
		syncParams(r)
		if r.Method != "GET" || len(r.Params) > 0 {
			if err := r.Save(); err != nil {
				m.setStatus(err, "saving %s", name)
			}
		}
		m.open(r)
		m.setStatus(nil, "created %s", m.rel(r.Path))
		return nil
	})
}

func (m *Model) duplicateRequest() tea.Cmd {
	src := m.fileTarget()
	if src == nil {
		return nil
	}
	return m.openForm("Duplicate "+src.Name, "The copy goes at the end of "+m.folderName(src.Folder)+".", []*field{textField("Name", src.Name+" copy")}, 0, func(v []string) tea.Cmd {
		r, err := src.Duplicate(strings.TrimSpace(v[0]))
		if err != nil {
			m.setStatus(err, "duplicating %s", src.Name)
			return nil
		}
		m.open(r)
		m.setStatus(nil, "created %s", m.rel(r.Path))
		return nil
	})
}

func (m *Model) renameRequest() tea.Cmd {
	r := m.fileTarget()
	if r == nil {
		return nil
	}
	return m.openForm("Rename "+r.Name, "Renaming saves the request and renames its file.", []*field{textField("Name", r.Name)}, 0, func(v []string) tea.Cmd {
		if err := r.Rename(strings.TrimSpace(v[0])); err != nil {
			m.setStatus(err, "renaming %s", r.Name)
			return nil
		}
		delete(m.dirty, r)
		m.pristine[r] = r.Clone()
		m.tree.refresh()
		m.setStatus(nil, "renamed to %s", m.rel(r.Path))
		return nil
	})
}

func (m *Model) deleteRequest() tea.Cmd {
	r := m.fileTarget()
	if r == nil {
		return nil
	}
	m.modal = &confirmModal{
		title: "Delete " + r.Name + "?",
		lines: []string{"Removes " + m.rel(r.Path) + " from disk.", "", "y delete · n or esc keep"},
		yes: func() tea.Cmd {
			if err := r.Delete(); err != nil {
				m.setStatus(err, "deleting %s", r.Name)
				return nil
			}
			delete(m.dirty, r)
			delete(m.results, r)
			delete(m.pristine, r)
			if m.current == r {
				m.current = nil
			}
			m.tree.refresh()
			m.setStatus(nil, "deleted %s", r.Name)
			return nil
		},
	}
	return nil
}
