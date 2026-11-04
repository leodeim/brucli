// Package collection reads and writes Bruno collections in YAML and .bru formats.
package collection

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	collectionFile = "opencollection.yml"
	folderFile     = "folder.yml"
	envDir         = "environments"
	dotEnvFile     = ".env"
)

// ErrNotCollection reports a directory without a Bruno collection manifest.
var ErrNotCollection = errors.New("not a Bruno collection: no opencollection.yml or bruno.json")

// Collection is a loaded collection: its folder tree, environments and the .env next to it.
type Collection struct {
	Name string
	Dir  string
	// Root is the collection itself: its Defaults are the collection-level headers, auth, variables and scripts.
	Root               *Folder
	Docs               string
	Environments       []*Environment
	GlobalEnvironments []*Environment
	WorkspaceDir       string
	Config             Config
	// DotEnv is the collection's .env file, exposed to requests as {{process.env.NAME}}.
	DotEnv map[string]string
	// Warnings lists files that were skipped while loading.
	Warnings []string
}

// Folder is a directory of requests; folder.yml may add defaults that its requests inherit.
type Folder struct {
	format   string
	Name     string
	Seq      int
	Dir      string
	Defaults Defaults
	Docs     string
	Parent   *Folder
	Folders  []*Folder
	Requests []*Request
}

// Defaults are the request settings a collection or folder passes down.
type Defaults struct {
	Headers []KV
	Auth    Auth
	Vars    []KV
	Scripts Scripts
}

// KV is a named value that can be switched off: a header, a variable, a form field.
type KV struct {
	Name        string
	Value       string
	Type        string
	ContentType string
	Description string
	Enabled     bool
	// typed keeps a {type, data} value as written, so saving an unchanged typed variable keeps its type.
	typed *yaml.Node
	data  string
	// described records a description key that was present, even empty: Bruno writes description: "" for env vars.
	described bool
}

// Param is a query or path parameter.
type Param struct {
	KV
	// Type is "query" or "path".
	Type string
}

// Auth is a request's or folder's authentication. Mode "inherit" defers to the parent.
type Auth struct {
	Options map[string]string
	// Mode is inherit, none, basic, bearer, apikey, or another Bruno mode brucli cannot send (kept as written).
	Mode     string
	Username string
	Password string
	Token    string
	Key      string
	Value    string
	// Placement is where an apikey goes: "header" (default) or "query", as Bruno writes it.
	Placement string
	raw       *yaml.Node
}

// Supported reports whether brucli can apply this auth mode.
func (a Auth) Supported() bool {
	switch a.Mode {
	case "inherit", "none", "basic", "bearer", "apikey", "digest", "ntlm", "awsv4", "oauth1", "oauth2", "wsse", "akamai-edgegrid":
		return true
	}
	return false
}

// Option accepts both YAML's camelCase fields and the legacy .bru snake_case fields.
func (a Auth) Option(names ...string) string {
	for _, name := range names {
		if value, ok := a.Options[name]; ok {
			return value
		}
	}
	normalize := func(value string) string { return strings.ToLower(strings.ReplaceAll(value, "_", "")) }
	for _, name := range names {
		for key, value := range a.Options {
			if normalize(key) == normalize(name) {
				return value
			}
		}
	}
	return ""
}

// Body is a request body. Raw modes keep Data; form modes keep Form.
type Body struct {
	// Mode is none, json, text, xml, sparql, form-urlencoded, multipart-form, or another Bruno mode kept as written.
	Mode          string
	Data          string
	Form          []KV
	Variables     string
	OperationName string
	Messages      []Message
	raw           *yaml.Node
}

type Message struct {
	Title    string
	Type     string
	Data     string
	Selected bool
}

// Scripts are the JavaScript hooks of a request, folder or collection.
type Scripts struct {
	Before string
	After  string
	Tests  string
}

// Assertion checks Expression (a JS expression over res) with Operator against Value.
type Assertion struct {
	Expression  string
	Operator    string
	Value       string
	Description string
	Enabled     bool
}

// Action is a post-response variable: Expression is evaluated against the response and stored as Name.
type Action struct {
	Name       string
	Expression string
	// Scope is "runtime" or "request".
	Scope       string
	Description string
	Enabled     bool
}

// Settings are per-request transport options.
type Settings struct {
	EncodeURL           bool
	TimeoutMS           int
	FollowRedirects     bool
	MaxRedirects        int
	KeepAliveIntervalMS int
}

// Request is one HTTP request file.
type Request struct {
	Kind          string
	Name          string
	Seq           int
	Tags          []string
	Method        string
	MethodType    string
	ProtoFilePath string
	URL           string
	Params        []Param
	Headers       []KV
	Body          Body
	Auth          Auth
	Vars          []KV
	Actions       []Action
	Scripts       Scripts
	Assertions    []Assertion
	Settings      Settings
	Docs          string

	Path      string
	Folder    *Folder
	doc       *yaml.Node
	bruSource string
}

// Chain returns the folders a request inherits from, collection root first.
func (r *Request) Chain() []*Folder {
	var chain []*Folder
	for f := r.Folder; f != nil; f = f.Parent {
		chain = append(chain, f)
	}
	slices.Reverse(chain)
	return chain
}

// Clone copies the request so it can be sent while the original is edited; it shares the file and folder.
func (r *Request) Clone() *Request {
	c := *r
	c.Tags = slices.Clone(r.Tags)
	c.Params = slices.Clone(r.Params)
	c.Headers = slices.Clone(r.Headers)
	c.Vars = slices.Clone(r.Vars)
	c.Actions = slices.Clone(r.Actions)
	c.Assertions = slices.Clone(r.Assertions)
	c.Body.Form = slices.Clone(r.Body.Form)
	c.Body.Messages = slices.Clone(r.Body.Messages)
	c.Auth.Options = maps.Clone(r.Auth.Options)
	return &c
}

func (r *Request) MethodLabel() string {
	switch r.Kind {
	case "grpc":
		return "gRPC"
	case "websocket":
		return "WS"
	}
	return r.Method
}

// HasTag reports whether the request carries tag.
func (r *Request) HasTag(tag string) bool { return slices.Contains(r.Tags, tag) }

// Environment is one file in environments/.
type Environment struct {
	Name      string
	Vars      []EnvVar
	Path      string
	doc       *yaml.Node
	bruSource string
}

// Clone copies the environment's variables; the copy saves to the same file.
func (e *Environment) Clone() *Environment {
	c := *e
	c.Vars = slices.Clone(e.Vars)
	if e.doc != nil {
		c.doc = deepCopy(e.doc)
	}
	return &c
}

// EnvVar is an environment variable. A secret's value is never read from or written to the file.
type EnvVar struct {
	KV
	Secret bool
}

// Environment returns the environment called name, or nil.
func (c *Collection) Environment(name string) *Environment {
	for _, e := range c.Environments {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// AllRequests lists every request of the tree under f in display order.
func (f *Folder) AllRequests() []*Request {
	var out []*Request
	for _, sub := range f.Folders {
		out = append(out, sub.AllRequests()...)
	}
	return append(out, f.Requests...)
}

// Load reads the collection in dir.
func Load(dir string) (*Collection, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	doc, err := readYAML(filepath.Join(dir, collectionFile))
	var raw rawCollection
	format := ".yml"
	if errors.Is(err, os.ErrNotExist) {
		raw, err = readBruCollection(dir)
		format = ".bru"
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", dir, ErrNotCollection)
		}
	} else if err == nil {
		err = doc.Decode(&raw)
	}
	if err != nil {
		return nil, err
	}
	c := &Collection{
		Name:   cmp.Or(raw.Info.Name, filepath.Base(dir)),
		Dir:    dir,
		Docs:   docsText(&raw.Docs),
		Config: raw.Config,
	}
	c.Root = &Folder{Name: c.Name, Dir: dir, Defaults: raw.Request.defaults(), Docs: c.Docs, format: format}

	ignore := map[string]bool{"node_modules": true, ".git": true, envDir: true}
	for _, name := range raw.Extensions.Bruno.Ignore {
		ignore[name] = true
	}
	if err := c.loadFolder(c.Root, ignore); err != nil {
		return nil, err
	}
	if err := c.loadEnvironments(); err != nil {
		return nil, err
	}
	if err := c.loadGlobalEnvironments(); err != nil {
		return nil, err
	}
	c.DotEnv, err = readDotEnv(filepath.Join(dir, dotEnvFile))
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Collection) loadFolder(f *Folder, ignore map[string]bool) error {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(f.Dir, name)
		switch {
		case strings.HasPrefix(name, "."), f == c.Root && ignore[name]:
		case e.IsDir():
			sub, err := c.readFolder(path, f)
			if err != nil {
				return err
			}
			if err := c.loadFolder(sub, ignore); err != nil {
				return err
			}
			f.Folders = append(f.Folders, sub)
		case name == folderFile, name == "folder.bru", f == c.Root && (name == collectionFile || name == "collection.bru"), !slices.Contains([]string{".yml", ".yaml", ".bru"}, filepath.Ext(name)):
		default:
			// Like Bruno, a file that cannot be read is skipped with a warning; the rest still loads.
			r, err := readRequest(path)
			if err != nil {
				c.Warnings = append(c.Warnings, fmt.Sprintf("%s: %v", c.rel(path), err))
				continue
			}
			r.Folder = f
			f.Requests = append(f.Requests, r)
		}
	}
	f.Folders = sortItems(f.Folders, func(x *Folder) (string, int) { return x.Name, x.Seq })
	f.Requests = sortItems(f.Requests, func(x *Request) (string, int) { return x.Name, x.Seq })
	return nil
}

func (c *Collection) readFolder(dir string, parent *Folder) (*Folder, error) {
	f := &Folder{Name: filepath.Base(dir), Dir: dir, Parent: parent, Defaults: Defaults{Auth: Auth{Mode: "inherit"}}, format: parent.format}
	name := "folder" + parent.format
	doc, err := readYAML(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	var raw rawFolder
	if err := doc.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", c.rel(filepath.Join(dir, folderFile)), err)
	}
	f.Name = cmp.Or(raw.Info.Name, f.Name)
	f.Seq = raw.Info.Seq
	f.Docs = docsText(&raw.Docs)
	if raw.Request != nil {
		f.Defaults = raw.Request.defaults()
	}
	return f, nil
}

func (c *Collection) loadEnvironments() error {
	entries, err := os.ReadDir(filepath.Join(c.Dir, envDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !slices.Contains([]string{".yml", ".yaml", ".bru"}, filepath.Ext(e.Name())) {
			continue
		}
		env, err := readEnvironment(filepath.Join(c.Dir, envDir, e.Name()))
		if err != nil {
			return fmt.Errorf("%s: %w", c.rel(filepath.Join(c.Dir, envDir, e.Name())), err)
		}
		c.Environments = append(c.Environments, env)
	}
	slices.SortFunc(c.Environments, func(a, b *Environment) int { return strings.Compare(a.Name, b.Name) })
	return nil
}

// rel returns path relative to the collection directory.
func (c *Collection) rel(path string) string {
	if r, err := filepath.Rel(c.Dir, path); err == nil {
		return r
	}
	return path
}

// sortItems orders like Bruno: alphabetical, then each item with a valid seq moved to position seq.
func sortItems[T any](items []T, key func(T) (string, int)) []T {
	var withSeq, out []T
	for _, it := range items {
		if _, seq := key(it); seq > 0 {
			withSeq = append(withSeq, it)
		} else {
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b T) int {
		an, _ := key(a)
		bn, _ := key(b)
		return strings.Compare(an, bn)
	})
	slices.SortStableFunc(withSeq, func(a, b T) int {
		an, as := key(a)
		bn, bs := key(b)
		return cmp.Or(cmp.Compare(as, bs), strings.Compare(an, bn))
	})
	for _, it := range withSeq {
		_, seq := key(it)
		out = slices.Insert(out, min(seq-1, len(out)), it)
	}
	return out
}
