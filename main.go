// Command brucli is a terminal UI for Bruno collections in the YAML (OpenCollection) format, and
// `brucli run` sends them headless like `bru run`.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/runner"
	"github.com/leodeim/brucli/internal/tui"
)

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "brucli:", err)
	}
	os.Exit(code)
}

type envVars map[string]string

func (e envVars) String() string { return fmt.Sprint(map[string]string(e)) }

func (e envVars) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return fmt.Errorf("want name=value, got %q", v)
	}
	e[name] = value
	return nil
}

type options struct {
	dir       string
	env       string
	globalEnv string
	vars      envVars
	insecure  bool
	noMouse   bool
	tags      string
	exclude   string
	folder    string
	transport runner.TransportOptions
}

func parse(args []string, headless bool, stderr io.Writer) (*options, error) {
	name := "brucli"
	if headless {
		name = "brucli run"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := &options{vars: envVars{}}
	fs.StringVar(&o.env, "env", "", "environment to use (a file name in environments/)")
	fs.StringVar(&o.globalEnv, "global-env", "", "workspace environment to use")
	fs.Var(o.vars, "env-var", "override an environment variable, name=value (repeatable)")
	fs.BoolVar(&o.insecure, "insecure", false, "skip TLS certificate verification")
	fs.StringVar(&o.transport.ProxyURL, "proxy", "", "proxy URL (HTTP, HTTPS, SOCKS4 or SOCKS5)")
	fs.BoolVar(&o.transport.NoProxy, "noproxy", false, "disable collection and environment proxies")
	fs.StringVar(&o.transport.CACert, "cacert", "", "PEM file containing trusted CA certificates")
	fs.StringVar(&o.transport.ClientCertConfig, "client-cert-config", "", "JSON client certificate configuration file")
	if !headless {
		fs.BoolVar(&o.noMouse, "no-mouse", false, "leave the mouse to the terminal (text selection) instead of clicks and scrolling")
	}
	if headless {
		fs.StringVar(&o.tags, "tags", "", "run only requests with one of these tags (comma separated)")
		fs.StringVar(&o.exclude, "exclude-tags", "", "skip requests with one of these tags (comma separated)")
		fs.StringVar(&o.folder, "folder", "", "run only this folder of the collection (path relative to it)")
	}
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s [flags] [collection-dir]\n", name)
		if !headless {
			fmt.Fprintln(stderr, "       brucli run [flags] [collection-dir]   send requests without the UI, like bru run")
			fmt.Fprintln(stderr, "       brucli update                       update to the latest release")
			fmt.Fprintln(stderr, "       brucli --version                    print the version")
		}
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	switch fs.NArg() {
	case 0:
		o.dir = "."
	case 1:
		o.dir = fs.Arg(0)
	default:
		fs.Usage()
		return nil, errors.New("one collection directory at most")
	}
	return o, nil
}

func run(args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 {
		switch args[0] {
		case "update":
			return runUpdate(args[1:], stdout, stderr)
		case "version", "--version", "-version":
			if len(args) != 1 {
				return 2, fmt.Errorf("unexpected argument %q", args[1])
			}
			_, err := fmt.Fprintln(stdout, "brucli", buildVersion())
			if err != nil {
				return 1, err
			}
			return 0, nil
		}
	}
	headless := len(args) > 0 && args[0] == "run"
	if headless {
		args = args[1:]
	}
	o, err := parse(args, headless, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0, err
	}
	if err != nil {
		return 2, err
	}
	c, err := collection.Load(o.dir)
	if err != nil {
		return 2, err
	}
	var env *collection.Environment
	if o.env != "" {
		if env = c.Environment(o.env); env == nil {
			return 2, fmt.Errorf("no environment %q in %s", o.env, filepath.Join(c.Dir, "environments"))
		}
	}
	s := runner.NewSession(c, env, o.insecure)
	s.Interactive = !headless
	if err := s.ConfigureTransport(o.transport); err != nil {
		return 2, err
	}
	if o.globalEnv != "" {
		s.GlobalEnv = c.GlobalEnvironment(o.globalEnv)
		if s.GlobalEnv == nil {
			return 2, fmt.Errorf("no global environment %q in workspace %s", o.globalEnv, c.WorkspaceDir)
		}
	}
	s.Overrides = o.vars
	if !headless {
		m := tui.New(tui.Config{Session: s, Editor: cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR")), Mouse: !o.noMouse})
		opts := []tea.ProgramOption{tea.WithAltScreen()}
		if !o.noMouse {
			opts = append(opts, tea.WithMouseCellMotion())
		}
		if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
			return 1, err
		}
		return 0, nil
	}
	return runHeadless(c, s, o, stdout)
}

func runHeadless(c *collection.Collection, s *runner.Session, o *options, out io.Writer) (int, error) {
	root := c.Root
	if o.folder != "" {
		if root = findFolder(c.Root, filepath.Join(c.Dir, o.folder)); root == nil {
			return 2, fmt.Errorf("no folder %q in the collection", o.folder)
		}
	}
	reqs := runner.Filter(root.AllRequests(), split(o.tags), split(o.exclude))
	if len(reqs) == 0 {
		return 2, errors.New("no requests match")
	}
	for _, w := range c.Warnings {
		fmt.Fprintln(out, "skipped", w)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// The first Ctrl-C stops the run after the current request; a second one kills the process.
	context.AfterFunc(ctx, stop)
	var counts runner.Counts
	s.Run(ctx, reqs, func(res *runner.Result) {
		report(out, c, res)
		counts.Add(res)
	})
	fmt.Fprintf(out, "\n%s, %d requests\n", counts, counts.Passed+counts.Failed+counts.Skipped)
	if counts.Failed > 0 || ctx.Err() != nil {
		return 1, nil
	}
	return 0, nil
}

func report(out io.Writer, c *collection.Collection, res *runner.Result) {
	r := res.Request
	name, _ := filepath.Rel(c.Dir, strings.TrimSuffix(r.Path, filepath.Ext(r.Path)))
	mark := "✓"
	if res.Failed() {
		mark = "✗"
	}
	switch {
	case res.Skipped:
		fmt.Fprintf(out, "- %-6s %s  skipped\n", r.Method, name)
		return
	case res.Err != nil:
		fmt.Fprintf(out, "%s %-6s %s  %v\n", mark, r.Method, name, res.Err)
	default:
		fmt.Fprintf(out, "%s %-6s %s  %d %s\n", mark, r.Method, name, res.Response.Status, res.Response.Duration.Round(1e6))
	}
	for _, a := range res.Assertions {
		if !a.Passed {
			fmt.Fprintf(out, "    ✗ %s %s %s: %s\n", a.Assertion.Expression, a.Assertion.Operator, a.Assertion.Value, a.Error)
		}
	}
	for _, t := range res.Tests {
		if !t.Passed {
			fmt.Fprintf(out, "    ✗ test %s: %s\n", t.Name, t.Error)
		}
	}
	for _, e := range res.ScriptErrors {
		fmt.Fprintf(out, "    ✗ %s\n", e)
	}
	if len(res.Unresolved) > 0 {
		fmt.Fprintf(out, "    ! undefined: %s\n", strings.Join(res.Unresolved, ", "))
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "    ! %s\n", w)
	}
}

func findFolder(f *collection.Folder, dir string) *collection.Folder {
	if filepath.Clean(f.Dir) == filepath.Clean(dir) {
		return f
	}
	for _, sub := range f.Folders {
		if found := findFolder(sub, dir); found != nil {
			return found
		}
	}
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
