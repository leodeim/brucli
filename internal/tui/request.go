package tui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/runner"
)

type reqTab int

const (
	tabParams reqTab = iota
	tabBody
	tabHeaders
	tabAuth
	tabVars
	tabScripts
	tabAssert
	tabDocs
)

var reqTabNames = []string{"Params", "Body", "Headers", "Auth", "Vars", "Scripts", "Assert", "Docs"}

var (
	methods   = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"}
	bodyModes = []string{"none", "json", "text", "xml", "form-urlencoded", "multipart-form", "graphql"}
	authModes = []string{"inherit", "none", "basic", "bearer", "apikey", "digest", "ntlm", "oauth1", "oauth2", "awsv4", "wsse", "akamai-edgegrid"}
	operators = []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "notIn", "contains", "notContains", "length", "matches", "notMatches", "startsWith", "endsWith", "between", "isEmpty", "isNotEmpty", "isNull", "isUndefined", "isDefined", "isTruthy", "isFalsy", "isJson", "isNumber", "isString", "isBoolean", "isArray"}
	bodyExt   = map[string]string{"json": ".json", "text": ".txt", "xml": ".xml", "sparql": ".sparql", "graphql": ".graphql"}
)

type requestPane struct {
	tab    reqTab
	cursor map[reqTab]int
	scroll int
	// spec and names are the last built tab and tab labels, for clicks and menu labels.
	spec   tabSpec
	names  []string
	editor *editor
}

// row is one line of a tab; selectable rows take the row actions (edit, toggle, rename, delete).
type row struct {
	line string
	sel  bool
	// what names the row in menus, e.g. "header".
	what   string
	toggle func()
	edit   func() tea.Cmd
	rename func() tea.Cmd
	del    func()
	// click runs when the row is clicked; selectable rows without it are selected, then edited.
	click func() tea.Cmd
}

type tabSpec struct {
	rows     []row
	add      func() tea.Cmd
	addLabel string
	// edit is what enter does when no selectable row is under the cursor, e.g. open the body editor.
	edit     func() tea.Cmd
	editWhat string
	external func() tea.Cmd
}

func text(line string) row { return row{line: line} }

func (p *requestPane) build(m *Model) tabSpec {
	r := m.current
	if r == nil {
		return tabSpec{rows: []row{text(stMuted.Render("no request selected"))}}
	}
	spec := map[reqTab]func(*Model, *collection.Request) tabSpec{
		tabParams: paramsTab, tabBody: bodyTab, tabHeaders: headersTab, tabAuth: authTab,
		tabVars: varsTab, tabScripts: scriptsTab, tabAssert: assertTab, tabDocs: docsTab,
	}[p.tab](m, r)
	p.spec = spec
	return spec
}

func selectable(spec tabSpec) []int {
	var idx []int
	for i, r := range spec.rows {
		if r.sel {
			idx = append(idx, i)
		}
	}
	return idx
}

// currentRow is the selected row of the current tab, or nil when the tab has none.
func (p *requestPane) currentRow(m *Model) *row {
	spec := p.build(m)
	sel := selectable(spec)
	if len(sel) == 0 {
		return nil
	}
	p.cursor[p.tab] = min(p.cursor[p.tab], len(sel)-1)
	return &p.spec.rows[sel[p.cursor[p.tab]]]
}

// editFunc is what enter does: edit the selected row, or the tab's own edit.
func (p *requestPane) editFunc(m *Model) func() tea.Cmd {
	if r := p.currentRow(m); r != nil && r.edit != nil {
		return r.edit
	}
	return p.spec.edit
}

// rowWhat names the selected row for the row actions' menu labels.
func (p *requestPane) rowWhat(m *Model) string {
	if r := p.currentRow(m); r != nil {
		return r.what
	}
	return "row"
}

// what names the thing enter edits, for menu labels.
func (p *requestPane) what(m *Model) string {
	if r := p.currentRow(m); r != nil {
		return r.what
	}
	return cmp.Or(p.spec.editWhat, "row")
}

func (p *requestPane) view(m *Model, w, h int) []string {
	r := m.current
	if r == nil {
		return []string{stMuted.Render("Pick a request in the sidebar, or press a there to create one.")}
	}
	spec := p.build(m)
	p.names = slices.Clone(reqTabNames)
	for i, n := range p.names {
		if c := tabCount(r, reqTab(i)); c > 0 {
			p.names[i] = fmt.Sprintf("%s %d", n, c)
		}
	}
	url := methodTag(r.Method) + " " + markVars(r.URL)
	if pr := m.prompt; pr != nil && pr.inline {
		pr.input.Width = max(10, w-9)
		url = methodTag(r.Method) + " " + pr.input.View()
	}
	lines := []string{url, tabs(p.names, int(p.tab))}
	if p.editor != nil {
		return append(lines, p.editor.view(w, h-len(lines))...)
	}
	body := h - len(lines)
	sel := selectable(spec)
	cur := -1
	if len(sel) > 0 {
		p.cursor[p.tab] = min(p.cursor[p.tab], len(sel)-1)
		cur = sel[p.cursor[p.tab]]
		p.scroll = max(min(p.scroll, cur), cur-body+1)
	}
	p.scroll = max(0, min(p.scroll, len(spec.rows)-body))
	for i := p.scroll; i < len(spec.rows) && len(lines) < h; i++ {
		line := spec.rows[i].line
		if i == cur && m.focus == focusRequest {
			line = stCursor.Render(fit(stripStyles(line), w))
		}
		lines = append(lines, line)
	}
	return lines
}

func tabCount(r *collection.Request, t reqTab) int {
	count := func(n int, on func(int) bool) int {
		c := 0
		for i := range n {
			if on(i) {
				c++
			}
		}
		return c
	}
	switch t {
	case tabParams:
		return count(len(r.Params), func(i int) bool { return r.Params[i].Enabled })
	case tabHeaders:
		return count(len(r.Headers), func(i int) bool { return r.Headers[i].Enabled })
	case tabVars:
		return count(len(r.Vars), func(i int) bool { return r.Vars[i].Enabled }) + count(len(r.Actions), func(i int) bool { return r.Actions[i].Enabled })
	case tabAssert:
		return count(len(r.Assertions), func(i int) bool { return r.Assertions[i].Enabled })
	case tabScripts:
		return count(3, func(i int) bool { return []string{r.Scripts.Before, r.Scripts.After, r.Scripts.Tests}[i] != "" })
	}
	return 0
}

// key moves around the pane; the edit keys are actions (see actions.go).
func (p *requestPane) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	if m.current == nil {
		return nil
	}
	sel := selectable(p.build(m))
	k := msg.String()
	move := func(d int) {
		if len(sel) > 0 {
			p.cursor[p.tab] = max(0, min(len(sel)-1, p.cursor[p.tab]+d))
		} else {
			p.scroll = max(0, p.scroll+d)
		}
	}
	switch k {
	case "right", "l":
		return m.cycleTab(1)
	case "left", "h":
		return m.cycleTab(-1)
	case "1", "2", "3", "4", "5", "6", "7", "8":
		p.switchTab(reqTab(k[0] - '1'))
	case "up", "k":
		move(-1)
	case "down", "j":
		move(1)
	case "pgdown":
		move(10)
	case "pgup":
		move(-10)
	case "home", "g":
		move(-1 << 20)
	case "end", "G":
		move(1 << 20)
	}
	return nil
}

func (p *requestPane) switchTab(t reqTab) {
	if t != p.tab {
		p.editor = nil
	}
	p.tab = t
	p.scroll = 0
}

// click handles a left click on line y of the pane's content (0 is the URL, 1 the tabs).
func (p *requestPane) click(m *Model, x, y int) tea.Cmd {
	if m.current == nil {
		return nil
	}
	switch y {
	case 0:
		if x < 7 {
			return m.pickMethod()
		}
		return m.editURL()
	case 1:
		if t := tabAt(p.names, x); t >= 0 {
			p.switchTab(reqTab(t))
		}
		return nil
	}
	i := p.scroll + y - 2
	if i < 0 || i >= len(p.spec.rows) {
		if p.spec.edit != nil {
			return p.spec.edit()
		}
		return nil
	}
	rw := p.spec.rows[i]
	switch {
	case rw.click != nil:
		return rw.click()
	case rw.sel:
		n := slices.Index(selectable(p.spec), i)
		was := p.cursor[p.tab] == n
		p.cursor[p.tab] = n
		switch {
		case x < 4 && rw.toggle != nil:
			rw.toggle()
		case was && rw.edit != nil:
			return rw.edit()
		}
	case p.spec.edit != nil:
		return p.spec.edit()
	}
	return nil
}

// tabAt is the tab under column x of a tab line drawn by tabs.
func tabAt(names []string, x int) int {
	at := 0
	for i, n := range names {
		w := len([]rune(n)) + 2
		if x >= at && x < at+w {
			return i
		}
		at += w
	}
	return -1
}

func checkbox(on bool) string {
	if on {
		return stOK.Render("[x]")
	}
	return stMuted.Render("[ ]")
}

// kvRow renders and edits one switchable name/value row.
func kvRow(m *Model, kv *collection.KV, what, label string, onChange, del func()) row {
	line := checkbox(kv.Enabled) + " " + stBold.Render(kv.Name) + "  " + markVars(kv.Value)
	if label != "" {
		line += "  " + stMuted.Render(label)
	}
	if kv.Description != "" {
		line += "  " + stMuted.Render(strings.ReplaceAll(kv.Description, "\n", " "))
	}
	edit := func(focus int) func() tea.Cmd {
		return func() tea.Cmd {
			return m.kvForm("Edit "+what, kv.Name, kv.Value, focus, func(name, value string) {
				kv.Name, kv.Value = name, value
				onChange()
			})
		}
	}
	return row{
		line:   line,
		sel:    true,
		what:   what,
		toggle: func() { kv.Enabled = !kv.Enabled; onChange() },
		edit:   edit(1),
		rename: edit(0),
		del:    del,
	}
}

func kvRows(m *Model, list *[]collection.KV, what string, onChange func()) []row {
	var rows []row
	for i := range *list {
		rows = append(rows, kvRow(m, &(*list)[i], what, "", onChange, func() {
			*list = slices.Delete(*list, i, i+1)
			onChange()
		}))
	}
	return rows
}

// kvForm edits a name and a value. A Name typed as name=value (or "Name: value") fills both.
func (m *Model) kvForm(title, name, value string, focus int, apply func(name, value string)) tea.Cmd {
	fields := []*field{textField("Name", name).hint("name, or name=value to fill both"), textField("Value", value).hint("value; {{variables}} work")}
	return m.openForm(title, "", fields, focus, func(v []string) tea.Cmd {
		n, val := strings.TrimSpace(v[0]), v[1]
		if val == "" {
			if a, b, ok := strings.Cut(n, "="); ok {
				n, val = strings.TrimSpace(a), strings.TrimSpace(b)
			} else if a, b, ok := strings.Cut(n, ": "); ok {
				n, val = strings.TrimSpace(a), strings.TrimSpace(b)
			}
		}
		if n == "" {
			m.setStatus(errors.New("a name is required"), "nothing changed")
			return nil
		}
		apply(n, val)
		return nil
	})
}

// addKV appends an enabled entry to list through kvForm.
func addKV(m *Model, what string, list *[]collection.KV, onChange func()) func() tea.Cmd {
	return func() tea.Cmd {
		return m.kvForm("Add "+what, "", "", 0, func(name, value string) {
			*list = append(*list, collection.KV{Name: name, Value: value, Enabled: true})
			onChange()
		})
	}
}

func paramsTab(m *Model, r *collection.Request) tabSpec {
	onChange := func() {
		syncURL(r)
		m.changed(r)
	}
	var rows []row
	for i := range r.Params {
		p := &r.Params[i]
		if p.Type == "path" {
			rows = append(rows, row{
				line: stMuted.Render("[:]") + " " + stBold.Render(p.Name) + "  " + markVars(p.Value) + "  " + stMuted.Render("path"),
				sel:  true,
				what: "path param",
				edit: func() tea.Cmd {
					return m.openForm("Path param :"+p.Name, "", []*field{textField("Value", p.Value)}, 0, func(v []string) tea.Cmd {
						p.Value = v[0]
						onChange()
						return nil
					})
				},
			})
			continue
		}
		rows = append(rows, kvRow(m, &p.KV, "query param", "", onChange, func() {
			r.Params = slices.Delete(r.Params, i, i+1)
			onChange()
		}))
	}
	if len(rows) == 0 {
		rows = append(rows, text(stMuted.Render("No params. a adds a query param; :name in the URL makes a path param.")))
	}
	return tabSpec{
		rows:     rows,
		addLabel: "Add query param",
		add: func() tea.Cmd {
			return m.kvForm("Add query param", "", "", 0, func(name, value string) {
				r.Params = append(r.Params, collection.Param{KV: collection.KV{Name: name, Value: value, Enabled: true}, Type: "query"})
				onChange()
			})
		},
	}
}

// syncURL rewrites the URL's query string from the enabled query params, as Bruno's editor does.
func syncURL(r *collection.Request) {
	base, _, _ := strings.Cut(r.URL, "?")
	var q []string
	for _, p := range r.Params {
		if p.Type == "query" && p.Enabled {
			q = append(q, p.Name+"="+p.Value)
		}
	}
	r.URL = base
	if len(q) > 0 {
		r.URL += "?" + strings.Join(q, "&")
	}
}

// syncParams rebuilds the params from an edited URL: its query becomes the enabled query params,
// disabled ones stay, and path params follow the :name segments.
func syncParams(r *collection.Request) {
	base, query, _ := strings.Cut(r.URL, "?")
	old := r.Params
	var params []collection.Param
	for _, seg := range strings.Split(base, "/") {
		name, ok := strings.CutPrefix(seg, ":")
		if !ok || name == "" {
			continue
		}
		p := collection.Param{KV: collection.KV{Name: name, Enabled: true}, Type: "path"}
		if i := slices.IndexFunc(old, func(o collection.Param) bool { return o.Type == "path" && o.Name == name }); i >= 0 {
			p = old[i]
		}
		params = append(params, p)
	}
	used := map[int]bool{}
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		p := collection.Param{KV: collection.KV{Name: name, Value: value, Enabled: true}, Type: "query"}
		if i := slices.IndexFunc(old, func(o collection.Param) bool {
			return o.Type == "query" && o.Enabled && o.Name == name && !used[slices.Index(old, o)]
		}); i >= 0 {
			used[i] = true
			p.KV = old[i].KV
			p.Value = value
		}
		params = append(params, p)
	}
	for _, o := range old {
		if o.Type == "query" && !o.Enabled {
			params = append(params, o)
		}
	}
	r.Params = params
}

func (m *Model) editURL() tea.Cmd {
	r := m.current
	if r == nil {
		return nil
	}
	m.focus = focusRequest
	m.req.editor = nil
	cmd := m.ask("URL", r.URL, func(v string) tea.Cmd {
		if v = strings.TrimSpace(v); v != r.URL {
			r.URL = v
			syncParams(r)
			m.changed(r)
		}
		return nil
	})
	m.prompt.inline = true
	return cmd
}

func (m *Model) pickMethod() tea.Cmd {
	r := m.current
	if r == nil {
		return nil
	}
	items := make([]pickItem, len(methods))
	for i, x := range methods {
		items[i] = pickItem{label: x}
	}
	return m.choose("Method", items, slices.Index(methods, r.Method), func(i int) tea.Cmd {
		if methods[i] != r.Method {
			r.Method = methods[i]
			m.changed(r)
		}
		return nil
	})
}

func bodyLang(mode string) string {
	return cmp.Or(map[string]string{"json": "json", "xml": "xml"}[mode], "text")
}

// editBody opens the body in the editor, or asks for a body type first when there is none.
func (m *Model) editBody() tea.Cmd {
	r := m.current
	if r == nil {
		return nil
	}
	m.focus = focusRequest
	m.req.editor = nil
	m.req.switchTab(tabBody)
	switch mode := cmp.Or(r.Body.Mode, "none"); mode {
	case "none":
		return m.pickBodyType()
	case "form-urlencoded", "multipart-form":
		m.setStatus(nil, "a form body: a adds a field, enter edits the selected one")
	case "json", "text", "xml", "sparql", "graphql":
		m.openEditor("Body ("+mode+")", bodyLang(mode), r.Body.Data, func(v string) {
			if v != r.Body.Data {
				r.Body.Data = v
				m.changed(r)
			}
		})
	default:
		m.setStatus(errors.New("brucli keeps it as written"), "body type %q is not editable", mode)
	}
	return nil
}

func (m *Model) pickBodyType() tea.Cmd {
	r := m.current
	if r == nil {
		return nil
	}
	mode := cmp.Or(r.Body.Mode, "none")
	if !slices.Contains(bodyModes, mode) {
		m.setStatus(errors.New("brucli keeps it as written"), "body type %q is not editable", mode)
		return nil
	}
	items := []pickItem{
		{"none", "no body"}, {"json", "application/json"}, {"text", "text/plain"},
		{"xml", "application/xml"}, {"form-urlencoded", "application/x-www-form-urlencoded"},
		{"multipart-form", "text fields and file uploads"}, {"graphql", "GraphQL query and variables"},
	}
	m.req.editor = nil
	return m.choose("Body type", items, slices.Index(bodyModes, mode), func(i int) tea.Cmd {
		next := bodyModes[i]
		if next != mode {
			r.Body.Mode = next
			if next == "graphql" {
				r.Kind, r.Method = "graphql", "POST"
			} else if r.Kind == "graphql" {
				r.Kind = "http"
			}
			m.changed(r)
		}
		if next == "none" {
			m.focus = focusRequest
			m.req.switchTab(tabBody)
			return nil
		}
		seeded := next == "json" && strings.TrimSpace(r.Body.Data) == ""
		if seeded {
			r.Body.Data = "{\n  \n}"
			m.changed(r)
		}
		cmd := m.editBody()
		if e := m.req.editor; e != nil && seeded {
			e.row, e.col = 1, 2
		}
		return cmd
	})
}

func (m *Model) pickAuthMode() tea.Cmd {
	r := m.current
	if r == nil {
		return nil
	}
	a := &r.Auth
	items := []pickItem{
		{"inherit", "use the folder's or the collection's auth"}, {"none", "send no auth"},
		{"basic", "username and password"}, {"bearer", "token"}, {"apikey", "key and value in a header or query"},
		{"digest", "HTTP Digest"}, {"ntlm", "NTLM domain authentication"}, {"oauth1", "OAuth 1 request signing"}, {"oauth2", "OAuth 2 tokens and browser login"},
		{"awsv4", "AWS Signature Version 4"}, {"wsse", "WSSE UsernameToken"}, {"akamai-edgegrid", "Akamai EdgeGrid request signing"},
	}
	return m.choose("Auth mode", items, slices.Index(authModes, a.Mode), func(i int) tea.Cmd {
		if authModes[i] != a.Mode {
			*a = collection.Auth{Mode: authModes[i], Username: a.Username, Password: a.Password, Token: a.Token, Key: a.Key, Value: a.Value, Placement: a.Placement}
			a.Options = map[string]string{}
			for key, value := range authDefaults(a.Mode) {
				a.Options[key] = value
			}
			if _, ok := a.Options["username"]; ok {
				a.Options["username"] = a.Username
			}
			if _, ok := a.Options["password"]; ok {
				a.Options["password"] = a.Password
			}
			m.changed(r)
		}
		m.focus = focusRequest
		m.req.switchTab(tabAuth)
		return nil
	})
}

func bodyTab(m *Model, r *collection.Request) tabSpec {
	mode := cmp.Or(r.Body.Mode, "none")
	spec := tabSpec{editWhat: "body"}
	spec.rows = append(spec.rows, row{line: stMuted.Render("type ") + stBold.Render(mode) + stMuted.Render(" ▾"), click: m.pickBodyType})
	switch mode {
	case "none":
		spec.rows = append(spec.rows, text(stMuted.Render("No body. enter picks a type and opens the editor.")))
		spec.edit = m.pickBodyType
		return spec
	case "form-urlencoded", "multipart-form":
		onChange := func() { m.changed(r) }
		spec.rows = append(spec.rows, kvRows(m, &r.Body.Form, "form field", onChange)...)
		spec.add, spec.addLabel = addKV(m, "form field", &r.Body.Form, onChange), "Add form field"
		if mode == "multipart-form" {
			for i := range r.Body.Form {
				field := &r.Body.Form[i]
				spec.rows = append(spec.rows, row{line: field.Name + stMuted.Render(" type: ") + cmp.Or(field.Type, "text"), sel: true, what: "field type", edit: func() tea.Cmd {
					return m.choose("Field type", []pickItem{{"text", "send a text value"}, {"file", "upload files, one collection-relative path per line"}}, 0, func(i int) tea.Cmd {
						field.Type = []string{"text", "file"}[i]
						onChange()
						if i == 1 {
							m.openEditor("File paths: "+field.Name, "text", field.Value, func(value string) { field.Value = value; onChange() })
						}
						return nil
					})
				}})
			}
		}
		return spec
	case "graphql":
		spec.rows = append(spec.rows, row{line: stInfo.Render("Variables (JSON)"), sel: true, what: "GraphQL variables", edit: func() tea.Cmd {
			m.openEditor("GraphQL variables", "json", r.Body.Variables, func(value string) { r.Body.Variables = value; m.changed(r) })
			return nil
		}})
	case "json", "text", "xml", "sparql":
	default:
		spec.rows = append(spec.rows, text(stWarn.Render("brucli cannot edit or send this body type; it is kept in the file as written.")))
		return spec
	}
	spec.edit = m.editBody
	spec.external = func() tea.Cmd {
		return m.external(r.Body.Data, bodyExt[mode], func(v string) tea.Cmd {
			r.Body.Data = v
			m.changed(r)
			return nil
		})
	}
	data := r.Body.Data
	if strings.TrimSpace(data) == "" {
		spec.rows = append(spec.rows, row{line: stMuted.Render("Empty. enter edits it."), click: m.editBody})
		return spec
	}
	if mode == "json" {
		if err := jsonProblem(data); err != nil {
			spec.rows = append(spec.rows, text(stWarn.Render(err.Error()+" (sent as written)")))
		}
	}
	for _, l := range strings.Split(data, "\n") {
		spec.rows = append(spec.rows, row{line: highlight(bodyLang(mode), l), click: m.editBody})
	}
	return spec
}

func headersTab(m *Model, r *collection.Request) tabSpec {
	onChange := func() { m.changed(r) }
	rows := kvRows(m, &r.Headers, "header", onChange)
	if len(rows) == 0 {
		rows = append(rows, text(stMuted.Render("No request headers. a adds one.")))
	}
	var inherited []string
	for _, f := range r.Chain() {
		src := "folder " + f.Name
		if f == m.c.Root {
			src = "collection"
		}
		for _, h := range f.Defaults.Headers {
			if h.Enabled {
				inherited = append(inherited, stMuted.Render(fmt.Sprintf("%-16s", src))+h.Name+": "+markVars(h.Value))
			}
		}
	}
	if a, src := runner.EffectiveAuth(r); a.Mode == "basic" || a.Mode == "bearer" {
		inherited = append(inherited, stMuted.Render(fmt.Sprintf("%-16s", "auth "+src))+"Authorization: "+stMuted.Render(a.Mode))
	}
	if ct, ok := map[string]string{"json": "application/json", "text": "text/plain", "xml": "application/xml", "form-urlencoded": "application/x-www-form-urlencoded"}[r.Body.Mode]; ok {
		inherited = append(inherited, stMuted.Render(fmt.Sprintf("%-16s", "body"))+"Content-Type: "+ct+stMuted.Render(" (unless set above)"))
	}
	if len(inherited) > 0 {
		rows = append(rows, text(""), text(stMuted.Render("Also sent")))
		for _, l := range inherited {
			rows = append(rows, text("  "+l))
		}
	}
	return tabSpec{rows: rows, add: addKV(m, "header", &r.Headers, onChange), addLabel: "Add header"}
}

func authTab(m *Model, r *collection.Request) tabSpec {
	a := &r.Auth
	onChange := func() { m.changed(r) }
	spec := tabSpec{edit: m.pickAuthMode, editWhat: "auth mode"}
	spec.rows = append(spec.rows, row{line: stMuted.Render("mode ") + stBold.Render(a.Mode) + stMuted.Render(" ▾"), click: m.pickAuthMode})
	authField := func(label string, v *string, secret bool) row {
		shown := markVars(*v)
		if secret && !m.reveal && !strings.Contains(*v, "{{") && *v != "" {
			shown = "••••••"
		}
		return row{line: fmt.Sprintf("%-10s %s", label, shown), sel: true, what: label, edit: func() tea.Cmd {
			f := textField(label, *v)
			if secret {
				f = secretField(label, *v, !m.reveal)
			}
			return m.openForm("Auth "+label, "", []*field{f}, 0, func(vals []string) tea.Cmd {
				*v = vals[0]
				onChange()
				return nil
			})
		}}
	}
	switch a.Mode {
	case "inherit":
		eff, src := runner.EffectiveAuth(r)
		spec.rows = append(spec.rows, text(stMuted.Render("uses ")+stBold.Render(eff.Mode)+stMuted.Render(" from "+src)))
		scope := m.s.Scope(r)
		show := func(label, v string, secret bool) {
			resolved := scope.Interpolate(v)
			if secret && !m.reveal {
				resolved = strings.Repeat("•", min(len(resolved), 8))
			}
			spec.rows = append(spec.rows, text(fmt.Sprintf("  %-10s %s %s %s", label, markVars(v), stMuted.Render("→"), resolved)))
		}
		switch eff.Mode {
		case "basic":
			show("username", eff.Username, false)
			show("password", eff.Password, true)
		case "bearer":
			show("token", eff.Token, true)
		case "apikey":
			show(eff.Key, eff.Value, true)
		}
	case "none":
		spec.rows = append(spec.rows, text(stMuted.Render("No auth.")))
	case "basic":
		spec.rows = append(spec.rows, authField("username", &a.Username, false), authField("password", &a.Password, true))
	case "bearer":
		spec.rows = append(spec.rows, authField("token", &a.Token, true))
	case "apikey":
		place := row{line: fmt.Sprintf("%-10s %s", "add to", cmp.Or(a.Placement, "header")), sel: true, what: "placement", toggle: func() {
			a.Placement = map[string]string{"query": "header"}[a.Placement]
			if a.Placement == "" {
				a.Placement = "query"
			}
			onChange()
		}}
		spec.rows = append(spec.rows, authField("key", &a.Key, false), authField("value", &a.Value, true), place)
	default:
		if !a.Supported() {
			spec.rows = append(spec.rows, text(stWarn.Render("This auth mode is not supported; the request will not be sent.")))
			break
		}
		if a.Options == nil {
			a.Options = map[string]string{}
		}
		keys := make([]string, 0, len(a.Options))
		for key := range a.Options {
			keys = append(keys, key)
		}
		for key := range authDefaults(a.Mode) {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		for _, key := range keys {
			shown := a.Options[key]
			secret := runner.LooksSecret(key)
			if secret && !m.reveal && !strings.Contains(shown, "{{") && shown != "" {
				shown = "••••••"
			}
			spec.rows = append(spec.rows, row{line: key + "  " + markVars(shown), sel: true, what: key, edit: func() tea.Cmd {
				value := a.Options[key]
				f := textField(key, value)
				if secret {
					f = secretField(key, value, !m.reveal)
				}
				if value == "true" || value == "false" {
					f = choiceField(key, []string{"true", "false"}, value)
				}
				return m.openForm("Auth "+key, "", []*field{f}, 0, func(values []string) tea.Cmd { a.Options[key] = values[0]; onChange(); return nil })
			}})
		}
	}
	return spec
}

func authDefaults(mode string) map[string]string {
	options := map[string]string{}
	keys := map[string][]string{
		"digest": {"username", "password"}, "ntlm": {"username", "password", "domain"}, "wsse": {"username", "password"},
		"oauth1":          {"consumerKey", "consumerSecret", "accessToken", "accessTokenSecret", "signatureMethod", "privateKey", "privateKeyType", "placement"},
		"oauth2":          {"flow", "accessTokenUrl", "authorizationUrl", "callbackUrl", "credentials.clientId", "credentials.clientSecret", "scope"},
		"awsv4":           {"accessKeyId", "secretAccessKey", "sessionToken", "region", "service", "profileName"},
		"akamai-edgegrid": {"clientToken", "clientSecret", "accessToken", "headersToSign"},
	}
	for _, key := range keys[mode] {
		options[key] = ""
	}
	if mode == "oauth1" {
		options["signatureMethod"] = "HMAC-SHA1"
		options["placement"] = "header"
	}
	if mode == "oauth2" {
		options["flow"] = "client_credentials"
	}
	return options
}

func (m *Model) actionForm(title string, name, expr string, focus int, apply func(name, expr string)) tea.Cmd {
	fields := []*field{textField("Name", name), textField("Expression", expr)}
	return m.openForm(title, "The expression runs after the response, e.g. res.body.user.id; the result goes to a runtime variable.", fields, focus, func(v []string) tea.Cmd {
		if n := strings.TrimSpace(v[0]); n != "" {
			apply(n, strings.TrimSpace(v[1]))
		}
		return nil
	})
}

func (m *Model) addPostResponseVar() tea.Cmd {
	r := m.current
	if r == nil {
		return nil
	}
	return m.actionForm("Add post-response variable", "", "res.body.", 0, func(name, expr string) {
		r.Actions = append(r.Actions, collection.Action{Name: name, Expression: expr, Scope: "runtime", Enabled: true})
		m.changed(r)
	})
}

func varsTab(m *Model, r *collection.Request) tabSpec {
	onChange := func() { m.changed(r) }
	rows := []row{text(stMuted.Render("Pre-request variables"))}
	rows = append(rows, kvRows(m, &r.Vars, "variable", onChange)...)
	if len(r.Vars) == 0 {
		rows = append(rows, text(stMuted.Render("  none · a adds one")))
	}
	rows = append(rows, text(""), text(stMuted.Render("Post-response variables (expression over res)")))
	if len(r.Actions) == 0 {
		rows = append(rows, text(stMuted.Render("  none · A adds one, e.g. userId = res.body.user.id")))
	}
	for i := range r.Actions {
		a := &r.Actions[i]
		edit := func(focus int) func() tea.Cmd {
			return func() tea.Cmd {
				return m.actionForm("Edit post-response variable", a.Name, a.Expression, focus, func(name, expr string) {
					a.Name, a.Expression = name, expr
					onChange()
				})
			}
		}
		rows = append(rows, row{
			line:   fmt.Sprintf("%s %s %s %s  %s", checkbox(a.Enabled), stBold.Render(a.Name), stMuted.Render("←"), a.Expression, stMuted.Render(a.Scope)),
			sel:    true,
			what:   "post-response variable",
			toggle: func() { a.Enabled = !a.Enabled; onChange() },
			edit:   edit(1),
			rename: edit(0),
			del:    func() { r.Actions = slices.Delete(r.Actions, i, i+1); onChange() },
		})
	}
	return tabSpec{rows: rows, add: addKV(m, "pre-request variable", &r.Vars, onChange), addLabel: "Add pre-request variable"}
}

func scriptsTab(m *Model, r *collection.Request) tabSpec {
	var rows []row
	type script struct {
		name string
		code *string
	}
	scripts := []script{{"Before request", &r.Scripts.Before}, {"After response", &r.Scripts.After}, {"Tests", &r.Scripts.Tests}}
	for _, s := range scripts {
		code := s.code
		edit := func() tea.Cmd {
			m.openEditor(s.name+" script", "js", *code, func(v string) {
				if v != *code {
					*code = v
					m.changed(r)
				}
			})
			return nil
		}
		label := stMuted.Render("(empty) · enter writes one")
		if *code != "" {
			label = stMuted.Render(strconv.Itoa(len(strings.Split(strings.TrimRight(*code, "\n"), "\n"))) + " lines")
		}
		rows = append(rows, row{
			line: stBold.Render(s.name) + " " + label,
			sel:  true,
			what: strings.ToLower(s.name) + " script",
			edit: edit,
			del:  func() { *code = ""; m.changed(r) },
		})
		if *code != "" {
			for _, l := range strings.Split(*code, "\n") {
				rows = append(rows, row{line: "  " + highlightJS(l), click: edit})
			}
		}
	}
	p := &m.req
	return tabSpec{
		rows: rows,
		external: func() tea.Cmd {
			target := scripts[min(p.cursor[tabScripts], 2)].code
			return m.external(*target, ".js", func(v string) tea.Cmd {
				*target = v
				m.changed(r)
				return nil
			})
		},
	}
}

func (m *Model) assertForm(title string, a collection.Assertion, focus int, apply func(collection.Assertion)) tea.Cmd {
	fields := []*field{textField("Expression", a.Expression), choiceField("Operator", operators, cmp.Or(a.Operator, "eq")), textField("Value", a.Value)}
	return m.openForm(title, "Expression is over res, e.g. res.status or res.body.items.length.", fields, focus, func(v []string) tea.Cmd {
		if a.Expression = strings.TrimSpace(v[0]); a.Expression == "" {
			m.setStatus(errors.New("an expression is required"), "nothing changed")
			return nil
		}
		a.Operator, a.Value = v[1], strings.TrimSpace(v[2])
		apply(a)
		return nil
	})
}

func assertTab(m *Model, r *collection.Request) tabSpec {
	onChange := func() { m.changed(r) }
	var rows []row
	last := m.results[r]
	for i := range r.Assertions {
		a := &r.Assertions[i]
		line := fmt.Sprintf("%s %s  %s %s", checkbox(a.Enabled), stBold.Render(a.Expression), stInfo.Render(a.Operator), markVars(a.Value))
		if last != nil {
			for _, res := range last.Assertions {
				if res.Assertion == *a {
					if res.Passed {
						line += "  " + stOK.Render("✓")
					} else {
						line += "  " + stErr.Render("✗ "+res.Error)
					}
				}
			}
		}
		edit := func(focus int) func() tea.Cmd {
			return func() tea.Cmd {
				return m.assertForm("Edit assertion", *a, focus, func(n collection.Assertion) { *a = n; onChange() })
			}
		}
		rows = append(rows, row{
			line:   line,
			sel:    true,
			what:   "assertion",
			toggle: func() { a.Enabled = !a.Enabled; onChange() },
			edit:   edit(2),
			rename: edit(0),
			del:    func() { r.Assertions = slices.Delete(r.Assertions, i, i+1); onChange() },
		})
	}
	if len(rows) == 0 {
		rows = append(rows, text(stMuted.Render("No assertions. a adds one, e.g. res.status eq 200.")))
	}
	return tabSpec{
		rows:     rows,
		addLabel: "Add assertion",
		add: func() tea.Cmd {
			return m.assertForm("Add assertion", collection.Assertion{Expression: "res.status", Operator: "eq", Value: "200", Enabled: true}, 0,
				func(a collection.Assertion) {
					r.Assertions = append(r.Assertions, a)
					onChange()
				})
		},
	}
}

func docsTab(m *Model, r *collection.Request) tabSpec {
	commit := func(v string) {
		if v != r.Docs {
			r.Docs = v
			m.changed(r)
		}
	}
	edit := func() tea.Cmd { m.openEditor("Docs", "md", r.Docs, commit); return nil }
	var rows []row
	if r.Docs == "" {
		rows = append(rows, row{line: stMuted.Render("No docs. enter writes some."), click: edit})
	} else {
		for _, l := range strings.Split(r.Docs, "\n") {
			rows = append(rows, row{line: highlight("md", l), click: edit})
		}
	}
	return tabSpec{
		rows:     rows,
		edit:     edit,
		editWhat: "docs",
		external: func() tea.Cmd { return m.external(r.Docs, ".md", func(v string) tea.Cmd { commit(v); return nil }) },
	}
}
