package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/leodeim/brucli/internal/script"
)

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5A4FCF", Dark: "#A99BFF"}
	colMuted  = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6E6E7A"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#56D364"}
	colErr    = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#FF7B72"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"}
	colInfo   = lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#79C0FF"}
	colSelBg  = lipgloss.AdaptiveColor{Light: "#5A4FCF", Dark: "#5B4BC4"}

	stTitle  = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stMuted  = lipgloss.NewStyle().Foreground(colMuted)
	stBold   = lipgloss.NewStyle().Bold(true)
	stOK     = lipgloss.NewStyle().Foreground(colOK)
	stErr    = lipgloss.NewStyle().Foreground(colErr)
	stWarn   = lipgloss.NewStyle().Foreground(colWarn)
	stInfo   = lipgloss.NewStyle().Foreground(colInfo)
	stCursor = lipgloss.NewStyle().Background(colSelBg).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	stTabOn  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(colAccent).Padding(0, 1)
	stTabOff = lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1)
	stMenu   = lipgloss.NewStyle().Padding(0, 1)
	stKey    = lipgloss.NewStyle().Foreground(colInfo)
	stCaret  = lipgloss.NewStyle().Reverse(true)
	stMatch  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(colWarn)

	methodStyles = map[string]lipgloss.Style{
		"GET":    stOK,
		"POST":   stInfo,
		"PUT":    stWarn,
		"PATCH":  stWarn,
		"DELETE": stErr,
	}
)

func methodTag(m string) string {
	st, ok := methodStyles[m]
	if !ok {
		st = stTitle
	}
	return st.Render(fmt.Sprintf("%-6s", m))
}

func statusStyle(code int) lipgloss.Style {
	switch {
	case code >= 500:
		return stErr
	case code >= 400:
		return stWarn
	case code >= 300:
		return stInfo
	}
	return stOK
}

func responseStyle(response *script.Response) lipgloss.Style {
	if response.Protocol == "grpc" && response.Status != 0 {
		return stErr
	}
	return statusStyle(response.Status)
}

func stripStyles(s string) string { return ansi.Strip(s) }

// fit cuts or pads s, which may hold ANSI codes, to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(clean(s), w, "…")
	if n := ansi.StringWidth(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// clean replaces tabs and carriage returns, which have no width of their own and would shift
// whatever is drawn after them.
func clean(s string) string {
	if !strings.ContainsAny(s, "\t\r") {
		return s
	}
	return strings.NewReplacer("\t", "  ", "\r", "").Replace(s)
}

// wrap breaks each line to w cells, keeping ANSI styles.
func wrap(lines []string, w int) []string {
	var out []string
	for _, l := range lines {
		l = clean(l)
		if ansi.StringWidth(l) <= w {
			out = append(out, l)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(l, w, true), "\n")...)
	}
	return out
}

func tabs(names []string, active int) string {
	var b strings.Builder
	for i, n := range names {
		if i == active {
			b.WriteString(stTabOn.Render(n))
		} else {
			b.WriteString(stTabOff.Render(n))
		}
	}
	return b.String()
}

func pretty(b []byte) string {
	if len(bytes.TrimSpace(b)) == 0 {
		return ""
	}
	var out bytes.Buffer
	if json.Indent(&out, b, "", "  ") != nil {
		return string(b)
	}
	return out.String()
}

type tint struct{ pre, post string }

func newTint(st lipgloss.Style) tint {
	pre, post, _ := strings.Cut(st.Render("\x00"), "\x00")
	return tint{pre, post}
}

func (t tint) wrap(s string) string { return t.pre + s + t.post }

// over applies t to the plain parts of s, which may already hold styled parts: the style is
// resumed after every reset inside s.
func (t tint) over(s string) string {
	if t.post == "" {
		return s
	}
	return t.pre + strings.ReplaceAll(s, t.post, t.post+t.pre) + t.post
}

var (
	tintKey = newTint(stInfo)
	tintStr = newTint(stOK)
	tintNum = newTint(stWarn)
	tintLit = newTint(stTitle)
	tintVar = newTint(stWarn.Bold(true))
	tintCom = newTint(stMuted)

	tintMuted  = newTint(stMuted)
	tintAccent = newTint(lipgloss.NewStyle().Foreground(colAccent))
	tintBold   = newTint(stBold)
)

// highlight colors one line of text in the given language: json, js, md, or plain text.
func highlight(lang, s string) string {
	switch lang {
	case "json":
		return highlightJSON(s)
	case "js":
		return highlightJS(s)
	case "md":
		if strings.HasPrefix(strings.TrimSpace(s), "#") {
			return tintLit.wrap(s)
		}
	}
	return markVars(s)
}

// highlightJSON colors keys, strings, numbers and literals of indented JSON, and {{variables}} inside it.
func highlightJSON(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			j := strings.Index(s[i:], "}}")
			if j < 0 {
				b.WriteString(s[i:])
				return b.String()
			}
			j += i + 2
			b.WriteString(tintVar.wrap(s[i:j]))
			i = j
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			k := j
			for k < len(s) && s[k] == ' ' {
				k++
			}
			if k < len(s) && s[k] == ':' {
				b.WriteString(tintKey.wrap(s[i:j]))
			} else {
				b.WriteString(tintStr.wrap(highlightVars(s[i:j], tintStr)))
			}
			i = j
		case c == '-' || c >= '0' && c <= '9':
			j := i + 1
			for j < len(s) && strings.IndexByte("0123456789.eE+-", s[j]) >= 0 {
				j++
			}
			b.WriteString(tintNum.wrap(s[i:j]))
			i = j
		case strings.HasPrefix(s[i:], "true"), strings.HasPrefix(s[i:], "null"):
			b.WriteString(tintLit.wrap(s[i : i+4]))
			i += 4
		case strings.HasPrefix(s[i:], "false"):
			b.WriteString(tintLit.wrap(s[i : i+5]))
			i += 5
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

var (
	jsKeywords = map[string]bool{
		"const": true, "let": true, "var": true, "function": true, "return": true, "if": true, "else": true,
		"for": true, "while": true, "do": true, "await": true, "async": true, "new": true, "true": true,
		"false": true, "null": true, "undefined": true, "typeof": true, "instanceof": true, "of": true,
		"in": true, "try": true, "catch": true, "finally": true, "throw": true, "break": true,
		"continue": true, "switch": true, "case": true, "default": true, "this": true, "delete": true,
	}
	jsGlobals = map[string]bool{"bru": true, "res": true, "req": true, "expect": true, "test": true, "console": true}
)

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// highlightJS colors one line of a script: comments, strings, numbers, keywords and Bruno's globals.
func highlightJS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case strings.HasPrefix(s[i:], "//"):
			b.WriteString(tintCom.wrap(s[i:]))
			return b.String()
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				b.WriteString(tintCom.wrap(s[i:]))
				return b.String()
			}
			j += i + 4
			b.WriteString(tintCom.wrap(s[i:j]))
			i = j
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			b.WriteString(tintStr.wrap(s[i:j]))
			i = j
		case c >= '0' && c <= '9' && (i == 0 || !isIdentByte(s[i-1])):
			j := i + 1
			for j < len(s) && (isIdentByte(s[j]) || s[j] == '.') {
				j++
			}
			b.WriteString(tintNum.wrap(s[i:j]))
			i = j
		case isIdentByte(c):
			j := i + 1
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			switch w := s[i:j]; {
			case jsKeywords[w]:
				b.WriteString(tintLit.wrap(w))
			case jsGlobals[w]:
				b.WriteString(tintKey.wrap(w))
			default:
				b.WriteString(w)
			}
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// highlightMatches marks every case-insensitive occurrence of q in the plain text of s.
func highlightMatches(s, q string) string {
	plain := stripStyles(s)
	lower, lq := strings.ToLower(plain), strings.ToLower(q)
	if len(lower) != len(plain) || len(lq) != len(q) {
		lower, lq = plain, q
	}
	if q == "" || !strings.Contains(lower, lq) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, lq)
		if i < 0 {
			b.WriteString(plain)
			return b.String()
		}
		b.WriteString(plain[:i])
		b.WriteString(stMatch.Render(plain[i : i+len(q)]))
		plain, lower = plain[i+len(q):], lower[i+len(q):]
	}
}

// highlightVars marks {{variables}} inside text already styled with base.
func highlightVars(s string, base tint) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "{{")
		j := strings.Index(s[max(i, 0):], "}}")
		if i < 0 || j < 0 {
			b.WriteString(s)
			return b.String()
		}
		j += i + 2
		b.WriteString(s[:i] + base.post + tintVar.wrap(s[i:j]) + base.pre)
		s = s[j:]
	}
}

// markVars styles {{variables}} in plain text.
func markVars(s string) string { return highlightVars(s, tint{}) }
