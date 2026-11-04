// Package runner sends a collection's requests the way Bruno does: variable scopes, inherited auth and
// headers, scripts in sandwich order, post-response variables, assertions and tests.
package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

// DefaultTimeout applies when a request has no timeout setting.
const DefaultTimeout = 60 * time.Second

// maxBody caps how much of a response body is read.
const maxBody = 64 << 20

// Execute sends one request with its scripts, post-response variables, assertions and tests.
func (s *Session) Execute(ctx context.Context, r *collection.Request) *Result {
	res := &Result{Request: r}
	scope := s.Scope(r)
	chain := r.Chain()
	sc := &script.Context{Scope: scope, Ctx: ctx, CollectionName: s.Collection.Name, EnvName: s.envName()}
	sc.Req = s.draft(r, chain, res)

	// Bruno runs the before-request scripts as one script: an error stops it and nothing is sent.
	ok := true
	for _, f := range chain {
		if ok = s.runScript(sc, res, f.Defaults.Scripts.Before, "before-request", f); !ok {
			break
		}
	}
	if ok {
		ok = s.runScript(sc, res, r.Scripts.Before, "before-request", nil)
	}
	if !ok {
		res.Err = errors.New("not sent: a before-request script failed")
		s.finish(sc, res)
		return res
	}
	if sc.Skip {
		res.Skipped = true
		s.finish(sc, res)
		return res
	}

	sent, err := s.finalize(r, chain, sc.Req, scope, res)
	res.Sent = sent
	if err != nil {
		res.Err = err
		s.finish(sc, res)
		return res
	}
	if err := s.prepareAuth(ctx, r, sent, scope); err != nil {
		res.Err = err
		s.finish(sc, res)
		return res
	}
	switch r.Kind {
	case "websocket":
		res.Response, res.Err = s.sendWebSocket(ctx, r, sent, res, scope)
	case "grpc":
		res.Response, res.Err = s.sendGRPC(ctx, r, sent, res, scope)
	default:
		res.Response, res.Err = s.send(ctx, sent, r.Settings, res, scope)
	}
	if res.Err != nil {
		s.finish(sc, res)
		return res
	}
	sc.Res = res.Response

	for _, a := range r.Actions {
		if !a.Enabled {
			continue
		}
		v, defined, err := sc.Eval(a.Expression)
		switch {
		case err != nil:
			res.ScriptErrors = append(res.ScriptErrors, fmt.Sprintf("post-response var %s: %v", a.Name, err))
		case !defined:
			// Undefined leaves {{name}} unresolved, as in Bruno.
			delete(scope.Request, a.Name)
			delete(scope.Runtime, a.Name)
		case a.Scope == "request":
			scope.Request[a.Name] = vars.Format(v)
		default:
			scope.Runtime[a.Name] = v
		}
	}
	s.runScript(sc, res, r.Scripts.After, "after-response", nil)
	for _, f := range slices.Backward(chain) {
		s.runScript(sc, res, f.Defaults.Scripts.After, "after-response", f)
	}
	for _, a := range r.Assertions {
		if a.Enabled {
			res.Assertions = append(res.Assertions, sc.Assert(a))
		}
	}
	for _, f := range chain {
		s.runTests(sc, res, f.Defaults.Scripts.Tests, f)
	}
	s.runTests(sc, res, r.Scripts.Tests, nil)
	s.finish(sc, res)
	return res
}

// runScript runs one script and reports whether it finished without an error.
func (s *Session) runScript(sc *script.Context, res *Result, code, phase string, f *collection.Folder) bool {
	if err := sc.Run(code); err != nil {
		res.ScriptErrors = append(res.ScriptErrors, fmt.Sprintf("%s script (%s): %v", phase, s.owner(f), err))
		return false
	}
	return true
}

func (s *Session) runTests(sc *script.Context, res *Result, code string, f *collection.Folder) {
	if err := sc.Run(code); err != nil {
		res.ScriptErrors = append(res.ScriptErrors, fmt.Sprintf("tests (%s): %v", s.owner(f), err))
	}
}

func (s *Session) owner(f *collection.Folder) string {
	switch {
	case f == nil:
		return "request"
	case f == s.Collection.Root:
		return "collection"
	}
	return "folder " + f.Name
}

// finish copies what scripts produced into the result and applies bru.setEnvVar to the environment.
func (s *Session) finish(sc *script.Context, res *Result) {
	res.Tests = sc.Tests
	res.Logs = sc.Logs
	res.NextRequest, res.Stop = sc.NextRequest, sc.Stop
	if len(sc.EnvSet) == 0 || s.Env == nil {
		return
	}
	persist := false
	for _, set := range sc.EnvSet {
		persist = persist || set.Persist
		if _, ok := s.Overrides[set.Name]; ok {
			s.Overrides[set.Name] = set.Value
		}
		i := slices.IndexFunc(s.Env.Vars, func(v collection.EnvVar) bool { return v.Name == set.Name })
		if i < 0 {
			s.Env.Vars = append(s.Env.Vars, collection.EnvVar{KV: collection.KV{Name: set.Name, Value: set.Value, Enabled: true}})
			continue
		}
		if s.Env.Vars[i].Secret {
			s.SetSecret(set.Name, set.Value)
			continue
		}
		s.Env.Vars[i].Value = set.Value
	}
	res.EnvSet = sc.EnvSet
	if !persist {
		return
	}
	for _, set := range sc.EnvSet {
		if !set.Persist {
			continue
		}
		if err := s.Env.Persist(set.Name, set.Value); err != nil {
			res.ScriptErrors = append(res.ScriptErrors, "saving environment: "+err.Error())
			return
		}
	}
	res.Saved = true
}

// Filter keeps requests like bru run --tags/--exclude-tags: any tag of include (or include empty), none of exclude.
func Filter(reqs []*collection.Request, include, exclude []string) []*collection.Request {
	var out []*collection.Request
	for _, r := range reqs {
		if slices.ContainsFunc(exclude, r.HasTag) {
			continue
		}
		if len(include) > 0 && !slices.ContainsFunc(include, r.HasTag) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// maxJumps caps bru.setNextRequest jumps in one run, like bru run, so a script that jumps back
// forever cannot hang it.
const maxJumps = 10000

// Run executes reqs in order, honouring bru.setNextRequest and bru.runner.stopExecution; each result goes to
// report as soon as it is known. It stops early when ctx is done.
func (s *Session) Run(ctx context.Context, reqs []*collection.Request, report func(*Result)) {
	jumps := 0
	for i := 0; i < len(reqs) && ctx.Err() == nil; i++ {
		res := s.Execute(ctx, reqs[i])
		next := -1
		if res.NextRequest != "" && !res.Stop {
			next = slices.IndexFunc(reqs, func(r *collection.Request) bool { return r.Name == res.NextRequest })
			switch {
			case next < 0:
				res.Warnings = append(res.Warnings, fmt.Sprintf("bru.setNextRequest(%q): no such request in this run; going on in order", res.NextRequest))
			case jumps >= maxJumps:
				res.Warnings = append(res.Warnings, fmt.Sprintf("bru.setNextRequest: stopped after %d jumps", maxJumps))
				res.Stop = true
			default:
				jumps++
			}
		}
		report(res)
		if res.Stop {
			return
		}
		if next >= 0 {
			i = next - 1
		}
	}
}
