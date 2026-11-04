package collection

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

var errUnsupported = errors.New("unsupported item type")

type rawCollection struct {
	Config Config `yaml:"config"`
	Info   struct {
		Name string `yaml:"name"`
	} `yaml:"info"`
	Request    rawDefaults `yaml:"request"`
	Docs       yaml.Node   `yaml:"docs"`
	Extensions struct {
		Bruno struct {
			Ignore []string `yaml:"ignore"`
		} `yaml:"bruno"`
	} `yaml:"extensions"`
}

type rawFolder struct {
	Info struct {
		Name string `yaml:"name"`
		Seq  int    `yaml:"seq"`
	} `yaml:"info"`
	Request *rawDefaults `yaml:"request"`
	Docs    yaml.Node    `yaml:"docs"`
}

type rawDefaults struct {
	Headers   []rawKV     `yaml:"headers"`
	Auth      yaml.Node   `yaml:"auth"`
	Variables []rawKV     `yaml:"variables"`
	Scripts   []rawScript `yaml:"scripts"`
}

func (d rawDefaults) defaults() Defaults {
	return Defaults{
		Headers: kvs(d.Headers),
		Auth:    decodeAuth(&d.Auth),
		Vars:    kvs(d.Variables),
		Scripts: decodeScripts(d.Scripts),
	}
}

type rawKV struct {
	Name        string    `yaml:"name"`
	Value       yaml.Node `yaml:"value"`
	Description yaml.Node `yaml:"description"`
	Disabled    bool      `yaml:"disabled"`
	Type        string    `yaml:"type"`
	ContentType string    `yaml:"contentType"`
	Secret      bool      `yaml:"secret"`
}

func (r rawKV) kv() KV {
	kv := KV{Name: r.Name, Type: r.Type, ContentType: r.ContentType, Description: docsText(&r.Description), Enabled: !r.Disabled, described: r.Description.Kind != 0}
	if r.Value.Kind == yaml.SequenceNode {
		var files []string
		_ = r.Value.Decode(&files)
		kv.Value = strings.Join(files, "\n")
		return kv
	}
	if r.Value.Kind == yaml.MappingNode {
		var typed struct {
			Data string `yaml:"data"`
		}
		_ = r.Value.Decode(&typed)
		kv.Value, kv.data, kv.typed = typed.Data, typed.Data, &r.Value
		return kv
	}
	kv.Value = r.Value.Value
	return kv
}

func kvs(raw []rawKV) []KV {
	out := make([]KV, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.kv())
	}
	return out
}

type rawScript struct {
	Type string `yaml:"type"`
	Code string `yaml:"code"`
}

func decodeScripts(raw []rawScript) Scripts {
	var s Scripts
	for _, r := range raw {
		switch r.Type {
		case "before-request":
			s.Before = r.Code
		case "after-response":
			s.After = r.Code
		case "tests":
			s.Tests = r.Code
		}
	}
	return s
}

type rawHTTP struct {
	Method  string    `yaml:"method"`
	URL     string    `yaml:"url"`
	Params  []rawKV   `yaml:"params"`
	Headers []rawKV   `yaml:"headers"`
	Body    yaml.Node `yaml:"body"`
	Auth    yaml.Node `yaml:"auth"`
}

type rawRequest struct {
	Info struct {
		Name string   `yaml:"name"`
		Type string   `yaml:"type"`
		Seq  int      `yaml:"seq"`
		Tags []string `yaml:"tags"`
	} `yaml:"info"`
	HTTP    rawHTTP `yaml:"http"`
	GraphQL rawHTTP `yaml:"graphql"`
	Runtime struct {
		Variables  []rawKV        `yaml:"variables"`
		Scripts    []rawScript    `yaml:"scripts"`
		Assertions []rawAssertion `yaml:"assertions"`
		Actions    []rawAction    `yaml:"actions"`
	} `yaml:"runtime"`
	Settings struct {
		EncodeURL       *bool `yaml:"encodeUrl"`
		Timeout         any   `yaml:"timeout"`
		FollowRedirects *bool `yaml:"followRedirects"`
		MaxRedirects    *int  `yaml:"maxRedirects"`
	} `yaml:"settings"`
	Docs yaml.Node `yaml:"docs"`
}

type rawAssertion struct {
	Expression  string    `yaml:"expression"`
	Operator    string    `yaml:"operator"`
	Value       string    `yaml:"value"`
	Description yaml.Node `yaml:"description"`
	Disabled    bool      `yaml:"disabled"`
}

type rawAction struct {
	Type     string `yaml:"type"`
	Selector struct {
		Expression string `yaml:"expression"`
	} `yaml:"selector"`
	Variable struct {
		Name  string `yaml:"name"`
		Scope string `yaml:"scope"`
	} `yaml:"variable"`
	Description yaml.Node `yaml:"description"`
	Disabled    bool      `yaml:"disabled"`
}

func readRequest(path string) (*Request, error) {
	doc, err := readYAML(path)
	if err != nil {
		return nil, err
	}
	r, err := requestFromNode(path, doc)
	if err == nil && filepath.Ext(path) == ".bru" {
		data, _ := os.ReadFile(path)
		r.bruSource = string(data)
	}
	return r, err
}

func requestFromNode(path string, doc *yaml.Node) (*Request, error) {
	var raw rawRequest
	if err := doc.Decode(&raw); err != nil {
		return nil, err
	}
	if raw.Info.Type != "http" && raw.Info.Type != "graphql" {
		return nil, fmt.Errorf("%w %q", errUnsupported, raw.Info.Type)
	}
	if raw.Info.Type == "graphql" {
		raw.HTTP = raw.GraphQL
	}
	r := &Request{
		Kind:    raw.Info.Type,
		Name:    raw.Info.Name,
		Seq:     raw.Info.Seq,
		Tags:    raw.Info.Tags,
		Method:  strings.ToUpper(cmp.Or(raw.HTTP.Method, "GET")),
		URL:     raw.HTTP.URL,
		Headers: kvs(raw.HTTP.Headers),
		Body:    decodeBody(&raw.HTTP.Body),
		Auth:    decodeAuth(&raw.HTTP.Auth),
		Vars:    kvs(raw.Runtime.Variables),
		Scripts: decodeScripts(raw.Runtime.Scripts),
		Docs:    docsText(&raw.Docs),
		Path:    path,
		doc:     doc,
	}
	if r.Kind == "graphql" {
		var body struct {
			Query         string `yaml:"query"`
			Variables     string `yaml:"variables"`
			OperationName string `yaml:"operationName"`
		}
		if err := raw.HTTP.Body.Decode(&body); err != nil {
			return nil, fmt.Errorf("GraphQL body: %w", err)
		}
		r.Body = Body{Mode: "graphql", Data: body.Query, Variables: body.Variables, OperationName: body.OperationName}
		r.Method = strings.ToUpper(cmp.Or(raw.HTTP.Method, "POST"))
	}
	for _, p := range raw.HTTP.Params {
		r.Params = append(r.Params, Param{KV: p.kv(), Type: cmp.Or(p.Type, "query")})
	}
	for _, a := range raw.Runtime.Assertions {
		r.Assertions = append(r.Assertions, Assertion{
			Expression: a.Expression, Operator: cmp.Or(a.Operator, "eq"), Value: a.Value,
			Description: docsText(&a.Description), Enabled: !a.Disabled,
		})
	}
	for _, a := range raw.Runtime.Actions {
		if a.Type != "set-variable" {
			continue
		}
		r.Actions = append(r.Actions, Action{
			Name: a.Variable.Name, Expression: a.Selector.Expression, Scope: cmp.Or(a.Variable.Scope, "runtime"),
			Description: docsText(&a.Description), Enabled: !a.Disabled,
		})
	}
	s := raw.Settings
	r.Settings = Settings{EncodeURL: true, FollowRedirects: true, MaxRedirects: 5}
	if s.EncodeURL != nil {
		r.Settings.EncodeURL = *s.EncodeURL
	}
	if s.FollowRedirects != nil {
		r.Settings.FollowRedirects = *s.FollowRedirects
	}
	if s.MaxRedirects != nil {
		r.Settings.MaxRedirects = *s.MaxRedirects
	}
	if ms, ok := s.Timeout.(int); ok {
		r.Settings.TimeoutMS = ms
	}
	return r, nil
}

func decodeAuth(n *yaml.Node) Auth {
	switch n.Kind {
	case 0:
		return Auth{Mode: "none"}
	case yaml.ScalarNode:
		return Auth{Mode: n.Value}
	}
	var raw struct {
		Type      string `yaml:"type"`
		Username  string `yaml:"username"`
		Password  string `yaml:"password"`
		Token     string `yaml:"token"`
		Key       string `yaml:"key"`
		Value     string `yaml:"value"`
		Placement string `yaml:"placement"`
	}
	_ = n.Decode(&raw)
	if raw.Placement == "queryparams" {
		raw.Placement = "query"
	}
	a := Auth{
		Mode: raw.Type, Username: raw.Username, Password: raw.Password, Token: raw.Token,
		Key: raw.Key, Value: raw.Value, Placement: raw.Placement,
	}
	if !a.Supported() {
		a.raw = n
	}
	return a
}

func decodeBody(n *yaml.Node) Body {
	if n.Kind != yaml.MappingNode {
		return Body{Mode: "none"}
	}
	var head struct {
		Type string    `yaml:"type"`
		Data yaml.Node `yaml:"data"`
	}
	_ = n.Decode(&head)
	b := Body{Mode: head.Type}
	switch b.Mode {
	case "json", "text", "xml", "sparql":
		b.Data = head.Data.Value
	case "form-urlencoded", "multipart-form":
		var form []rawKV
		_ = head.Data.Decode(&form)
		b.Form = kvs(form)
	default:
		b.raw = n
	}
	return b
}

func readEnvironment(path string) (*Environment, error) {
	doc, err := readYAML(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Name      string  `yaml:"name"`
		Variables []rawKV `yaml:"variables"`
	}
	if err := doc.Decode(&raw); err != nil {
		return nil, err
	}
	env := &Environment{Name: raw.Name, Path: path, doc: doc}
	if env.Name == "" {
		env.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if filepath.Ext(path) == ".bru" {
		data, _ := os.ReadFile(path)
		env.bruSource = string(data)
	}
	for _, v := range raw.Variables {
		env.Vars = append(env.Vars, EnvVar{KV: v.kv(), Secret: v.Secret})
	}
	return env, nil
}

// docsText reads a description or docs block: a plain string or Bruno's {content, type} object.
func docsText(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value
	case yaml.MappingNode:
		var d struct {
			Content string `yaml:"content"`
		}
		_ = n.Decode(&d)
		return d.Content
	}
	return ""
}

func readYAML(path string) (*yaml.Node, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if filepath.Ext(path) == ".bru" {
		return bruNode(path, b)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	return doc.Content[0], nil
}
