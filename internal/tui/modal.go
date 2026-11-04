package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/runner"
	"github.com/leodeim/brucli/internal/vars"
)

// listView windows lines around cur for a modal of height h; start is the first line shown.
func listView(lines []string, cur, h int) (shown []string, start int) {
	h = max(h, 3)
	if len(lines) <= h {
		return lines, 0
	}
	start = max(0, min(cur-h/2, len(lines)-h))
	return lines[start : start+h], start
}

func line(selected bool, s string) string {
	if selected {
		return stCursor.Render("› " + stripStyles(s))
	}
	return "  " + s
}

type confirmModal struct {
	title string
	lines []string
	yes   func() tea.Cmd
	// no runs on n; nil means n only closes.
	no func() tea.Cmd
}

func (c *confirmModal) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		m.modal = nil
		return c.yes()
	case "n", "N":
		m.modal = nil
		if c.no != nil {
			return c.no()
		}
	case "esc", "q":
		m.modal = nil
	}
	return nil
}

func (c *confirmModal) view(_ *Model, w, _ int) string { return box(c.title, c.lines, modalWidth(w)) }
func (c *confirmModal) help() string                   { return "y yes · n no · esc cancel" }

func (m *Model) pickEnv() tea.Cmd {
	items := []pickItem{{label: "No environment"}}
	for _, e := range m.c.Environments {
		detail := fmt.Sprintf("%d vars", len(e.Vars))
		if m.envDirty[e] {
			detail += " · unsaved"
		}
		items = append(items, pickItem{label: e.Name, detail: detail})
	}
	current := 0
	if m.s.Env != nil {
		current = slices.Index(m.c.Environments, m.s.Env) + 1
	}
	return m.choose("Environment", items, current, func(i int) tea.Cmd {
		if m.busy {
			m.setStatus(errors.New("wait for it or esc"), "a request is running")
			return nil
		}
		if i == 0 {
			m.s.Env = nil
			m.setStatus(nil, "no environment")
			return nil
		}
		m.s.Env = m.c.Environments[i-1]
		m.setStatus(nil, "environment %s", m.s.Env.Name)
		return nil
	})
}

func (m *Model) pickGlobalEnv() tea.Cmd {
	items := []pickItem{{label: "No global environment"}}
	for _, env := range m.c.GlobalEnvironments {
		items = append(items, pickItem{label: env.Name})
	}
	current := 0
	if m.s.GlobalEnv != nil {
		current = slices.IndexFunc(m.c.GlobalEnvironments, func(env *collection.Environment) bool { return env.Path == m.s.GlobalEnv.Path }) + 1
	}
	return m.choose("Global environment", items, current, func(i int) tea.Cmd {
		m.s.GlobalEnv = nil
		if i > 0 {
			m.s.GlobalEnv = m.c.GlobalEnvironments[i-1]
			m.setStatus(nil, "global environment %s", m.s.GlobalEnv.Name)
		} else {
			m.setStatus(nil, "no global environment")
		}
		return nil
	})
}

func (m *Model) openEnvEditor() tea.Cmd {
	if m.s.Env == nil {
		m.setStatus(errors.New("e picks one"), "no environment selected")
		return nil
	}
	m.modal = &envEditor{env: m.s.Env}
	return nil
}

type envEditor struct {
	env    *collection.Environment
	cursor int
	start  int
}

func (e *envEditor) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	vs := &e.env.Vars
	k := msg.String()
	if k == "esc" || k == "q" {
		m.modal = nil
		return nil
	}
	if m.busy && k != "up" && k != "down" && k != "j" && k != "k" {
		m.setStatus(errors.New("wait for it"), "a request is running")
		return nil
	}
	changed := func() { m.envDirty[e.env] = true }
	var cur *collection.EnvVar
	if e.cursor < len(*vs) {
		cur = &(*vs)[e.cursor]
	}
	switch k {
	case "up", "k":
		e.cursor = max(0, e.cursor-1)
	case "down", "j":
		e.cursor = max(0, min(len(*vs)-1, e.cursor+1))
	case " ":
		if cur != nil {
			cur.Enabled = !cur.Enabled
			changed()
		}
	case "enter", "n":
		if cur == nil {
			return nil
		}
		focus := map[string]int{"enter": 1, "n": 0}[k]
		if cur.Secret {
			fields := []*field{textField("Name", cur.Name), secretField("Value", m.s.Secrets[e.env.Name][cur.Name], !m.reveal)}
			return m.openForm("Secret "+cur.Name, "Secret values stay in this session and are never written to the file.", fields, focus, func(v []string) tea.Cmd {
				m.s.SetSecret(cur.Name, v[1])
				if n := strings.TrimSpace(v[0]); n != "" && n != cur.Name {
					cur.Name = n
					m.s.SetSecret(n, v[1])
					changed()
				}
				return nil
			})
		}
		fields := []*field{textField("Name", cur.Name), textField("Value", cur.Value)}
		return m.openForm("Variable "+cur.Name, "", fields, focus, func(v []string) tea.Cmd {
			if n := strings.TrimSpace(v[0]); n != "" {
				cur.Name = n
			}
			cur.Value = v[1]
			changed()
			return nil
		})
	case "t":
		if cur != nil {
			cur.Secret = !cur.Secret
			if cur.Secret {
				m.s.SetSecret(cur.Name, cur.Value)
				cur.Value = ""
			} else {
				cur.Value = m.s.Secrets[e.env.Name][cur.Name]
			}
			changed()
		}
	case "a":
		return m.kvForm("New variable in "+e.env.Name, "", "", 0, func(name, value string) {
			*vs = append(*vs, collection.EnvVar{KV: collection.KV{Name: name, Value: value, Enabled: true}})
			e.cursor = len(*vs) - 1
			changed()
		})
	case "d", "delete":
		if cur != nil {
			*vs = slices.Delete(*vs, e.cursor, e.cursor+1)
			e.cursor = max(0, min(e.cursor, len(*vs)-1))
			changed()
		}
	case "ctrl+s":
		if err := e.env.Save(); err != nil {
			m.setStatus(err, "saving %s", e.env.Name)
			return nil
		}
		delete(m.envDirty, e.env)
		m.setStatus(nil, "saved %s", m.rel(e.env.Path))
	}
	return nil
}

func (e *envEditor) view(m *Model, w, h int) string {
	var lines []string
	nameW := 8
	for _, v := range e.env.Vars {
		nameW = max(nameW, len(v.Name))
	}
	scope := m.s.Scope(m.current)
	for i, v := range e.env.Vars {
		value := markVars(v.Value)
		if v.Secret {
			value = stMuted.Render("secret ")
			if s, ok := m.s.Secrets[e.env.Name][v.Name]; ok && s != "" {
				value += strings.Repeat("•", min(len(s), 8))
			} else {
				value += stWarn.Render("not set")
			}
		} else if resolved := scope.Interpolate(v.Value); resolved != v.Value {
			if runner.LooksSecret(v.Name) && !m.reveal {
				resolved = strings.Repeat("•", min(len(resolved), 8))
			}
			value += stMuted.Render(" → ") + resolved
		}
		lines = append(lines, line(i == e.cursor, fmt.Sprintf("%s %-*s  %s", checkbox(v.Enabled), nameW, v.Name, value)))
	}
	if len(lines) == 0 {
		lines = append(lines, stMuted.Render("No variables. a adds one."))
	}
	lines, e.start = listView(lines, e.cursor, h-10)
	if e.cursor < len(e.env.Vars) && e.env.Vars[e.cursor].Description != "" {
		lines = append(lines, "", stMuted.Render(e.env.Vars[e.cursor].Description))
	}
	title := "Environment " + e.env.Name
	if m.envDirty[e.env] {
		title += " ●"
	}
	return box(title, lines, modalWidth(w))
}

func (e *envEditor) help() string {
	return "enter edit · space on/off · n rename · t secret · a add · d delete · ctrl+s save file · esc close"
}

func (e *envEditor) click(_ *Model, _, y int) tea.Cmd {
	if i := e.start + y - boxTop; i >= 0 && i < len(e.env.Vars) {
		e.cursor = i
	}
	return nil
}

func (e *envEditor) wheel(_ *Model, d int) {
	e.cursor = max(0, min(e.cursor+d, len(e.env.Vars)-1))
}

func (m *Model) openVars() tea.Cmd {
	m.modal = &varsModal{}
	return nil
}

type varsModal struct{ cursor, start int }

func (v *varsModal) all(m *Model) []vars.Var { return m.s.Scope(m.current).All() }

func (v *varsModal) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	all := v.all(m)
	switch msg.String() {
	case "esc", "q", "v":
		m.modal = nil
	case "up", "k":
		v.cursor = max(0, v.cursor-1)
	case "down", "j":
		v.cursor = max(0, min(len(all)-1, v.cursor+1))
	case "enter":
		if m.busy || v.cursor >= len(all) {
			return nil
		}
		name := all[v.cursor].Name
		return m.openForm("Runtime value for "+name, "Runtime variables win over every other scope until brucli quits.", []*field{textField("Value", all[v.cursor].Raw)}, 0, func(s []string) tea.Cmd {
			m.s.Runtime[name] = s[0]
			return nil
		})
	case "a":
		if m.busy {
			return nil
		}
		return m.kvForm("New runtime variable", "", "", 0, func(name, value string) { m.s.Runtime[name] = value })
	case "d", "delete":
		if !m.busy && v.cursor < len(all) && all[v.cursor].Source == vars.Runtime {
			delete(m.s.Runtime, all[v.cursor].Name)
			v.cursor = max(0, min(v.cursor, len(all)-2))
		}
	case "D":
		if !m.busy {
			clear(m.s.Runtime)
		}
	}
	return nil
}

func (v *varsModal) view(m *Model, w, h int) string {
	all := v.all(m)
	nameW := 8
	for _, x := range all {
		nameW = max(nameW, len(x.Name))
	}
	var lines []string
	for i, x := range all {
		val := markVars(x.Raw)
		if x.Resolved != x.Raw {
			val += stMuted.Render(" → ") + x.Resolved
		}
		if runner.LooksSecret(x.Name) && !m.reveal {
			val = strings.Repeat("•", 8)
		}
		src := stMuted.Render(fmt.Sprintf("%-11s", x.Source))
		if x.Source == vars.Runtime {
			src = stWarn.Render(fmt.Sprintf("%-11s", x.Source))
		}
		shadow := ""
		if len(x.Shadowed) > 0 {
			var s []string
			for _, src := range x.Shadowed {
				s = append(s, string(src))
			}
			shadow = stMuted.Render("  overrides " + strings.Join(s, ", "))
		}
		lines = append(lines, line(i == v.cursor, fmt.Sprintf("%-*s %s %s%s", nameW, x.Name, src, val, shadow)))
	}
	if len(lines) == 0 {
		lines = append(lines, stMuted.Render("No variables."))
	}
	title := "Variables"
	if m.current != nil {
		title += " for " + m.current.Name
	}
	lines, v.start = listView(lines, v.cursor, h-10)
	lines = append(lines, "", stMuted.Render("precedence: runtime > request > folder > environment > collection > global · {{process.env.X}} reads .env"))
	return box(title, lines, modalWidth(w))
}

func (v *varsModal) help() string {
	return "enter set runtime value · a add runtime · d delete runtime · D clear runtime · esc close"
}

func (v *varsModal) click(m *Model, _, y int) tea.Cmd {
	if i := v.start + y - boxTop; i >= 0 && i < len(v.all(m)) {
		v.cursor = i
	}
	return nil
}

func (v *varsModal) wheel(m *Model, d int) {
	v.cursor = max(0, min(v.cursor+d, len(v.all(m))-1))
}

func (m *Model) openRunSetup(f *collection.Folder) tea.Cmd {
	m.modal = &runSetup{folder: f}
	return nil
}

// runSetup picks tags before a folder run.
type runSetup struct {
	folder           *collection.Folder
	include, exclude []string
}

func (r *runSetup) selected() []*collection.Request {
	return runner.Filter(r.folder.AllRequests(), r.include, r.exclude)
}

func (r *runSetup) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	tags := func(v string) []string {
		var out []string
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	switch msg.String() {
	case "esc", "q":
		m.modal = nil
	case "t":
		return m.ask("only tags (comma separated, empty = all)", strings.Join(r.include, ","), func(v string) tea.Cmd { r.include = tags(v); return nil })
	case "x":
		return m.ask("skip tags (comma separated)", strings.Join(r.exclude, ","), func(v string) tea.Cmd { r.exclude = tags(v); return nil })
	case "r":
		r.include, r.exclude = []string{"read"}, nil
	case "enter":
		reqs := r.selected()
		if len(reqs) == 0 {
			m.setStatus(errors.New("change the tags"), "nothing to run")
			return nil
		}
		if m.busy {
			m.setStatus(errors.New("wait for it"), "a request is running")
			return nil
		}
		var writes []string
		for _, q := range reqs {
			if !q.HasTag("read") {
				writes = append(writes, q.Name)
			}
		}
		if len(writes) == 0 {
			return m.startRun(r.folder, reqs, r.include, r.exclude)
		}
		env := "no environment"
		if m.s.Env != nil {
			env = m.s.Env.Name
		}
		m.modal = &confirmModal{
			title: "Run requests that change data?",
			lines: []string{
				fmt.Sprintf("%d of %d requests are not tagged read and run against %s:", len(writes), len(reqs), stWarn.Render(env)),
				stMuted.Render(strings.Join(writes, ", ")),
				"", "y run all · n or esc cancel",
			},
			yes: func() tea.Cmd { return m.startRun(r.folder, reqs, r.include, r.exclude) },
		}
	}
	return nil
}

func (r *runSetup) view(m *Model, w, _ int) string {
	reqs := r.selected()
	reads := 0
	for _, q := range reqs {
		if q.HasTag("read") {
			reads++
		}
	}
	or := func(v []string, def string) string {
		if len(v) == 0 {
			return stMuted.Render(def)
		}
		return strings.Join(v, ", ")
	}
	return box("Run "+m.folderName(r.folder), []string{
		fmt.Sprintf("only tags  %s   %s", or(r.include, "all"), stMuted.Render("t changes")),
		fmt.Sprintf("skip tags  %s   %s", or(r.exclude, "none"), stMuted.Render("x changes")),
		"",
		fmt.Sprintf("%d requests: %d read, %s", len(reqs), reads, stWarn.Render(fmt.Sprintf("%d write", len(reqs)-reads))),
		stMuted.Render("Requests run in order; scripts may jump with bru.setNextRequest or stop the run."),
		"",
		stTitle.Render("enter") + " run   " + stTitle.Render("r") + " reads only   " + stTitle.Render("esc") + " close",
	}, min(modalWidth(w), 80))
}

func (r *runSetup) help() string {
	return "t only tags · x skip tags · r reads only · enter run · esc close"
}

// runModal shows a folder run. start and drawn are the first row and row count of the last view.
type runModal struct{ cursor, start, drawn int }

func (m *Model) showRun() tea.Cmd {
	m.modal = &runModal{}
	return nil
}

func (rm *runModal) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	st := m.lastRun
	switch msg.String() {
	case "esc", "q":
		if st != nil && st.active {
			st.cancel()
			m.setStatus(nil, "stopping the run")
			return nil
		}
		m.modal = nil
	case "up", "k":
		rm.cursor = max(0, rm.cursor-1)
	case "down", "j":
		if st != nil {
			rm.cursor = max(0, min(len(st.results)-1, rm.cursor+1))
		}
	case "enter":
		return rm.open(m, rm.cursor)
	}
	return nil
}

func (rm *runModal) open(m *Model, i int) tea.Cmd {
	st := m.lastRun
	if st == nil || i < 0 || i >= len(st.results) {
		return nil
	}
	r := st.origin[st.results[i].Request]
	if !slices.Contains(r.Folder.Requests, r) {
		m.setStatus(errors.New("it was deleted"), "cannot open %s", r.Name)
		return nil
	}
	m.modal = nil
	m.open(r)
	m.focus = focusResponse
	return nil
}

func (rm *runModal) view(m *Model, w, h int) string {
	st := m.lastRun
	if st == nil {
		return box("Run", []string{"no run"}, modalWidth(w))
	}
	var counts runner.Counts
	var lines []string
	for i, res := range st.results {
		counts.Add(res)
		r := res.Request
		mark, detail := stOK.Render("✓"), ""
		switch {
		case res.Skipped:
			mark, detail = stMuted.Render("-"), stMuted.Render("skipped")
		case res.Err != nil:
			mark, detail = stErr.Render("✗"), stErr.Render(res.Err.Error())
		default:
			detail = statusStyle(res.Response.Status).Render(fmt.Sprint(res.Response.Status)) + stMuted.Render(" "+elapsed(res.Response.Duration))
			if res.Failed() {
				mark = stErr.Render("✗")
				detail += "  " + stErr.Render(firstFailure(res))
			}
		}
		lines = append(lines, line(i == rm.cursor, fmt.Sprintf("%s %s %-28s %s", mark, methodTag(r.Method), r.Name, detail)))
	}
	state := fmt.Sprintf("%d/%d done", len(st.results), len(st.reqs))
	if st.active {
		state += stInfo.Render(" · running")
	} else {
		state += " · " + counts.String()
	}
	lines, rm.start = listView(lines, rm.cursor, h-10)
	rm.drawn = len(lines)
	return box("Run "+st.name, append([]string{state, ""}, lines...), modalWidth(w))
}

func firstFailure(res *runner.Result) string {
	if len(res.ScriptErrors) > 0 {
		return res.ScriptErrors[0]
	}
	for _, a := range res.Assertions {
		if !a.Passed {
			return a.Assertion.Expression + ": " + a.Error
		}
	}
	for _, t := range res.Tests {
		if !t.Passed {
			return t.Name + ": " + t.Error
		}
	}
	return ""
}

func (rm *runModal) help() string {
	return "↑↓ move · enter or click opens a result · esc stops or closes"
}

// click opens the clicked result; the list starts below the state line and a blank line.
func (rm *runModal) click(m *Model, _, y int) tea.Cmd {
	if row := y - boxTop - 2; row >= 0 && row < rm.drawn {
		return rm.open(m, rm.start+row)
	}
	return nil
}

func (rm *runModal) wheel(m *Model, d int) {
	if m.lastRun != nil {
		rm.cursor = max(0, min(rm.cursor+d, len(m.lastRun.results)-1))
	}
}
