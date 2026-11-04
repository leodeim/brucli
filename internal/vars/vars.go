// Package vars resolves Bruno variables: precedence across scopes and {{name}} interpolation.
package vars

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Source names the scope a variable came from.
type Source string

// Scopes from lowest to highest precedence, plus process.env which is only reachable by its prefix.
const (
	Collection  Source = "collection"
	Environment Source = "environment"
	Folder      Source = "folder"
	Request     Source = "request"
	Runtime     Source = "runtime"
	Process     Source = "process.env"
)

const processPrefix = "process.env."

// Scope holds the variables visible to one request.
type Scope struct {
	Collection  map[string]string
	Environment map[string]string
	Folder      map[string]string
	Request     map[string]string
	// Runtime values come from scripts and keep their JS type until interpolated.
	Runtime map[string]any
	Process map[string]string
}

// Var is one resolved name, for display.
type Var struct {
	Name     string
	Raw      string
	Resolved string
	Source   Source
	// Shadowed lists lower scopes that also define the name.
	Shadowed []Source
}

// Lookup finds name in the highest scope that defines it. Like Bruno, a name that no scope
// defines may be a path into a runtime object: {{user.id}}, {{rows[0].id}}.
func (s *Scope) Lookup(name string) (string, Source, bool) {
	if key, ok := strings.CutPrefix(name, processPrefix); ok {
		v, ok := s.Process[key]
		return v, Process, ok
	}
	if v, ok := s.Runtime[name]; ok {
		return Format(v), Runtime, true
	}
	for _, l := range s.layers() {
		if v, ok := l.vars[name]; ok {
			return v, l.src, true
		}
	}
	if v, ok := s.path(name); ok {
		return Format(v), Runtime, true
	}
	return "", "", false
}

// path resolves a.b[0].c against the runtime variables.
func (s *Scope) path(name string) (any, bool) {
	keys := strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '[' || r == ']' })
	if len(keys) < 2 {
		return nil, false
	}
	v, ok := s.Runtime[keys[0]]
	for _, k := range keys[1:] {
		if !ok {
			return nil, false
		}
		switch c := v.(type) {
		case map[string]any:
			v, ok = c[k]
		case []any:
			i, err := strconv.Atoi(k)
			ok = err == nil && i >= 0 && i < len(c)
			if ok {
				v = c[i]
			}
		default:
			ok = false
		}
	}
	return v, ok
}

type layer struct {
	src  Source
	vars map[string]string
}

// layers lists the string scopes from highest to lowest precedence.
func (s *Scope) layers() []layer {
	return []layer{{Request, s.Request}, {Folder, s.Folder}, {Environment, s.Environment}, {Collection, s.Collection}}
}

// All lists every variable once, by the scope that wins, sorted by name.
func (s *Scope) All() []Var {
	byName := map[string]*Var{}
	note := func(name, raw string, src Source) {
		if v, ok := byName[name]; ok {
			v.Shadowed = append(v.Shadowed, src)
			return
		}
		byName[name] = &Var{Name: name, Raw: raw, Source: src}
	}
	for name, v := range s.Runtime {
		note(name, Format(v), Runtime)
	}
	for _, l := range s.layers() {
		for _, name := range slices.Sorted(maps.Keys(l.vars)) {
			note(name, l.vars[name], l.src)
		}
	}
	out := make([]Var, 0, len(byName))
	for _, v := range byName {
		v.Resolved = s.Interpolate(v.Raw)
		out = append(out, *v)
	}
	slices.SortFunc(out, func(a, b Var) int { return strings.Compare(a.Name, b.Name) })
	return out
}

var placeholder = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// interpolationBudget caps how many bytes one Interpolate call may produce, so variables that
// expand into each other several times over cannot exhaust memory.
const interpolationBudget = 4 << 20

// Interpolate replaces {{name}} like Bruno: recursively, leaving unknown names as written.
func (s *Scope) Interpolate(str string) string {
	if !strings.Contains(str, "{{") {
		return str
	}
	in := &interpolation{s: s, visiting: map[string]bool{}, done: map[string]string{}, budget: interpolationBudget}
	return in.replace(mock(str))
}

type interpolation struct {
	s        *Scope
	visiting map[string]bool
	// done holds each name's expansion, so a name used many times is expanded once.
	done   map[string]string
	budget int
}

func (in *interpolation) replace(str string) string {
	return placeholder.ReplaceAllStringFunc(str, func(m string) string {
		name := m[2 : len(m)-2]
		if v, ok := in.done[name]; ok && in.budget >= len(v) {
			in.budget -= len(v)
			return v
		}
		v, _, ok := in.s.Lookup(name)
		if !ok || in.visiting[name] || in.budget < len(v) {
			return m
		}
		if strings.Contains(v, "{{") {
			in.visiting[name] = true
			v = in.replace(mock(v))
			delete(in.visiting, name)
		}
		in.done[name] = v
		in.budget -= len(v)
		return v
	})
}

// Unresolved lists the {{names}} left in str after interpolation.
func (s *Scope) Unresolved(str string) []string {
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(s.Interpolate(str), -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// Format renders a script value the way it appears when interpolated.
func Format(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err == nil {
		return strings.TrimSuffix(b.String(), "\n")
	}
	// Not JSON, e.g. a cyclic object: fmt would recurse forever on it.
	return fmt.Sprintf("[%T]", v)
}

var mockPattern = regexp.MustCompile(`\{\{\$(\w+)\}\}`)

// mocks is the subset of Bruno's {{$name}} dynamic values that brucli generates.
var mocks = map[string]func() string{
	"guid":         uuid,
	"randomUUID":   uuid,
	"timestamp":    func() string { return strconv.FormatInt(time.Now().Unix(), 10) },
	"isoTimestamp": func() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") },
	"randomInt": func() string {
		n, _ := rand.Int(rand.Reader, big.NewInt(1000))
		return n.String()
	},
}

func mock(str string) string {
	if !strings.Contains(str, "{{$") {
		return str
	}
	return mockPattern.ReplaceAllStringFunc(str, func(m string) string {
		if f, ok := mocks[m[3:len(m)-2]]; ok {
			return f()
		}
		return m
	})
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
