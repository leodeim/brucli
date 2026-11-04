package collection

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Key orders Bruno writes; a key brucli adds goes before the first existing key that follows it here.
var (
	requestKeys = []string{"info", "http", "graphql", "grpc", "websocket", "runtime", "settings", "examples", "docs"}
	infoKeys    = []string{"name", "type", "seq", "tags"}
	httpKeys    = []string{"method", "url", "params", "headers", "body", "auth"}
	runtimeKeys = []string{"variables", "scripts", "assertions", "actions"}
	envKeys     = []string{"name", "color", "variables"}
	// blockKeys get a blank line before them at the top level, as Bruno's stringifyYml does.
	blockKeys = []string{"info", "http", "graphql", "grpc", "websocket", "mock", "routes", "runtime", "settings", "examples", "docs", "items", "request"}
)

// fresh returns the file's current YAML, or cached when it cannot be read; saving into it keeps
// what Bruno or git changed in keys brucli does not model since the file was loaded.
func fresh(path string, cached *yaml.Node) *yaml.Node {
	if doc, err := readYAML(path); err == nil {
		return doc
	}
	return deepCopy(cached)
}

// Save writes the request back to its file. Keys brucli does not model stay as they are on disk.
func (r *Request) Save() error {
	if filepath.Ext(r.Path) == ".bru" {
		return r.saveBru()
	}
	r.doc = fresh(r.Path, r.doc)
	set(child(r.doc, "info", requestKeys), "name", scalar(r.Name), infoKeys)
	kind := r.Kind
	if kind == "" {
		kind = "http"
	}
	info := child(r.doc, "info", requestKeys)
	if i := lookup(info, "type"); i >= 0 {
		old := info.Content[i+1].Value
		if old != kind && (old == "http" || old == "graphql") {
			set(r.doc, old, nil, requestKeys)
		}
	}
	set(child(r.doc, "info", requestKeys), "type", scalar(kind), infoKeys)
	http := child(r.doc, kind, requestKeys)
	set(http, "method", scalar(r.Method), httpKeys)
	set(http, "url", scalar(r.URL), httpKeys)
	set(http, "params", paramsNode(r.Params), httpKeys)
	set(http, "headers", kvsNode(r.Headers), httpKeys)
	set(http, "body", bodyNode(r.Body), httpKeys)
	set(http, "auth", authNode(r.Auth), httpKeys)

	runtime := child(r.doc, "runtime", requestKeys)
	set(runtime, "variables", kvsNode(r.Vars), runtimeKeys)
	set(runtime, "scripts", scriptsNode(r.Scripts), runtimeKeys)
	set(runtime, "assertions", assertionsNode(r.Assertions), runtimeKeys)
	set(runtime, "actions", actionsNode(r.Actions), runtimeKeys)
	if len(runtime.Content) == 0 {
		set(r.doc, "runtime", nil, requestKeys)
	}
	set(r.doc, "docs", optional(r.Docs), requestKeys)
	return writeYAML(r.Path, r.doc)
}

// Save writes the environment back to its file. A secret's value is never written.
func (e *Environment) Save() error {
	if filepath.Ext(e.Path) == ".bru" {
		return e.saveBru()
	}
	e.doc = fresh(e.Path, e.doc)
	set(e.doc, "name", scalar(e.Name), envKeys)
	var items []*yaml.Node
	for _, v := range e.Vars {
		n := mapping()
		if v.Secret {
			add(n, "secret", boolean(true))
			add(n, "name", scalar(v.Name))
		} else {
			add(n, "name", scalar(v.Name))
			add(n, "value", valueNode(v.KV))
		}
		if !v.Enabled {
			add(n, "disabled", boolean(true))
		}
		if v.Description != "" || v.described {
			add(n, "description", scalar(v.Description))
		}
		items = append(items, n)
	}
	set(e.doc, "variables", sequence(items), envKeys)
	return writeYAML(e.Path, e.doc)
}

// Persist writes one variable's value to the environment file as it is on disk, leaving every
// other variable as the file has it; a secret variable's value is never written.
func (e *Environment) Persist(name, value string) error {
	disk, err := readEnvironment(e.Path)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(disk.Vars, func(v EnvVar) bool { return v.Name == name })
	switch {
	case i < 0:
		disk.Vars = append(disk.Vars, EnvVar{KV: KV{Name: name, Value: value, Enabled: true}})
	case disk.Vars[i].Secret:
		return nil
	default:
		disk.Vars[i].Value = value
	}
	return disk.Save()
}

func paramsNode(params []Param) *yaml.Node {
	var items []*yaml.Node
	for _, p := range params {
		n := mapping()
		add(n, "name", scalar(p.Name))
		add(n, "value", valueNode(p.KV))
		add(n, "type", scalar(p.Type))
		describe(n, p.KV)
		items = append(items, n)
	}
	return sequence(items)
}

func kvsNode(kvs []KV) *yaml.Node {
	var items []*yaml.Node
	for _, kv := range kvs {
		n := mapping()
		add(n, "name", scalar(kv.Name))
		if kv.Type == "file" || kv.Type == "text" {
			add(n, "type", scalar(kv.Type))
		}
		add(n, "value", valueNode(kv))
		if kv.ContentType != "" {
			add(n, "contentType", scalar(kv.ContentType))
		}
		describe(n, kv)
		items = append(items, n)
	}
	return sequence(items)
}

func describe(n *yaml.Node, kv KV) {
	if kv.Description != "" {
		add(n, "description", scalar(kv.Description))
	}
	if !kv.Enabled {
		add(n, "disabled", boolean(true))
	}
}

func valueNode(kv KV) *yaml.Node {
	if kv.Type == "file" {
		var files []*yaml.Node
		for _, path := range strings.Split(kv.Value, "\n") {
			if path != "" {
				files = append(files, scalar(path))
			}
		}
		return sequence(files)
	}
	switch {
	case kv.typed == nil:
		return scalar(kv.Value)
	case kv.Value == kv.data:
		return kv.typed
	}
	n := deepCopy(kv.typed)
	set(n, "data", scalar(kv.Value), []string{"type", "data"})
	return n
}

func bodyNode(b Body) *yaml.Node {
	if b.raw != nil {
		return b.raw
	}
	switch b.Mode {
	case "graphql":
		n := mapping()
		add(n, "query", scalar(b.Data))
		add(n, "variables", scalar(b.Variables))
		if b.OperationName != "" {
			add(n, "operationName", scalar(b.OperationName))
		}
		return n
	case "", "none":
		return nil
	case "form-urlencoded", "multipart-form":
		n := mapping()
		add(n, "type", scalar(b.Mode))
		if len(b.Form) > 0 {
			add(n, "data", kvsNode(b.Form))
		}
		return n
	}
	n := mapping()
	add(n, "type", scalar(b.Mode))
	add(n, "data", scalar(b.Data))
	return n
}

func authNode(a Auth) *yaml.Node {
	if a.raw != nil {
		return a.raw
	}
	n := mapping()
	switch a.Mode {
	case "", "none":
		return nil
	case "inherit":
		return scalar("inherit")
	case "basic":
		add(n, "type", scalar("basic"))
		add(n, "username", scalar(a.Username))
		add(n, "password", scalar(a.Password))
	case "bearer":
		add(n, "type", scalar("bearer"))
		add(n, "token", scalar(a.Token))
	case "apikey":
		add(n, "type", scalar("apikey"))
		add(n, "key", scalar(a.Key))
		add(n, "value", scalar(a.Value))
		if a.Placement != "" {
			add(n, "placement", scalar(a.Placement))
		}
	}
	return n
}

func scriptsNode(s Scripts) *yaml.Node {
	var items []*yaml.Node
	for _, sc := range []struct{ typ, code string }{
		{"before-request", s.Before}, {"after-response", s.After}, {"tests", s.Tests},
	} {
		code := strings.TrimSpace(sc.code)
		if code == "" {
			continue
		}
		n := mapping()
		add(n, "type", scalar(sc.typ))
		add(n, "code", scalar(code))
		items = append(items, n)
	}
	return sequence(items)
}

func assertionsNode(as []Assertion) *yaml.Node {
	var items []*yaml.Node
	for _, a := range as {
		n := mapping()
		add(n, "expression", scalar(a.Expression))
		add(n, "operator", scalar(a.Operator))
		if a.Value != "" {
			add(n, "value", scalar(a.Value))
		}
		if a.Description != "" {
			add(n, "description", scalar(a.Description))
		}
		if !a.Enabled {
			add(n, "disabled", boolean(true))
		}
		items = append(items, n)
	}
	return sequence(items)
}

func actionsNode(as []Action) *yaml.Node {
	var items []*yaml.Node
	for _, a := range as {
		sel, v := mapping(), mapping()
		add(sel, "expression", scalar(a.Expression))
		add(sel, "method", scalar("jsonq"))
		add(v, "name", scalar(a.Name))
		add(v, "scope", scalar(a.Scope))
		n := mapping()
		add(n, "type", scalar("set-variable"))
		add(n, "phase", scalar("after-response"))
		add(n, "selector", sel)
		add(n, "variable", v)
		if a.Description != "" {
			add(n, "description", scalar(a.Description))
		}
		if !a.Enabled {
			add(n, "disabled", boolean(true))
		}
		items = append(items, n)
	}
	return sequence(items)
}

func mapping() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }

// sequence returns nil for no items, so set drops the key the way Bruno omits empty lists.
func sequence(items []*yaml.Node) *yaml.Node {
	if len(items) == 0 {
		return nil
	}
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

func optional(s string) *yaml.Node {
	if s == "" {
		return nil
	}
	return scalar(s)
}

func boolean(b bool) *yaml.Node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

// scalar styles a string like Bruno's writer: plain when YAML allows it, else double-quoted, block when multi-line.
func scalar(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	switch {
	case strings.Contains(s, "\n"):
		n.Style = yaml.LiteralStyle
	case needsQuotes(s):
		n.Style = yaml.DoubleQuotedStyle
	default:
		// Untagged, so yaml.v3 does not quote words that only YAML 1.1 reads as non-strings.
		n.Tag = ""
	}
	return n
}

// core12 matches plain scalars the YAML 1.2 core schema reads as null, bool, int or float.
var core12 = regexp.MustCompile(`^(?:~|null|Null|NULL|true|True|TRUE|false|False|FALSE|[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+|[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.nan|\.NaN|\.NAN)$`)

// yaml11 matches words yaml.v3 quotes for YAML 1.1 (yes/no, base 60, timestamps, 1_000, 0b1)
// that Bruno's YAML 1.2 writer leaves plain.
var yaml11 = regexp.MustCompile(`^(?:[yYnN]|[yY]es|YES|[nN]o|NO|[oO]n|ON|[oO]ff|OFF|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+(?:\.[0-9_]*)?|[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[Tt ][0-9:.]+(?:Z|[-+][0-9:]+)?)?|[-+]?[0-9][0-9_]*_[0-9_]*|0b[01_]+)$`)

// needsQuotes reports a string that a plain YAML 1.2 scalar cannot hold as a string.
func needsQuotes(s string) bool {
	if s == "" || core12.MatchString(s) {
		return true
	}
	if yaml11.MatchString(s) {
		return false
	}
	out, err := yaml.Marshal(s)
	return err != nil || len(out) == 0 || out[0] == '"' || out[0] == '\''
}

func add(m *yaml.Node, key string, v *yaml.Node) {
	if v == nil {
		return
	}
	m.Content = append(m.Content, scalar(key), v)
}

func lookup(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// child returns the mapping under key, adding an empty one in canonical order when missing.
func child(m *yaml.Node, key string, order []string) *yaml.Node {
	if i := lookup(m, key); i >= 0 && m.Content[i+1].Kind == yaml.MappingNode {
		return m.Content[i+1]
	}
	c := mapping()
	set(m, key, c, order)
	return c
}

// set replaces key's value, inserts it in canonical order, or deletes it when v is nil.
func set(m *yaml.Node, key string, v *yaml.Node, order []string) {
	i := lookup(m, key)
	switch {
	case v == nil && i >= 0:
		m.Content = slices.Delete(m.Content, i, i+2)
	case v == nil:
	case i >= 0:
		m.Content[i+1] = v
	default:
		at := len(m.Content)
		if rank := slices.Index(order, key); rank >= 0 {
			for j := 0; j+1 < len(m.Content); j += 2 {
				if r := slices.Index(order, m.Content[j].Value); r > rank {
					at = j
					break
				}
			}
		}
		m.Content = slices.Insert(m.Content, at, scalar(key), v)
	}
}

func writeYAML(path string, root *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	lines := strings.Split(buf.String(), "\n")
	out := make([]string, 0, len(lines)+8)
	for i, line := range lines {
		key, _, _ := strings.Cut(line, ":")
		if i > 0 && line != "" && line[0] != ' ' && slices.Contains(blockKeys, key) && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, line)
	}
	return writeAtomic(path, []byte(strings.Join(out, "\n")))
}

func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
