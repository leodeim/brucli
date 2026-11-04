package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/runner"
)

type gateway struct {
	mu   sync.Mutex
	seen []string
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	g.mu.Lock()
	g.seen = append(g.seen, r.Method+" "+r.URL.RequestURI())
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"pet": map[string]any{"id": "p-1", "name": "Rex"}}
	if r.URL.Path == "/v1/pets" {
		body = map[string]any{"pets": []any{map[string]any{"id": "p-7"}}}
	}
	_ = json.NewEncoder(w).Encode(body)
}

type harness struct {
	t   *testing.T
	m   *Model
	g   *gateway
	dir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "petstore")
	if err := os.CopyFS(dir, os.DirFS("../collection/testdata/petstore")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PETSTORE_USERNAME=u\nPETSTORE_PASSWORD=p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := collection.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := &gateway{}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	s := runner.NewSession(c, c.Environment("local"), false)
	s.Overrides["baseUrl"] = srv.URL
	h := &harness{t: t, m: New(Config{Session: s, Editor: "true"}), g: g, dir: dir}
	h.do(tea.WindowSizeMsg{Width: 130, Height: 40})
	return h
}

// do feeds msg and every message its commands produce, the way the Bubble Tea loop would.
func (h *harness) do(msg tea.Msg) {
	h.t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		msg, queue = queue[0], queue[1:]
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					queue = append(queue, c())
				}
			}
			continue
		}
		if msg == nil {
			continue
		}
		_, cmd := h.m.Update(msg)
		if cmd != nil {
			if out := cmd(); out != nil {
				if _, quit := out.(tea.QuitMsg); quit {
					return
				}
				queue = append(queue, out)
			}
		}
	}
}

func (h *harness) keys(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		h.do(keyMsg(k))
	}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.do(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func keyMsg(k string) tea.KeyMsg {
	named := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown,
		"ctrl+s": tea.KeyCtrlS, "ctrl+c": tea.KeyCtrlC, "backspace": tea.KeyBackspace, "left": tea.KeyLeft, "right": tea.KeyRight,
		"end": tea.KeyEnd, "home": tea.KeyHome, "f10": tea.KeyF10, "ctrl+f": tea.KeyCtrlF, "ctrl+z": tea.KeyCtrlZ,
		"ctrl+y": tea.KeyCtrlY, "ctrl+k": tea.KeyCtrlK, "ctrl+p": tea.KeyCtrlP, "ctrl+r": tea.KeyCtrlR, "shift+tab": tea.KeyShiftTab,
	}
	if t, ok := named[k]; ok {
		return tea.KeyMsg{Type: t}
	}
	if k == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func (h *harness) screen() string { return ansi.Strip(h.m.View()) }

func (h *harness) click(x, y int) {
	h.t.Helper()
	h.m.View()
	h.do(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
}

func (h *harness) wheel(x, y, d int) {
	h.t.Helper()
	b := tea.MouseButtonWheelDown
	if d < 0 {
		b = tea.MouseButtonWheelUp
	}
	h.m.View()
	h.do(tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress})
}

// tabX is the screen column of tab i in a pane at x drawn with names.
func tabX(paneX int, names []string, i int) int {
	x := paneX
	for _, n := range names[:i] {
		x += len([]rune(n)) + 2
	}
	return x + 1
}

func (h *harness) open(name string) *collection.Request {
	h.t.Helper()
	for _, r := range h.m.c.Root.AllRequests() {
		if r.Name == name {
			h.m.open(r)
			return r
		}
	}
	h.t.Fatalf("no request %s", name)
	return nil
}

func (h *harness) contains(want ...string) {
	h.t.Helper()
	s := h.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			h.t.Fatalf("screen lacks %q:\n%s", w, s)
		}
	}
}

func TestLayoutFitsTheTerminal(t *testing.T) {
	h := newHarness(t)
	lines := strings.Split(h.m.View(), "\n")
	if len(lines) != 40 {
		t.Fatalf("%d lines, want 40", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 130 {
			t.Fatalf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
	h.contains("brucli", "File", "Request", "Environment", "Run", "View", "Help", "petstore", "env local", "Pets", "GET    ListPets", "Params")
	h.contains("? all actions · F10 menu")
}

func TestSendShowsResponse(t *testing.T) {
	h := newHarness(t)
	h.open("GetPet")
	h.keys("s")
	h.contains("Response · 200 OK", "1/1 checks", `"name": "Rex"`)
	if len(h.g.seen) != 1 || h.g.seen[0] != "GET /v1/pets/1" {
		t.Fatalf("seen = %v", h.g.seen)
	}
	h.keys("tab", "3")
	h.contains("Authorization: Basic ****", "X-Api-Version: 2", "curl -X GET")
}

func TestCaptureFeedsNextRequest(t *testing.T) {
	h := newHarness(t)
	h.open("ListPets")
	h.keys("s")
	h.open("GetPet")
	h.keys("s")
	if last := h.g.seen[len(h.g.seen)-1]; last != "GET /v1/pets/p-7" {
		t.Fatalf("get went to %s", last)
	}
}

func TestEditParamsRewritesURLAndSaves(t *testing.T) {
	h := newHarness(t)
	r := h.open("ListPets")
	h.keys(" ")
	if r.URL != "{{baseUrl}}/v1/pets" {
		t.Fatalf("url after disabling the limit = %s", r.URL)
	}
	h.keys("down", "enter")
	h.typeText("rex")
	h.keys("enter", " ")
	if r.URL != "{{baseUrl}}/v1/pets?name=rex" {
		t.Fatalf("url = %s", r.URL)
	}
	h.contains("1 unsaved", "ListPets ●")
	h.keys("ctrl+s")
	h.contains("saved Pets/ListPets.yml")
	c, err := collection.Load(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range c.Root.AllRequests() {
		if x.Name == "ListPets" && x.URL != r.URL {
			t.Fatalf("saved url = %s", x.URL)
		}
	}
}

func TestURLEditRebuildsParams(t *testing.T) {
	r := &collection.Request{
		URL: "{{baseUrl}}/x?a=1",
		Params: []collection.Param{
			{KV: collection.KV{Name: "a", Value: "1", Enabled: true, Description: "kept"}, Type: "query"},
			{KV: collection.KV{Name: "off", Value: "z"}, Type: "query"},
		},
	}
	r.URL = "{{baseUrl}}/e/:item_id/x?a=2&b=3"
	syncParams(r)
	got := []string{}
	for _, p := range r.Params {
		got = append(got, p.Type+":"+p.Name+"="+p.Value)
	}
	if strings.Join(got, " ") != "path:item_id= query:a=2 query:b=3 query:off=z" {
		t.Fatalf("params = %v", got)
	}
	if r.Params[1].Description != "kept" {
		t.Fatalf("description lost: %+v", r.Params[1])
	}
}

func TestEnvironmentSwitchAndEdit(t *testing.T) {
	h := newHarness(t)
	h.keys("e", "down", "down", "enter")
	h.contains("env staging")
	h.keys("e", "up", "enter")
	h.contains("env local")
	h.keys("E", "enter", "backspace", "backspace")
	h.typeText("81")
	h.keys("enter")
	h.contains("Environment local ●", "port      8081")
	h.keys("ctrl+s", "esc")
	c, err := collection.Load(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := c.Environment("local").Vars[0].Value; v != "8081" {
		t.Fatalf("saved port = %q", v)
	}
}

func TestRunReadsOnly(t *testing.T) {
	h := newHarness(t)
	h.keys("R", "r", "enter")
	h.contains("3/3 done", "3 passed", "0 failed")
	for _, s := range h.g.seen {
		if !strings.HasPrefix(s, "GET ") {
			t.Fatalf("a read run sent %s", s)
		}
	}
	h.keys("down", "enter")
	h.contains("Pets / GetPet")
}

func TestRunWithWritesAsksFirst(t *testing.T) {
	h := newHarness(t)
	h.keys("R", "enter")
	h.contains("Run requests that change data?", "2 of 5 requests")
	h.keys("n")
	if len(h.g.seen) != 0 {
		t.Fatalf("sent %v after declining", h.g.seen)
	}
}

func TestBodyEditor(t *testing.T) {
	h := newHarness(t)
	r := h.open("CreatePet")
	h.keys("2", "enter")
	h.contains("editing Body (json)", "✓ valid JSON", "esc done · ctrl+s save")
	// Enter on the first line indents the new line one level deeper than the brace.
	h.keys("end", "enter")
	h.typeText(`"extra": true,`)
	if !strings.HasPrefix(r.Body.Data, "{\n  \"extra\": true,\n  \"name\"") || !h.m.dirty[r] {
		t.Fatalf("body = %q dirty = %v", r.Body.Data, h.m.dirty[r])
	}
	h.keys("esc")
	if h.m.req.editor != nil || !strings.Contains(r.Body.Data, "extra") {
		t.Fatal("esc dropped the edit or kept the editor open")
	}
	h.contains("kept in memory")
	h.keys("enter", "ctrl+s")
	h.contains("saved Pets/CreatePet.yml")
	if b, _ := os.ReadFile(r.Path); !strings.Contains(string(b), "extra") {
		t.Fatalf("file lacks the edit:\n%s", b)
	}
}

func TestEditorIndentFormatUndo(t *testing.T) {
	h := newHarness(t)
	r := h.open("GetPet")
	h.keys("b")
	h.contains("Body type", "form-urlencoded")
	h.keys("down", "enter")
	if r.Body.Mode != "json" || h.m.req.editor == nil {
		t.Fatalf("mode %q editor %v", r.Body.Mode, h.m.req.editor != nil)
	}
	h.typeText(`"a": [`)
	h.keys("enter")
	h.typeText("1")
	h.keys("enter")
	h.typeText("]")
	want := "{\n  \"a\": [\n    1\n  ]\n}"
	if r.Body.Data != want {
		t.Fatalf("body = %q, want %q", r.Body.Data, want)
	}
	h.keys("ctrl+f")
	if r.Body.Data != "{\n  \"a\": [\n    1\n  ]\n}" {
		t.Fatalf("formatted = %q", r.Body.Data)
	}
	h.keys("ctrl+k")
	h.contains("invalid JSON")
	h.keys("ctrl+z")
	if r.Body.Data != want {
		t.Fatalf("undo gave %q", r.Body.Data)
	}
	h.keys("ctrl+y")
	if r.Body.Data == want {
		t.Fatal("redo did nothing")
	}
}

func TestFormatKeepsVariables(t *testing.T) {
	out, err := formatJSON(`{"id":"{{petId}}","n":{{count}},"list":[{{a}},{{a}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"id\": \"{{petId}}\",\n  \"n\": {{count}},\n  \"list\": [\n    {{a}},\n    {{a}}\n  ]\n}"
	if out != want {
		t.Fatalf("got %q", out)
	}
	if err := jsonProblem(`{"a": {{x}}, }`); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("problem = %v", err)
	}
}

func TestFilterAndQuit(t *testing.T) {
	h := newHarness(t)
	h.m.focus = focusTree
	h.keys("/")
	h.typeText("user")
	h.contains("ListUsers", "UpdateUser")
	for _, n := range h.m.tree.nodes {
		if n.req != nil && n.req.Name == "GetPet" {
			t.Fatal("filter kept GetPet")
		}
	}
	h.keys("enter")
	h.contains("Users / ListUsers")

	h.keys("m")
	h.contains("Method", "PATCH")
	h.keys("down", "enter")
	h.keys("q")
	h.contains("Unsaved changes", "Unsaved: 1 request(s)")
	h.keys("esc")
	if h.m.quitting {
		t.Fatal("esc quit")
	}
	h.keys("q", "n")
	if !h.m.quitting {
		t.Fatal("n did not quit")
	}
}

func TestSidebarFileOps(t *testing.T) {
	h := newHarness(t)
	h.m.focus = focusTree
	h.m.tree.focus(h.m.c.Root.Folders[1], nil)
	h.keys("a")
	h.typeText("Search users")
	h.keys("enter")
	h.contains("Users / Search users", "created Users/Search users.yml")
	r := h.m.current
	h.keys("u")
	h.do(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typeText("{{baseUrl}}/v1/users/search")
	h.keys("enter", "ctrl+s")
	if b, _ := os.ReadFile(filepath.Join(h.dir, "Users", "Search users.yml")); !strings.Contains(string(b), "/users/search") {
		t.Fatalf("saved file:\n%s", b)
	}

	h.m.focus = focusTree
	h.m.tree.focus(nil, r)
	h.keys("d", "y")
	if _, err := os.Stat(filepath.Join(h.dir, "Users", "Search users.yml")); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
	if h.m.current != nil || len(h.m.c.Root.Folders[1].Requests) != 2 {
		t.Fatalf("current %v, users has %d", h.m.current, len(h.m.c.Root.Folders[1].Requests))
	}
}

func TestMenuBarRunsActions(t *testing.T) {
	h := newHarness(t)
	h.open("GetPet")
	h.keys("f10")
	h.contains("New request…", "Save all", "Quit")
	h.keys("right")
	h.contains("Send", "Edit URL", "Copy as curl")
	// Send is the first enabled item of the Request menu.
	h.keys("enter")
	if len(h.g.seen) != 1 {
		t.Fatalf("seen %v", h.g.seen)
	}
	// A shortcut pressed while a menu is open does what it does anywhere.
	h.keys("f10", "?")
	h.contains("Actions", "type to search")
}

func TestPaletteFiltersAndRuns(t *testing.T) {
	h := newHarness(t)
	h.keys("?")
	h.contains("Actions", "File", "Save request", "ctrl+s", "Environment", "Choose environment…")
	h.typeText("choose env")
	h.keys("enter")
	h.contains("No environment", "local", "staging")
	h.keys("esc")
	if h.m.modal != nil {
		t.Fatalf("modal %T still open", h.m.modal)
	}
}

func TestMethodPickerFilters(t *testing.T) {
	h := newHarness(t)
	r := h.open("GetPet")
	h.keys("m")
	h.typeText("pa")
	h.keys("enter")
	if r.Method != "PATCH" || !h.m.dirty[r] {
		t.Fatalf("method %s dirty %v", r.Method, h.m.dirty[r])
	}
}

func TestHeaderFormSplitsPastedLine(t *testing.T) {
	h := newHarness(t)
	r := h.open("GetPet")
	h.keys("3", "a")
	h.contains("Add header", "Name", "Value")
	h.typeText("X-Debug: yes")
	h.keys("enter")
	last := r.Headers[len(r.Headers)-1]
	if last.Name != "X-Debug" || last.Value != "yes" || !last.Enabled {
		t.Fatalf("header = %+v", last)
	}
	h.contains("enter edit", "space on/off", "a add", "d delete")
}

func TestAssertionForm(t *testing.T) {
	h := newHarness(t)
	r := h.open("GetPet")
	n := len(r.Assertions)
	h.keys("7", "a", "tab", "right", "tab")
	h.do(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typeText("500")
	h.keys("enter")
	a := r.Assertions[n]
	if a.Expression != "res.status" || a.Operator != "neq" || a.Value != "500" {
		t.Fatalf("assertion = %+v", a)
	}
}

func TestMouse(t *testing.T) {
	h := newHarness(t)
	l := h.m.layout()
	// Second node of the tree: the first request under the first folder.
	h.click(l.tree.x+4, l.tree.y+2)
	if h.m.current != h.m.tree.nodes[1].req {
		t.Fatalf("click opened %v", h.m.current.Name)
	}
	h.screen()
	h.click(tabX(l.req.inner().x, h.m.req.names, int(tabHeaders)), l.req.inner().y+1)
	if h.m.req.tab != tabHeaders || h.m.focus != focusRequest {
		t.Fatalf("tab %d focus %d", h.m.req.tab, h.m.focus)
	}
	h.click(menuBar()[3].x+1, 0)
	h.contains("Run whole collection…")
	h.click(l.resp.x+5, l.resp.y+5)
	if h.m.modal != nil {
		t.Fatal("a click outside kept the menu open")
	}
	h.keys("s")
	h.click(l.resp.x+5, l.resp.y+5)
	h.wheel(l.resp.x+5, l.resp.y+5, 1)
	if h.m.focus != focusResponse || h.m.resp.scroll == 0 && len(h.m.resp.cache.lines) > l.resp.h {
		t.Fatalf("focus %d scroll %d", h.m.focus, h.m.resp.scroll)
	}
	// Clicking the method opens the method picker.
	h.click(l.req.inner().x+1, l.req.inner().y)
	h.contains("Method", "DELETE")
}

func TestZoomAndEditorLayout(t *testing.T) {
	h := newHarness(t)
	h.open("CreatePet")
	normal := h.m.layout().req.h
	h.keys("b")
	if h.m.layout().req.h <= normal {
		t.Fatal("the editor did not get more room")
	}
	h.keys("esc", "z")
	if l := h.m.layout(); l.tree.w != 0 || l.req.w != 130 {
		t.Fatalf("zoomed layout %+v", l)
	}
	h.keys("tab")
	if l := h.m.layout(); l.resp.w != 130 {
		t.Fatalf("zoom does not follow focus: %+v", l)
	}
	h.keys("z")
	if h.m.layout().tree.w == 0 {
		t.Fatal("unzoom kept the tree hidden")
	}
}

func TestResponseSearch(t *testing.T) {
	h := newHarness(t)
	h.open("ListPets")
	h.keys("s", "tab", "/")
	h.typeText("p-7")
	h.keys("enter")
	h.contains("/p-7 1/1", "1 matching line(s)")
	h.keys("n", "esc")
	if h.m.resp.query != "" {
		t.Fatal("esc kept the search")
	}
}

func TestRevertRequest(t *testing.T) {
	h := newHarness(t)
	r := h.open("GetPet")
	h.keys("m", "down", "enter")
	if r.Method == "GET" {
		t.Fatal("method did not change")
	}
	h.keys("?")
	h.typeText("revert")
	h.keys("enter", "y")
	if r.Method != "GET" || h.m.dirty[r] {
		t.Fatalf("method %s dirty %v", r.Method, h.m.dirty[r])
	}
}

func TestEveryActionHasAMenuAndKeysAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, a := range actions {
		if !slicesContains(menus, a.menu) {
			t.Fatalf("action %q has unknown menu %q", a.name, a.menu)
		}
		for _, k := range a.keys {
			id := fmt.Sprint(a.scope, k)
			// The same key may be reused in one scope only when the actions are never available together.
			if prev, ok := seen[id]; ok && !(k == "t" || k == "esc") {
				t.Fatalf("key %q in scope %d is bound to %q and %q", k, a.scope, prev, a.name)
			}
			seen[id] = a.name
		}
	}
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestProgramReadsTerminalInput feeds raw terminal bytes through a real Bubble Tea program: the
// key and SGR mouse sequences must reach the same handlers the model tests drive directly.
func TestProgramReadsTerminalInput(t *testing.T) {
	h := newHarness(t)
	l := h.m.layout()
	target := h.m.tree.nodes[3]
	if target.req == nil {
		t.Fatalf("node 3 is a folder: %s", target.folder.Name)
	}
	// SGR mouse coordinates are 1-based; press then release.
	click := func(x, y int) string { return fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+1, y+1) }
	in := strings.Join([]string{
		click(menuBar()[3].x+1, 0), // open the Run menu
		"\x1b[B", "\r",             // second item: Run whole collection…
		"q",                             // close the run setup
		click(l.tree.x+6, l.tree.y+1+3), // open the fourth sidebar line: CreatePet
		"\x10", "zoom", "\r",            // ctrl+p, filter, run
		"\x1b[21~", "\x1b[D", "\x1b[D", "\r", // F10 opens File, left twice reaches View, enter runs its first item
		"q",
	}, "")
	p := tea.NewProgram(h.m, tea.WithInput(strings.NewReader(in)), tea.WithOutput(io.Discard), tea.WithoutSignals())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if !h.m.quitting || h.m.current != target.req || !h.m.zoom {
		t.Fatalf("quitting %v current %v zoom %v", h.m.quitting, h.m.current.Name, h.m.zoom)
	}
	// View's first item is Next pane, so focus moved from the sidebar to the request.
	if h.m.focus != focusRequest {
		t.Fatalf("focus %d", h.m.focus)
	}
}

func TestLineFeedWorksAsEnter(t *testing.T) {
	h := newHarness(t)
	r := h.open("CreatePet")
	h.keys("b", "end")
	h.do(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.HasPrefix(r.Body.Data, "{\n  \n") {
		t.Fatalf("body = %q", r.Body.Data)
	}
}

func TestNoPanicOnEmptyLists(t *testing.T) {
	h := newHarness(t)
	h.m.focus = focusTree
	h.keys("/")
	h.typeText("zzzz")
	h.keys("enter", "G", "up", "down")
	h.screen()
	h.keys("/", "esc")
	// An environment emptied in the editor, then moved in.
	h.keys("E")
	for range len(h.m.s.Env.Vars) {
		h.keys("d")
	}
	h.keys("down", "j", "up", "enter", " ")
	h.screen()
	h.keys("esc")
	h.m.s.Env = nil
	h.m.current = nil
	h.keys("v", "down", "enter", "d")
	h.screen()
}

func TestSecretToggleKeepsValue(t *testing.T) {
	h := newHarness(t)
	h.keys("E", "t", "t")
	if v := h.m.s.Env.Vars[0]; v.Secret || v.Value != "8080" {
		t.Fatalf("after secret on and off: %+v", v)
	}
}

func TestBodyTypeNoneDoesNotLoop(t *testing.T) {
	h := newHarness(t)
	r := h.open("CreatePet")
	h.keys("b", "esc", "2", "t")
	h.typeText("none")
	h.keys("enter")
	if h.m.modal != nil || r.Body.Mode != "none" || h.m.req.editor != nil {
		t.Fatalf("modal %T mode %q editor %v", h.m.modal, r.Body.Mode, h.m.req.editor != nil)
	}
}

func TestMenuKeyIsNotTypedIntoTheEditor(t *testing.T) {
	h := newHarness(t)
	r := h.open("CreatePet")
	before := r.Body.Data
	h.keys("b", "f10", "x")
	if r.Body.Data != before {
		t.Fatalf("menu key reached the editor: %q", r.Body.Data)
	}
	// From the request pane a sidebar key is labelled with its pane.
	h.keys("esc", "f10")
	h.contains("a in sidebar")
}

func TestPaletteClickOutsideListDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.keys("?")
	h.screen()
	r := h.m.modalRect
	h.click(r.x+4, r.y+r.h-2) // the help line under the list
	if _, ok := h.m.modal.(*paletteModal); !ok {
		t.Fatalf("a click on the help line ran something: modal %T", h.m.modal)
	}
}

func TestDeletedRequestCannotBeReopenedFromRun(t *testing.T) {
	h := newHarness(t)
	h.keys("R", "r", "enter", "esc")
	first := h.m.lastRun.origin[h.m.lastRun.results[0].Request]
	h.m.focus = focusTree
	h.m.tree.focus(nil, first)
	h.keys("d", "y", "L", "enter")
	if h.m.current == first {
		t.Fatal("a deleted request was reopened")
	}
	h.contains("cannot open")
	h.keys("esc")
	// A single send keeps the last run available.
	h.open("ListUsers")
	h.keys("s", "L")
	if _, ok := h.m.modal.(*runModal); !ok {
		t.Fatalf("Show last run unavailable after a send: %T", h.m.modal)
	}
}

func TestEditorWideCharactersAndIndentedEnter(t *testing.T) {
	e := newEditor("t", "text", "日本語x\n  item", nil)
	e.row, e.col = 0, 3
	got := ansi.Strip(e.view(40, 4)[0])
	if !strings.Contains(got, "日本語x") {
		t.Fatalf("wide line drawn as %q", got)
	}
	e.click(e.gutter+6, 0) // the cell of "x"
	if e.col != 3 {
		t.Fatalf("click on x put the cursor at %d", e.col)
	}
	e.row, e.col = 1, 0
	e.key(tea.KeyMsg{Type: tea.KeyEnter})
	if e.text() != "日本語x\n\n  item" || e.row != 2 {
		t.Fatalf("enter at column 0 gave %q row %d", e.text(), e.row)
	}
}
