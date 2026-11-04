package tui

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/leodeim/brucli/internal/runner"
)

type respTab int

const (
	respBody respTab = iota
	respHeaders
	respTimeline
	respTests
	respConsole
)

var respTabNames = []string{"Body", "Headers", "Timeline", "Tests", "Console"}

// maxHighlight caps how much of a body is colored; the rest is shown plain.
const maxHighlight = 512 << 10

type responsePane struct {
	tab    respTab
	scroll int
	// names are the tab labels as last drawn, for clicks.
	names []string
	// query is the search; matches are the lines holding it and at the one jumped to last.
	query   string
	matches []int
	at      int
	cache   struct {
		res    *runner.Result
		tab    respTab
		width  int
		reveal bool
		query  string
		lines  []string
		hit    map[int]bool
	}
}

func (p *responsePane) switchTab(t respTab) {
	p.tab, p.scroll, p.at = t, 0, -1
}

func (p *responsePane) setQuery(q string) {
	p.query, p.at, p.matches = q, -1, nil
	p.cache.res = nil
}

// jump scrolls to the next (d > 0) or previous match.
func (p *responsePane) jump(d int) {
	n := len(p.matches)
	if n == 0 {
		return
	}
	if p.at < 0 {
		// The first jump goes to the first match at or below the top of the view.
		p.at = n - 1
		for i, l := range p.matches {
			if l >= p.scroll {
				p.at = (i - 1 + n) % n
				break
			}
		}
		if d < 0 {
			p.at = (p.at + 1) % n
		}
	}
	p.at = (p.at + d + n) % n
	p.scroll = max(0, p.matches[p.at]-2)
}

func (m *Model) searchResponse() tea.Cmd {
	m.focus = focusResponse
	return m.ask("search response", m.resp.query, func(q string) tea.Cmd {
		p := &m.resp
		p.setQuery(q)
		if q == "" {
			return nil
		}
		p.lines(m, m.results[m.current], m.layout().resp.inner().w)
		if len(p.matches) == 0 {
			m.setStatus(errors.New(q), "no match in %s for", respTabNames[p.tab])
			return nil
		}
		p.jump(1)
		m.setStatus(nil, "%d matching line(s) · n next · N previous · esc clears", len(p.matches))
		return nil
	})
}

func (p *responsePane) key(_ *Model, msg tea.KeyMsg) tea.Cmd {
	switch k := msg.String(); k {
	case "right", "l":
		p.switchTab((p.tab + 1) % respTab(len(respTabNames)))
	case "left", "h":
		p.switchTab((p.tab + respTab(len(respTabNames)) - 1) % respTab(len(respTabNames)))
	case "1", "2", "3", "4", "5":
		p.switchTab(respTab(k[0] - '1'))
	case "up", "k":
		p.scroll = max(0, p.scroll-1)
	case "down", "j":
		p.scroll++
	case "pgup":
		p.scroll = max(0, p.scroll-15)
	case "pgdown", " ":
		p.scroll += 15
	case "home", "g":
		p.scroll = 0
	case "end", "G":
		p.scroll = 1 << 30
	}
	return nil
}

func (p *responsePane) view(m *Model, w, h int) []string {
	res := m.results[m.current]
	names := slices.Clone(respTabNames)
	if res != nil && res.Response != nil {
		names[respHeaders] = fmt.Sprintf("Headers %d", len(res.Response.Headers))
	}
	if res != nil {
		if n := len(res.Assertions) + len(res.Tests); n > 0 {
			names[respTests] = "Tests " + checksCount(res)
		}
		if n := len(res.Logs) + len(res.ScriptErrors) + len(res.Warnings) + len(res.Unresolved); n > 0 {
			names[respConsole] = fmt.Sprintf("Console %d", n)
		}
	}
	p.names = names
	head := tabs(names, int(p.tab))
	content := p.lines(m, res, w)
	if p.query != "" {
		found := stErr.Render("no match")
		if n := len(p.matches); n > 0 {
			found = fmt.Sprintf("%d/%d", max(p.at, 0)+1, n)
		}
		head += "  " + stMuted.Render("/"+p.query+" ") + found
	}
	out := []string{head}
	body := h - 1
	p.scroll = max(0, min(p.scroll, len(content)-body))
	for i := p.scroll; i < len(content) && len(out) < h; i++ {
		l := content[i]
		if p.cache.hit[i] {
			l = highlightMatches(l, p.query)
		}
		out = append(out, l)
	}
	return out
}

func checksCount(res *runner.Result) string {
	passed := 0
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
	return fmt.Sprintf("%d/%d", passed, len(res.Assertions)+len(res.Tests))
}

func (p *responsePane) lines(m *Model, res *runner.Result, w int) []string {
	c := &p.cache
	if c.res == res && c.tab == p.tab && c.width == w && c.reveal == m.reveal && c.query == p.query && res != nil {
		return c.lines
	}
	var lines []string
	switch {
	case res == nil:
		lines = []string{stMuted.Render("Not sent yet. s sends it (ctrl+r while editing); R runs a folder.")}
	case p.tab == respBody:
		lines = bodyLines(res)
	case p.tab == respHeaders:
		lines = headerLines(res)
	case p.tab == respTimeline:
		lines = timelineLines(m, res)
	case p.tab == respTests:
		lines = testLines(res)
	default:
		lines = consoleLines(res)
	}
	lines = wrap(lines, w)
	p.matches, c.hit = nil, map[int]bool{}
	if q := strings.ToLower(p.query); q != "" {
		for i, l := range lines {
			if strings.Contains(strings.ToLower(stripStyles(l)), q) {
				p.matches = append(p.matches, i)
				c.hit[i] = true
			}
		}
	}
	if p.at >= len(p.matches) {
		p.at = -1
	}
	c.res, c.tab, c.width, c.reveal, c.query, c.lines = res, p.tab, w, m.reveal, p.query, lines
	return lines
}

func bodyLines(res *runner.Result) []string {
	switch {
	case res.Skipped:
		return []string{stMuted.Render("Skipped by a before-request script.")}
	case res.Err != nil:
		return []string{stErr.Render(res.Err.Error())}
	case len(res.Response.Body) == 0:
		return []string{stMuted.Render("(empty body)")}
	}
	text := pretty(res.Response.Body)
	if len(text) <= maxHighlight {
		text = highlightJSON(text)
	}
	return strings.Split(text, "\n")
}

func headerLines(res *runner.Result) []string {
	if res.Response == nil {
		return []string{stMuted.Render("No response.")}
	}
	var lines []string
	for _, k := range slices.Sorted(maps.Keys(res.Response.Headers)) {
		for _, v := range res.Response.Headers[k] {
			lines = append(lines, stInfo.Render(k)+": "+v)
		}
	}
	return lines
}

func timelineLines(m *Model, res *runner.Result) []string {
	if res.Sent == nil {
		return []string{stMuted.Render("Nothing was sent.")}
	}
	s := res.Sent
	if !m.reveal {
		s = m.s.Mask(res.Request, s)
	}
	lines := []string{stTitle.Render("Request"), methodTag(s.Method) + " " + s.URL}
	for _, h := range s.Headers {
		lines = append(lines, "  "+stInfo.Render(h.Name)+": "+h.Value)
	}
	if s.Body != "" {
		lines = append(lines, "")
		lines = append(lines, strings.Split(highlightJSON(pretty([]byte(s.Body))), "\n")...)
	}
	if r := res.Response; r != nil {
		lines = append(lines, "", stTitle.Render("Response"),
			statusStyle(r.Status).Render(fmt.Sprintf("%d %s", r.Status, r.StatusText))+stMuted.Render(fmt.Sprintf(" in %s from %s", elapsed(r.Duration), r.URL)))
	}
	lines = append(lines, "", stTitle.Render("curl")+stMuted.Render(" (c copies it with credentials)"))
	return append(lines, strings.Split(runner.Curl(s), "\n")...)
}

func testLines(res *runner.Result) []string {
	var lines []string
	if len(res.Assertions) > 0 {
		lines = append(lines, stTitle.Render("Assertions"))
	}
	for _, a := range res.Assertions {
		expr := fmt.Sprintf("%s %s %s", a.Assertion.Expression, a.Assertion.Operator, a.Assertion.Value)
		if a.Passed {
			lines = append(lines, stOK.Render("✓ ")+expr)
		} else {
			lines = append(lines, stErr.Render("✗ ")+expr+"  "+stErr.Render(a.Error))
		}
	}
	if len(res.Tests) > 0 {
		lines = append(lines, "", stTitle.Render("Tests"))
	}
	for _, t := range res.Tests {
		if t.Passed {
			lines = append(lines, stOK.Render("✓ ")+t.Name)
		} else {
			lines = append(lines, stErr.Render("✗ ")+t.Name+"  "+stErr.Render(t.Error))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, stMuted.Render("No assertions or tests on this request."))
	}
	return lines
}

func consoleLines(res *runner.Result) []string {
	var lines []string
	for _, e := range res.ScriptErrors {
		lines = append(lines, stErr.Render("✗ "+e))
	}
	for _, w := range res.Warnings {
		lines = append(lines, stWarn.Render("! "+w))
	}
	if len(res.Unresolved) > 0 {
		lines = append(lines, stWarn.Render("! sent with undefined variables: {{"+strings.Join(res.Unresolved, "}}, {{")+"}}"))
	}
	for _, set := range res.EnvSet {
		note := ""
		if set.Persist {
			note = stMuted.Render(" (saved)")
		}
		lines = append(lines, stInfo.Render("env ")+set.Name+" = "+set.Value+note)
	}
	for _, l := range res.Logs {
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		lines = append(lines, stMuted.Render("No console output."))
	}
	return lines
}
