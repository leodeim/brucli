// Package script runs a request's JavaScript the way Bruno does: scripts, tests, assertions and
// post-response variables, against Bruno's req, res and bru objects.
package script

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/vars"
)

//go:embed prelude.js
var prelude string

// DefaultTimeout bounds one script run; a runaway loop is interrupted.
const DefaultTimeout = 10 * time.Second

// Header is one request header, in order and with its case.
type Header struct {
	Name  string
	Value string
}

// Request is the request as scripts see and change it.
type Request struct {
	Name    string
	Method  string
	URL     string
	Headers []Header
	Body    string
	Timeout time.Duration
}

// Header returns the first header called name, ignoring case.
func (r *Request) Header(name string) (string, bool) {
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return "", false
}

// SetHeader replaces every header called name with one value.
func (r *Request) SetHeader(name, value string) {
	r.DeleteHeader(name)
	r.Headers = append(r.Headers, Header{Name: name, Value: value})
}

// DeleteHeader removes every header called name.
func (r *Request) DeleteHeader(name string) {
	out := r.Headers[:0]
	for _, h := range r.Headers {
		if !strings.EqualFold(h.Name, name) {
			out = append(out, h)
		}
	}
	r.Headers = out
}

// Response is a received response.
type Response struct {
	Protocol   string
	Status     int
	StatusText string
	Headers    http.Header
	Body       []byte
	Duration   time.Duration
	URL        string
}

// Data is the body parsed as JSON, or the body text when it is not JSON.
func (r *Response) Data() any {
	var v any
	if json.Unmarshal(r.Body, &v) == nil {
		return v
	}
	return string(r.Body)
}

// EnvSet is a bru.setEnvVar call; Persist asks for the environment file to be saved.
type EnvSet struct {
	Name    string
	Value   string
	Persist bool
}

// TestResult is one test() call.
type TestResult struct {
	Name   string
	Passed bool
	Error  string
}

// AssertResult is one assertion row.
type AssertResult struct {
	Assertion collection.Assertion
	Passed    bool
	Error     string
}

// Context is what one request's scripts share: the request, its response once received, the
// variables, and everything the scripts asked for.
type Context struct {
	Req   *Request
	Res   *Response
	Scope *vars.Scope
	// Ctx interrupts running scripts and bru.sleep when it is done; nil never does.
	Ctx context.Context

	CollectionName string
	EnvName        string

	Logs   []string
	Tests  []TestResult
	EnvSet []EnvSet
	// NextRequest, Skip and Stop steer a collection run.
	NextRequest string
	Skip        bool
	Stop        bool
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration

	// The running script's watchdog; bru.sleep pauses it.
	timer    *time.Timer
	deadline time.Time
}

// Run executes a before-request, after-response or tests script and appends its test() results;
// like Bruno, test() counts in any of them.
func (c *Context) Run(code string) error {
	if strings.TrimSpace(code) == "" {
		return nil
	}
	vm, err := c.vm()
	if err != nil {
		return err
	}
	runErr := c.settle(vm, code)
	var tests []map[string]any
	if err := vm.ExportTo(vm.Get("__tests"), &tests); err != nil {
		return err
	}
	for _, t := range tests {
		name, _ := t["name"].(string)
		passed, _ := t["passed"].(bool)
		msg, _ := t["error"].(string)
		c.Tests = append(c.Tests, TestResult{Name: name, Passed: passed, Error: msg})
	}
	return runErr
}

// Eval evaluates a JS expression such as res.body.items[0].id; defined is false when it is undefined.
func (c *Context) Eval(expr string) (value any, defined bool, err error) {
	vm, err := c.vm()
	if err != nil {
		return nil, false, err
	}
	v, err := c.run(vm, func() (goja.Value, error) { return vm.RunString("(" + expr + "\n)") })
	if err != nil {
		return nil, false, errors.New(message(err))
	}
	if v == nil || goja.IsUndefined(v) {
		return nil, false, nil
	}
	return v.Export(), true, nil
}

// Assert evaluates one assertion row like Bruno: the expression against res, the value as a literal.
func (c *Context) Assert(a collection.Assertion) AssertResult {
	out := AssertResult{Assertion: a}
	vm, err := c.vm()
	if err == nil {
		_, err = c.run(vm, func() (goja.Value, error) {
			lhs, err := vm.RunString("(" + a.Expression + "\n)")
			if err != nil {
				return nil, err
			}
			fn, _ := goja.AssertFunction(vm.Get("__assert"))
			return fn(goja.Undefined(), lhs, vm.ToValue(a.Operator), c.rhs(vm, a))
		})
	}
	if err != nil {
		out.Error = strings.TrimPrefix(message(err), "Error: ")
		return out
	}
	out.Passed = true
	return out
}

var unary = map[string]bool{
	"isEmpty": true, "isNotEmpty": true, "isNull": true, "isUndefined": true, "isDefined": true, "isTruthy": true,
	"isFalsy": true, "isJson": true, "isNumber": true, "isString": true, "isBoolean": true, "isArray": true,
}

func (c *Context) rhs(vm *goja.Runtime, a collection.Assertion) goja.Value {
	if unary[a.Operator] {
		return goja.Undefined()
	}
	v := strings.TrimSpace(a.Value)
	switch a.Operator {
	case "in", "notIn", "between":
		v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
		var items []any
		for _, part := range strings.Split(v, ",") {
			items = append(items, literal(c.Scope.Interpolate(strings.TrimSpace(part))))
		}
		return vm.NewArray(items...)
	case "matches", "notMatches":
		if len(v) >= 2 && v[0] == '/' && v[len(v)-1] == '/' {
			v = v[1 : len(v)-1]
		}
		return vm.ToValue(c.Scope.Interpolate(v))
	}
	return vm.ToValue(literal(c.Scope.Interpolate(v)))
}

var number = regexp.MustCompile(`^-?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)

// literal reads an assertion value as Bruno does: true, false, null, undefined, a number, a quoted or bare string.
func literal(s string) any {
	switch s = strings.TrimSpace(s); {
	case s == "true":
		return true
	case s == "false":
		return false
	case s == "null":
		return nil
	case s == "undefined":
		return goja.Undefined()
	case len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\''):
		return s[1 : len(s)-1]
	case number.MatchString(s):
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return s
}

var preludeProgram = goja.MustCompile("prelude.js", prelude, false)

func (c *Context) vm() (*goja.Runtime, error) {
	vm := goja.New()
	if _, err := vm.RunProgram(preludeProgram); err != nil {
		return nil, fmt.Errorf("prelude: %w", err)
	}
	_ = vm.Set("console", c.console(vm))
	_ = vm.Set("bru", c.bru(vm))
	_ = vm.Set("req", c.req(vm))
	if c.Res != nil {
		_ = vm.Set("res", c.res(vm))
	}
	return vm, nil
}

func (c *Context) console(vm *goja.Runtime) *goja.Object {
	o := vm.NewObject()
	render, _ := goja.AssertFunction(vm.Get("__show"))
	for _, level := range []string{"log", "info", "warn", "error", "debug"} {
		_ = o.Set(level, func(call goja.FunctionCall) goja.Value {
			parts := make([]string, len(call.Arguments))
			for i, a := range call.Arguments {
				if s, err := render(goja.Undefined(), a); err == nil {
					parts[i] = s.String()
				}
			}
			line := strings.Join(parts, " ")
			if level != "log" {
				line = level + ": " + line
			}
			c.Logs = append(c.Logs, line)
			return goja.Undefined()
		})
	}
	return o
}

// show renders a value as text for a header or an error message.
func show(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if _, ok := goja.AssertFunction(v); ok {
		return "[Function]"
	}
	if s, ok := v.Export().(string); ok {
		return s
	}
	return vars.Format(v.Export())
}

// name reads a required name argument; Bruno throws when it is missing.
func name(vm *goja.Runtime, fn string, v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) || v.String() == "" {
		panic(vm.NewTypeError(fn + " needs a name"))
	}
	return v.String()
}

func (c *Context) bru(vm *goja.Runtime) *goja.Object {
	s := c.Scope
	if s.Runtime == nil {
		s.Runtime = map[string]any{}
	}
	if s.Environment == nil {
		s.Environment = map[string]string{}
	}
	str := func(v goja.Value) string {
		if v == nil || goja.IsUndefined(v) {
			return ""
		}
		return vars.Format(v.Export())
	}
	// Like Bruno, reading a variable interpolates the {{variables}} inside it.
	get := func(m map[string]string, key string) goja.Value {
		if v, ok := m[key]; ok {
			return vm.ToValue(s.Interpolate(v))
		}
		return goja.Undefined()
	}
	next := func(v goja.Value) {
		if v != nil && goja.IsNull(v) {
			// bru.setNextRequest(null) ends the run.
			c.NextRequest, c.Stop = "", true
			return
		}
		c.NextRequest = name(vm, "bru.setNextRequest", v)
	}
	runner := vm.NewObject()
	_ = runner.Set("setNextRequest", next)
	_ = runner.Set("skipRequest", func() { c.Skip = true })
	_ = runner.Set("stopExecution", func() { c.Stop = true })

	o := vm.NewObject()
	for fn, impl := range map[string]any{
		"getVar": func(key string) goja.Value {
			v, ok := s.Runtime[key]
			switch x := v.(type) {
			case string:
				return vm.ToValue(s.Interpolate(x))
			case nil:
				if !ok {
					return goja.Undefined()
				}
			}
			return vm.ToValue(v)
		},
		"setVar": func(key, v goja.Value) {
			k := name(vm, "bru.setVar", key)
			// An undefined value leaves {{name}} unresolved, as in Bruno.
			if v == nil || goja.IsUndefined(v) {
				delete(s.Runtime, k)
				return
			}
			s.Runtime[k] = v.Export()
		},
		"hasVar":        func(key string) bool { _, ok := s.Runtime[key]; return ok },
		"deleteVar":     func(key string) { delete(s.Runtime, key) },
		"deleteAllVars": func() { clear(s.Runtime) },
		"getAllVars":    func() map[string]any { return s.Runtime },
		"getEnvVar":     func(key string) goja.Value { return get(s.Environment, key) },
		"hasEnvVar":     func(key string) bool { _, ok := s.Environment[key]; return ok },
		"setEnvVar": func(key, v, opts goja.Value) {
			k := name(vm, "bru.setEnvVar", key)
			value := str(v)
			s.Environment[k] = value
			persist := false
			if opts != nil && !goja.IsUndefined(opts) && !goja.IsNull(opts) {
				p := opts.ToObject(vm).Get("persist")
				persist = p != nil && p.ToBoolean()
			}
			c.EnvSet = append(c.EnvSet, EnvSet{Name: k, Value: value, Persist: persist})
		},
		"getEnvName": func() string { return c.EnvName },
		"getProcessEnv": func(key string) goja.Value {
			if v, ok := s.Process[key]; ok {
				return vm.ToValue(v)
			}
			return goja.Undefined()
		},
		"getCollectionVar":  func(key string) goja.Value { return get(s.Collection, key) },
		"getFolderVar":      func(key string) goja.Value { return get(s.Folder, key) },
		"getRequestVar":     func(key string) goja.Value { return get(s.Request, key) },
		"getCollectionName": func() string { return c.CollectionName },
		"interpolate":       func(v goja.Value) string { return s.Interpolate(str(v)) },
		"setNextRequest":    next,
		"sleep":             c.sleep,
		"runner":            runner,
	} {
		_ = o.Set(fn, impl)
	}
	return o
}

// sleep waits ms milliseconds, or until Ctx is done. The wait does not count against the
// script's time limit.
func (c *Context) sleep(ms int64) {
	d := time.Duration(ms) * time.Millisecond
	if d <= 0 {
		return
	}
	paused := c.timer != nil && c.timer.Stop()
	left := time.Until(c.deadline)
	var done <-chan struct{}
	if c.Ctx != nil {
		done = c.Ctx.Done()
	}
	t := time.NewTimer(d)
	select {
	case <-t.C:
	case <-done:
		t.Stop()
	}
	if paused {
		c.deadline = time.Now().Add(left)
		c.timer.Reset(left)
	}
}

func jsonCall(vm *goja.Runtime, fn string, v goja.Value) (goja.Value, error) {
	f, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get(fn))
	return f(goja.Undefined(), v)
}

func (c *Context) req(vm *goja.Runtime) *goja.Object {
	r := c.Req
	if r == nil {
		r = &Request{}
		c.Req = r
	}
	o := vm.NewObject()
	headers := func() map[string]string {
		m := map[string]string{}
		for _, h := range r.Headers {
			m[h.Name] = h.Value
		}
		return m
	}
	getBody := func() goja.Value {
		if v, err := jsonCall(vm, "parse", vm.ToValue(r.Body)); err == nil {
			return v
		}
		return vm.ToValue(r.Body)
	}
	setBody := func(v goja.Value) {
		switch {
		case v == nil || goja.IsUndefined(v) || goja.IsNull(v):
			r.Body = ""
		default:
			if s, ok := v.Export().(string); ok {
				r.Body = s
				return
			}
			// JSON.stringify keeps the object's key order.
			if out, err := jsonCall(vm, "stringify", v); err == nil && !goja.IsUndefined(out) {
				r.Body = out.String()
			}
		}
	}
	for fn, impl := range map[string]any{
		"getUrl":    func() string { return r.URL },
		"setUrl":    func(u string) { r.URL = u },
		"getMethod": func() string { return r.Method },
		"setMethod": func(m string) { r.Method = strings.ToUpper(m) },
		"getName":   func() string { return r.Name },
		"getHeader": func(name string) goja.Value {
			if v, ok := r.Header(name); ok {
				return vm.ToValue(v)
			}
			return goja.Undefined()
		},
		"setHeader":    func(key, v goja.Value) { r.SetHeader(name(vm, "req.setHeader", key), show(v)) },
		"deleteHeader": func(name string) { r.DeleteHeader(name) },
		"getHeaders":   headers,
		"setHeaders": func(m map[string]any) {
			r.Headers = nil
			for k, v := range m {
				r.Headers = append(r.Headers, Header{Name: k, Value: vars.Format(v)})
			}
		},
		"getBody":    getBody,
		"setBody":    setBody,
		"getTimeout": func() int64 { return r.Timeout.Milliseconds() },
		"setTimeout": func(ms int64) { r.Timeout = time.Duration(ms) * time.Millisecond },
	} {
		_ = o.Set(fn, impl)
	}
	// The properties read and write the live request, so they agree with the methods.
	accessor := func(prop string, get, set any) {
		var setter goja.Value
		if set != nil {
			setter = vm.ToValue(set)
		}
		_ = o.DefineAccessorProperty(prop, vm.ToValue(get), setter, goja.FLAG_TRUE, goja.FLAG_TRUE)
	}
	accessor("url", func() string { return r.URL }, func(u string) { r.URL = u })
	accessor("method", func() string { return r.Method }, func(m string) { r.Method = strings.ToUpper(m) })
	accessor("headers", headers, nil)
	accessor("body", getBody, setBody)
	return o
}

func (c *Context) res(vm *goja.Runtime) goja.Value {
	r := c.Res
	// Parsed by the script engine, so the body is a plain JS object with its keys in order.
	data := vm.ToValue(string(r.Body))
	if v, err := jsonCall(vm, "parse", data); err == nil {
		data = v
	}
	headers := map[string]any{}
	for k, vs := range r.Headers {
		if len(vs) == 1 {
			headers[strings.ToLower(k)] = vs[0]
		} else {
			headers[strings.ToLower(k)] = vs
		}
	}
	// res is callable like Bruno's: res('items[0].id') reads a path of the body.
	fn := vm.ToValue(func(path string) goja.Value {
		v, err := vm.RunString("(function(b){ try { return b" + accessorPath(path) + " } catch (e) { return undefined } })")
		if err != nil {
			return goja.Undefined()
		}
		call, _ := goja.AssertFunction(v)
		out, _ := call(goja.Undefined(), data)
		return out
	}).ToObject(vm)
	for name, v := range map[string]any{
		"status":          r.Status,
		"statusText":      r.StatusText,
		"headers":         headers,
		"body":            data,
		"responseTime":    r.Duration.Milliseconds(),
		"url":             r.URL,
		"getStatus":       func() int { return r.Status },
		"getStatusText":   func() string { return r.StatusText },
		"getHeaders":      func() map[string]any { return headers },
		"getBody":         func() goja.Value { return data },
		"getResponseTime": func() int64 { return r.Duration.Milliseconds() },
		"getUrl":          func() string { return r.URL },
		"getHeader": func(name string) goja.Value {
			if v, ok := headers[strings.ToLower(name)]; ok {
				return vm.ToValue(v)
			}
			return goja.Undefined()
		},
	} {
		_ = fn.Set(name, v)
	}
	return fn
}

// accessorPath turns "items[0].id" into `?.["items"]?.[0]?.["id"]` for res(path).
func accessorPath(path string) string {
	var b strings.Builder
	for _, part := range strings.Split(strings.TrimPrefix(path, "."), ".") {
		name, rest, _ := strings.Cut(part, "[")
		if name != "" {
			b.WriteString("?.[" + strconv.Quote(name) + "]")
		}
		for rest != "" {
			idx, tail, _ := strings.Cut(rest, "]")
			b.WriteString("?.[" + strings.Trim(idx, `'"`) + "]")
			rest = strings.TrimPrefix(tail, "[")
		}
	}
	return b.String()
}

func (c *Context) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

var (
	errTimeout   = errors.New("script timed out")
	errCancelled = errors.New("cancelled")
)

// run executes f under the script's time limit, interrupting it when Ctx is done. A panic in
// brucli's own script API becomes an error instead of ending the program.
func (c *Context) run(vm *goja.Runtime, f func() (goja.Value, error)) (v goja.Value, err error) {
	defer func() {
		if p := recover(); p != nil {
			v, err = nil, fmt.Errorf("internal error in the script API: %v", p)
		}
	}()
	c.deadline = time.Now().Add(c.timeout())
	c.timer = time.AfterFunc(c.timeout(), func() { vm.Interrupt(errTimeout) })
	defer c.timer.Stop()
	if c.Ctx != nil {
		stop := context.AfterFunc(c.Ctx, func() { vm.Interrupt(errCancelled) })
		defer stop()
	}
	return f()
}

// wrapperLines is how many lines settle puts before a script's first line.
const wrapperLines = 1

// settle runs a script as an async function and returns its error or rejection.
func (c *Context) settle(vm *goja.Runtime, code string) error {
	prg, err := goja.Compile("script", "(async function(){\n"+code+"\n})()", false)
	if err != nil {
		return syntaxError(err)
	}
	v, err := c.run(vm, func() (goja.Value, error) { return vm.RunProgram(prg) })
	if err != nil {
		return errors.New(message(err))
	}
	p, ok := v.Export().(*goja.Promise)
	if !ok {
		return nil
	}
	switch p.State() {
	case goja.PromiseStateRejected:
		return errors.New(thrown(p.Result()))
	case goja.PromiseStatePending:
		return errors.New("script left a promise pending (only microtasks run; no timers or network)")
	}
	return nil
}

var parserPosition = regexp.MustCompile(`^(?:script: )?Line (\d+):(\d+) (.*)$`)

// syntaxError reports a compile error with the line and column of the user's script.
func syntaxError(err error) error {
	se, ok := errors.AsType[*goja.CompilerSyntaxError](err)
	if !ok {
		return err
	}
	if se.File != nil {
		pos := se.File.Position(se.Offset)
		return fmt.Errorf("SyntaxError: %s (line %d:%d)", se.Message, pos.Line-wrapperLines, pos.Column)
	}
	if m := parserPosition.FindStringSubmatch(se.Message); m != nil {
		line, _ := strconv.Atoi(m[1])
		return fmt.Errorf("SyntaxError: %s (line %d:%s)", m[3], line-wrapperLines, m[2])
	}
	return fmt.Errorf("SyntaxError: %s", se.Message)
}

func message(err error) string {
	if ex, ok := errors.AsType[*goja.Exception](err); ok {
		return thrown(ex.Value())
	}
	if in, ok := errors.AsType[*goja.InterruptedError](err); ok {
		if e, ok := in.Value().(error); ok {
			return e.Error()
		}
		return errTimeout.Error()
	}
	return err.Error()
}

var stackLine = regexp.MustCompile(`\bscript:(\d+):(\d+)`)

// thrown renders a thrown value: an Error as "Name: message (line l:c)", anything else as its value.
func thrown(v goja.Value) string {
	o, ok := v.(*goja.Object)
	if !ok || o.Get("message") == nil {
		return show(v)
	}
	msg := v.String()
	if st := o.Get("stack"); st != nil {
		if m := stackLine.FindStringSubmatch(st.String()); m != nil {
			line, _ := strconv.Atoi(m[1])
			msg += fmt.Sprintf(" (line %d:%s)", line-wrapperLines, m[2])
		}
	}
	return msg
}
