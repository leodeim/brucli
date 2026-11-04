package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// scope is where an action's keys work; menus and the palette run actions from anywhere.
type scope int

const (
	anywhere scope = iota
	inTree
	inRequest
	inResponse
)

func (m *Model) scope() scope {
	switch m.focus {
	case focusTree:
		return inTree
	case focusRequest:
		return inRequest
	}
	return inResponse
}

// action is one command. Key handling, the menu bar, the action palette and the footer hints are
// all built from the actions list, so a key is declared once.
type action struct {
	menu string
	name string
	// label replaces name when the text depends on the state.
	label func(m *Model) string
	keys  []string
	scope scope
	// hint is the footer text shown next to the first key while the action is available.
	hint string
	// sep draws a separator above the action in its menu.
	sep  bool
	when func(m *Model) bool
	do   func(m *Model) tea.Cmd
}

func (a *action) title(m *Model) string {
	if a.label != nil {
		return a.label(m)
	}
	return a.name
}

func (a *action) enabled(m *Model) bool { return a.when == nil || a.when(m) }

func (a *action) keyName() string {
	if len(a.keys) == 0 {
		return ""
	}
	return keyLabel(a.keys[0])
}

// keyFor is the key shown for a in menus: the first key, plus the pane it needs when that is not
// the focused one (a sidebar key does something else in the request pane).
func (a *action) keyFor(m *Model) string {
	k := a.keyName()
	if k == "" || a.scope == anywhere || a.scope == m.scope() {
		return k
	}
	return k + " in " + map[scope]string{inTree: "sidebar", inRequest: "request", inResponse: "response"}[a.scope]
}

func keyLabel(k string) string {
	switch k {
	case " ":
		return "space"
	case "f10":
		return "F10"
	}
	return k
}

var menus = []string{"File", "Request", "Environment", "Run", "View", "Help"}

// actions is filled in init because the actions refer back to it (the palette lists them).
var actions []*action

func init() { actions = buildActions() }

func menuActions(menu string) []*action {
	var out []*action
	for _, a := range actions {
		if a.menu == menu {
			out = append(out, a)
		}
	}
	return out
}

// dispatch runs the action bound to k, preferring the focused pane's actions over global ones.
func (m *Model) dispatch(k string) (tea.Cmd, bool) {
	for _, sc := range []scope{m.scope(), anywhere} {
		for _, a := range actions {
			if a.scope != sc || !a.enabled(m) || !hasKey(a, k) {
				continue
			}
			return a.do(m), true
		}
	}
	return nil, false
}

func hasKey(a *action, k string) bool {
	for _, x := range a.keys {
		if x == k {
			return true
		}
	}
	return false
}

// runAction runs a from a menu or the palette.
func (m *Model) runAction(a *action) tea.Cmd {
	if !a.enabled(m) {
		return nil
	}
	return a.do(m)
}

// hints lists the focused pane's available actions, then the global ones, for the footer.
func (m *Model) hints() []string {
	var out []string
	for _, sc := range []scope{m.scope(), anywhere} {
		for _, a := range actions {
			if a.scope == sc && a.hint != "" && a.enabled(m) {
				out = append(out, keyLabel(a.keys[0])+" "+a.hint)
			}
		}
	}
	return out
}

func hasRequest(m *Model) bool { return m.current != nil }

func buildActions() []*action {
	rowHas := func(f func(*row) bool) func(*Model) bool {
		return func(m *Model) bool {
			r := m.req.currentRow(m)
			return r != nil && f(r)
		}
	}
	onTab := func(t reqTab) func(*Model) bool {
		return func(m *Model) bool { return m.current != nil && m.req.tab == t }
	}
	return []*action{
		{menu: "File", name: "New request…", keys: []string{"a"}, scope: inTree, hint: "new", do: (*Model).newRequestForm},
		{menu: "File", name: "Duplicate request…", keys: []string{"C"}, scope: inTree,
			when: func(m *Model) bool { return m.fileTarget() != nil }, do: (*Model).duplicateRequest},
		{menu: "File", name: "Rename request…", keys: []string{"r"}, scope: inTree,
			when: func(m *Model) bool { return m.fileTarget() != nil }, do: (*Model).renameRequest},
		{menu: "File", name: "Delete request…", keys: []string{"d", "delete"}, scope: inTree, hint: "delete",
			when: func(m *Model) bool { return m.fileTarget() != nil }, do: (*Model).deleteRequest},
		{menu: "File", name: "Save request", keys: []string{"ctrl+s"}, sep: true, hint: "save",
			when: func(m *Model) bool { return m.current != nil && m.dirty[m.current] },
			do:   func(m *Model) tea.Cmd { return m.save(m.current) }},
		{menu: "File", name: "Save all", keys: []string{"S"},
			when: func(m *Model) bool { return len(m.dirty) > 0 || len(m.envDirty) > 0 }, do: (*Model).saveAll},
		{menu: "File", name: "Revert request to saved",
			when: func(m *Model) bool { return m.current != nil && m.dirty[m.current] && m.pristine[m.current] != nil },
			do:   (*Model).revertRequest},
		{menu: "File", name: "Quit", keys: []string{"q"}, sep: true, do: (*Model).quit},

		{menu: "Request", name: "Send", keys: []string{"s", "ctrl+r"}, hint: "send",
			when: func(m *Model) bool { return m.sendTarget() != nil }, do: (*Model).sendSelected},
		{menu: "Request", name: "Cancel sending", keys: []string{"esc"}, hint: "cancel",
			when: func(m *Model) bool { return m.run != nil && m.run.active }, do: (*Model).cancelSend},
		{menu: "Request", name: "Edit URL", keys: []string{"u"}, sep: true, when: hasRequest, do: (*Model).editURL},
		{menu: "Request", name: "Method…", keys: []string{"m"}, when: hasRequest, do: (*Model).pickMethod},
		{menu: "Request", name: "Edit body", keys: []string{"b"}, when: hasRequest, do: (*Model).editBody},
		{menu: "Request", name: "Body type…", keys: []string{"t"}, scope: inRequest, hint: "type", when: onTab(tabBody), do: (*Model).pickBodyType},
		{menu: "Request", name: "Auth mode…", keys: []string{"t"}, scope: inRequest, hint: "mode", when: onTab(tabAuth), do: (*Model).pickAuthMode},
		{menu: "Request", keys: []string{"a"}, scope: inRequest, hint: "add", sep: true,
			label: func(m *Model) string {
				if l := m.req.spec.addLabel; l != "" {
					return l + "…"
				}
				return "Add…"
			},
			when: func(m *Model) bool { return m.current != nil && m.req.build(m).add != nil },
			do:   func(m *Model) tea.Cmd { return m.req.build(m).add() }},
		{menu: "Request", name: "Add post-response variable…", keys: []string{"A"}, scope: inRequest,
			when: onTab(tabVars), do: (*Model).addPostResponseVar},
		{menu: "Request", keys: []string{"enter"}, scope: inRequest, hint: "edit",
			label: func(m *Model) string { return "Edit " + m.req.what(m) },
			when:  func(m *Model) bool { return m.req.editFunc(m) != nil },
			do:    func(m *Model) tea.Cmd { return m.req.editFunc(m)() }},
		{menu: "Request", keys: []string{"n"}, scope: inRequest,
			label: func(m *Model) string { return "Rename " + m.req.rowWhat(m) + "…" },
			when:  rowHas(func(r *row) bool { return r.rename != nil }),
			do:    func(m *Model) tea.Cmd { return m.req.currentRow(m).rename() }},
		{menu: "Request", keys: []string{" "}, scope: inRequest, hint: "on/off",
			label: func(m *Model) string { return "Enable/disable " + m.req.rowWhat(m) },
			when:  rowHas(func(r *row) bool { return r.toggle != nil }),
			do:    func(m *Model) tea.Cmd { m.req.currentRow(m).toggle(); return nil }},
		{menu: "Request", keys: []string{"d", "delete", "backspace"}, scope: inRequest, hint: "delete",
			label: func(m *Model) string { return "Delete " + m.req.rowWhat(m) },
			when:  rowHas(func(r *row) bool { return r.del != nil }),
			do:    func(m *Model) tea.Cmd { m.req.currentRow(m).del(); return nil }},
		{menu: "Request", name: "Edit in $EDITOR", keys: []string{"ctrl+e"}, scope: inRequest,
			when: func(m *Model) bool { return m.current != nil && m.req.build(m).external != nil },
			do:   func(m *Model) tea.Cmd { return m.req.build(m).external() }},
		{menu: "Request", name: "Copy as curl", keys: []string{"c"}, sep: true,
			when: func(m *Model) bool { r := m.results[m.current]; return r != nil && r.Sent != nil }, do: (*Model).copyCurl},
		{menu: "Request", name: "Copy response body", keys: []string{"y"},
			when: func(m *Model) bool { r := m.results[m.current]; return r != nil && r.Response != nil }, do: (*Model).copyBody},

		{menu: "Environment", name: "Choose environment…", keys: []string{"e"}, do: (*Model).pickEnv},
		{menu: "Environment", name: "Choose global environment…", do: (*Model).pickGlobalEnv},
		{menu: "Environment", name: "Edit environment", keys: []string{"E"},
			when: func(m *Model) bool { return m.s.Env != nil }, do: (*Model).openEnvEditor},
		{menu: "Environment", name: "Variables in scope", keys: []string{"v"}, do: (*Model).openVars},
		{menu: "Environment", keys: []string{"ctrl+t"}, sep: true,
			label: func(m *Model) string { return map[bool]string{true: "Hide secrets", false: "Show secrets"}[m.reveal] },
			do:    (*Model).toggleSecrets},

		{menu: "Run", keys: []string{"R"},
			label: func(m *Model) string { return "Run " + m.folderName(m.tree.target()) + "…" },
			do:    func(m *Model) tea.Cmd { return m.openRunSetup(m.tree.target()) }},
		{menu: "Run", name: "Run whole collection…", do: func(m *Model) tea.Cmd { return m.openRunSetup(m.c.Root) }},
		{menu: "Run", name: "Show last run", keys: []string{"L"},
			when: func(m *Model) bool { return m.lastRun != nil }, do: (*Model).showRun},

		{menu: "View", name: "Next pane", keys: []string{"tab"}, do: func(m *Model) tea.Cmd { return m.cyclePane(1) }},
		{menu: "View", name: "Previous pane", keys: []string{"shift+tab"}, do: func(m *Model) tea.Cmd { return m.cyclePane(-1) }},
		{menu: "View", keys: []string{"z"},
			label: func(m *Model) string { return map[bool]string{true: "Restore panes", false: "Zoom this pane"}[m.zoom] },
			do:    (*Model).toggleZoom},
		{menu: "View", name: "Next tab", keys: []string{"]"}, sep: true, do: func(m *Model) tea.Cmd { return m.cycleTab(1) }},
		{menu: "View", name: "Previous tab", keys: []string{"["}, do: func(m *Model) tea.Cmd { return m.cycleTab(-1) }},
		{menu: "View", name: "Filter requests…", keys: []string{"/"}, scope: inTree, hint: "filter", sep: true, do: (*Model).filterTree},
		{menu: "View", name: "Search response…", keys: []string{"/"}, scope: inResponse, hint: "search",
			when: func(m *Model) bool { return m.results[m.current] != nil }, do: (*Model).searchResponse},
		{menu: "View", name: "Next match", keys: []string{"n"}, scope: inResponse, hint: "next",
			when: func(m *Model) bool { return m.resp.query != "" }, do: func(m *Model) tea.Cmd { m.resp.jump(1); return nil }},
		{menu: "View", name: "Previous match", keys: []string{"N"}, scope: inResponse,
			when: func(m *Model) bool { return m.resp.query != "" }, do: func(m *Model) tea.Cmd { m.resp.jump(-1); return nil }},
		{menu: "View", name: "Clear search", keys: []string{"esc"}, scope: inResponse,
			when: func(m *Model) bool { return m.resp.query != "" && (m.run == nil || !m.run.active) },
			do:   func(m *Model) tea.Cmd { m.resp.setQuery(""); return nil }},
		{menu: "View", sep: true,
			label: func(m *Model) string {
				return map[bool]string{true: "Turn mouse off (lets the terminal select text)", false: "Turn mouse on"}[m.mouseOn]
			},
			do: (*Model).toggleMouse},

		{menu: "Help", name: "All actions and keys…", keys: []string{"?", "ctrl+p", ":"}, do: (*Model).openPalette},
		{menu: "Help", name: "Menu bar", keys: []string{"f10"}, do: func(m *Model) tea.Cmd { return m.openMenu(0) }},
	}
}
