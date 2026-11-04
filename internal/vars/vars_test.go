package vars

import (
	"regexp"
	"slices"
	"strconv"
	"testing"
)

func sampleScope() *Scope {
	return &Scope{
		Collection:  map[string]string{"ownerId": "u-1", "sellerId": "{{ownerId}}", "shared": "collection"},
		Environment: map[string]string{"port": "8080", "baseUrl": "http://localhost:{{port}}", "username": "{{process.env.APP_USER}}", "shared": "env"},
		Folder:      map[string]string{"shared": "folder"},
		Request:     map[string]string{},
		Runtime:     map[string]any{"orderVersion": float64(3), "row": map[string]any{"id": "x"}},
		Process:     map[string]string{"APP_USER": "ada"},
	}
}

func TestInterpolate(t *testing.T) {
	s := sampleScope()
	tests := []struct{ in, want string }{
		{"{{baseUrl}}/v1/x", "http://localhost:8080/v1/x"},
		{"{{username}}", "ada"},
		{"{{sellerId}}", "u-1"},
		{"{{shared}}", "folder"},
		{`{"version": "{{orderVersion}}"}`, `{"version": "3"}`},
		{"{{row}}", `{"id":"x"}`},
		{"{{missing}} and {{ port }}", "{{missing}} and {{ port }}"},
		{"{{process.env.NOPE}}", "{{process.env.NOPE}}"},
		{"no vars", "no vars"},
	}
	for _, tt := range tests {
		if got := s.Interpolate(tt.in); got != tt.want {
			t.Errorf("Interpolate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrecedence(t *testing.T) {
	s := sampleScope()
	s.Request["shared"] = "request"
	if v, src, _ := s.Lookup("shared"); v != "request" || src != Request {
		t.Fatalf("request should win, got %q from %s", v, src)
	}
	s.Runtime["shared"] = "runtime"
	if v, src, _ := s.Lookup("shared"); v != "runtime" || src != Runtime {
		t.Fatalf("runtime should win, got %q from %s", v, src)
	}
	all := s.All()
	i := slices.IndexFunc(all, func(v Var) bool { return v.Name == "shared" })
	if want := []Source{Request, Folder, Environment, Collection}; !slices.Equal(all[i].Shadowed, want) {
		t.Fatalf("shadowed = %v, want %v", all[i].Shadowed, want)
	}
	j := slices.IndexFunc(all, func(v Var) bool { return v.Name == "baseUrl" })
	if all[j].Raw != "http://localhost:{{port}}" || all[j].Resolved != "http://localhost:8080" {
		t.Fatalf("baseUrl var = %+v", all[j])
	}
}

func TestCycleStops(t *testing.T) {
	s := &Scope{Environment: map[string]string{"a": "{{b}}", "b": "{{a}}"}}
	if got := s.Interpolate("{{a}}"); got != "{{a}}" {
		t.Fatalf("cycle = %q", got)
	}
	if got := s.Unresolved("x {{a}} {{a}}"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("unresolved = %v", got)
	}
}

func TestMocks(t *testing.T) {
	s := &Scope{}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if got := s.Interpolate("{{$randomUUID}}"); !uuidRe.MatchString(got) {
		t.Fatalf("randomUUID = %q", got)
	}
	if got := s.Interpolate("{{$unknownMock}}"); got != "{{$unknownMock}}" {
		t.Fatalf("unknown mock = %q", got)
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{{nil, "null"}, {"s", "s"}, {true, "true"}, {float64(2.5), "2.5"}, {float64(10), "10"}, {int64(7), "7"}, {[]any{1.0}, "[1]"}}
	for _, tt := range tests {
		if got := Format(tt.in); got != tt.want {
			t.Errorf("Format(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPathsAndLimits(t *testing.T) {
	s := &Scope{Runtime: map[string]any{"user": map[string]any{"id": "u1", "tags": []any{"a", map[string]any{"k": float64(2)}}}}}
	for in, want := range map[string]string{
		"{{user.id}}":        "u1",
		"{{user.tags[1].k}}": "2",
		"{{user.tags[0]}}":   "a",
		"{{user.missing}}":   "{{user.missing}}",
		"{{user.tags[9]}}":   "{{user.tags[9]}}",
		"{{user}}":           `{"id":"u1","tags":["a",{"k":2}]}`,
	} {
		if got := s.Interpolate(in); got != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
	// Each level doubles the one below: without a budget a29 would be gigabytes.
	doubling := map[string]string{"a0": "xxxxxxxxxx"}
	for i := 1; i < 30; i++ {
		doubling["a"+strconv.Itoa(i)] = "{{a" + strconv.Itoa(i-1) + "}}{{a" + strconv.Itoa(i-1) + "}}"
	}
	if got := (&Scope{Environment: doubling}).Interpolate("{{a29}}"); len(got) > 2*interpolationBudget {
		t.Fatalf("expanded to %d bytes", len(got))
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if got := Format(cyclic); got != "[map[string]interface {}]" {
		t.Fatalf("cyclic = %q", got)
	}
	if got := Format(map[string]any{"q": "a&b<c>"}); got != `{"q":"a&b<c>"}` {
		t.Fatalf("escaped = %q", got)
	}
}
