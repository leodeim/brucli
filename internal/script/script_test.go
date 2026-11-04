package script

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/vars"
)

func newContext(status int, body string) *Context {
	return &Context{
		Req:   &Request{Name: "ListPets", Method: "GET", URL: "{{baseUrl}}/pets"},
		Res:   &Response{Status: status, StatusText: http.StatusText(status), Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(body), Duration: 12 * time.Millisecond},
		Scope: &vars.Scope{Environment: map[string]string{"petId": "p-1"}, Runtime: map[string]any{}},
	}
}

// The after-response scripts of the sample collection.
func TestCollectionCaptureScripts(t *testing.T) {
	c, err := collection.Load("../collection/testdata/petstore")
	if err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{}
	for _, r := range c.Root.AllRequests() {
		scripts[r.Name] = r.Scripts.After
	}

	ctx := newContext(200, `{"users":[{"id":"u-bob","name":"Bob"},{"id":"u-ada","name":"Ada"}]}`)
	if err := ctx.Run(scripts["ListUsers"]); err != nil {
		t.Fatal(err)
	}
	if ctx.Scope.Runtime["userId"] != "u-ada" {
		t.Fatalf("userId = %v", ctx.Scope.Runtime["userId"])
	}

	// An empty page must leave earlier values alone.
	ctx = newContext(200, `{"pets":[]}`)
	ctx.Scope.Runtime["petId"] = "kept"
	if err := ctx.Run(scripts["ListPets"]); err != nil {
		t.Fatal(err)
	}
	if ctx.Scope.Runtime["petId"] != "kept" {
		t.Fatalf("petId = %v", ctx.Scope.Runtime["petId"])
	}
}

func TestAssertions(t *testing.T) {
	ctx := newContext(200, `{"items":[1,2,3],"name":"Rex","status":"available","empty":[],"n":null}`)
	ctx.Scope.Environment["want"] = "200"
	tests := []struct {
		expr, op, value string
		pass            bool
	}{
		{"res.status", "eq", "200", true},
		{"res.status", "eq", "201", false},
		{"res.status", "eq", "{{want}}", true},
		{"res.status", "neq", "500", true},
		{"res.status", "in", "[200, 204]", true},
		{"res.status", "between", "200, 299", true},
		{"res.status", "lt", "300", true},
		{"res.body.items", "length", "3", true},
		{"res.body.items", "contains", "2", true},
		{"res.body.items", "isArray", "", true},
		{"res.body.name", "eq", "'Rex'", true},
		{"res.body.name", "startsWith", "Re", true},
		{"res.body.status", "matches", "/^avail/", true},
		{"res.body.status", "notMatches", "^ACTIVE", true},
		{"res.body.empty", "isEmpty", "", true},
		{"res.body.n", "isNull", "", true},
		{"res.body.missing", "isUndefined", "", true},
		{"res.body.missing", "isDefined", "", false},
		{"res('items[0]')", "eq", "1", true},
		{"res.getHeader('content-type')", "contains", "json", true},
		{"res.body.items.", "eq", "1", false},
	}
	for _, tt := range tests {
		got := ctx.Assert(collection.Assertion{Expression: tt.expr, Operator: tt.op, Value: tt.value, Enabled: true})
		if got.Passed != tt.pass {
			t.Errorf("%s %s %s: passed = %v (%s), want %v", tt.expr, tt.op, tt.value, got.Passed, got.Error, tt.pass)
		}
	}
	fail := ctx.Assert(collection.Assertion{Expression: "res.status", Operator: "eq", Value: "201"})
	if fail.Error != "expected 200 to equal 201" {
		t.Fatalf("message = %q", fail.Error)
	}
}

func TestTests(t *testing.T) {
	ctx := newContext(200, `{"pet":{"id":"p1","tags":["a"]}}`)
	err := ctx.Run(`
test("status is 200", () => expect(res.status).to.equal(200));
test("has pet", function () {
  expect(res.body).to.have.property("pet");
  expect(res.body.pet).to.deep.equal({ id: "p1", tags: ["a"] });
  expect(res.body.pet.tags).to.include("a").and.have.lengthOf(1);
  expect(res.body.pet.id).to.be.a("string").that.is.not.empty;
});
test("fails", () => expect(res.status).to.be.above(299));
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Tests) != 3 || !ctx.Tests[0].Passed || !ctx.Tests[1].Passed || ctx.Tests[2].Passed {
		t.Fatalf("tests = %+v", ctx.Tests)
	}
	if ctx.Tests[2].Error != "expected 200 to be above 299" {
		t.Fatalf("error = %q", ctx.Tests[2].Error)
	}
}

func TestBruAndReq(t *testing.T) {
	ctx := newContext(200, `{}`)
	ctx.Res = nil
	ctx.Req.Headers = []Header{{"X-Old", "1"}}
	err := ctx.Run(`
console.log("pet", bru.getEnvVar("petId"), { a: 1 });
bru.setEnvVar("port", "9090", { persist: true });
bru.setEnvVar("tmp", 5);
bru.setVar("n", 41 + 1);
req.setHeader("Authorization", "Bearer " + bru.interpolate("{{petId}}"));
req.deleteHeader("x-old");
req.setUrl(req.getUrl() + "?x=1");
req.setBody({ hello: "world" });
await Promise.resolve();
bru.runner.skipRequest();
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Logs) != 1 || ctx.Logs[0] != `pet p-1 {"a":1}` {
		t.Fatalf("logs = %q", ctx.Logs)
	}
	if len(ctx.EnvSet) != 2 || ctx.EnvSet[0] != (EnvSet{"port", "9090", true}) || ctx.EnvSet[1] != (EnvSet{"tmp", "5", false}) {
		t.Fatalf("env set = %+v", ctx.EnvSet)
	}
	if ctx.Scope.Environment["port"] != "9090" || ctx.Scope.Runtime["n"] != int64(42) {
		t.Fatalf("scope = %+v %+v", ctx.Scope.Environment, ctx.Scope.Runtime)
	}
	if v, _ := ctx.Req.Header("authorization"); v != "Bearer p-1" || len(ctx.Req.Headers) != 1 {
		t.Fatalf("headers = %+v", ctx.Req.Headers)
	}
	if ctx.Req.URL != "{{baseUrl}}/pets?x=1" || ctx.Req.Body != `{"hello":"world"}` || !ctx.Skip {
		t.Fatalf("req = %+v skip %v", ctx.Req, ctx.Skip)
	}
}

func TestErrors(t *testing.T) {
	ctx := newContext(200, `{}`)
	if err := ctx.Run("throw new Error('boom')"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("throw: %v", err)
	}
	if err := ctx.Run("res.body.x.y"); err == nil || !strings.Contains(err.Error(), "TypeError") {
		t.Fatalf("type error: %v", err)
	}
	if err := ctx.Run("const = 1"); err == nil {
		t.Fatal("syntax error not reported")
	}
	ctx.Timeout = 50 * time.Millisecond
	if err := ctx.Run("while (true) {}"); err == nil || err.Error() != "script timed out" {
		t.Fatalf("timeout: %v", err)
	}
	if v, ok, err := ctx.Eval("res.status + 1"); err != nil || !ok || v != int64(201) {
		t.Fatalf("eval = %v %v", v, err)
	}
}

func TestScriptAPIEdgesDoNotCrash(t *testing.T) {
	ctx := newContext(200, `{"z":1,"a":{"b":2},"items":[{"id":1}]}`)
	err := ctx.Run(`
req.setBody();
const a = {}; a.self = a;
console.log(a, new Error("boom"), function f() {});
console.log(Object.keys(res.body).join(","), res.body.items.indexOf(res.body.items[0]));
bru.setVar("gone", "x"); bru.setVar("gone", undefined);
test("in a script", () => expect(1).to.equal(2));
`)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Req.Body != "" || len(ctx.Logs) != 2 || ctx.Logs[0] != `{"self":"[Circular]"} Error: boom [Function]` || ctx.Logs[1] != "z,a,items 0" {
		t.Fatalf("body %q logs %q", ctx.Req.Body, ctx.Logs)
	}
	if _, ok := ctx.Scope.Runtime["gone"]; ok {
		t.Fatal("setVar(undefined) kept the variable")
	}
	if len(ctx.Tests) != 1 || ctx.Tests[0].Passed {
		t.Fatalf("tests = %+v", ctx.Tests)
	}
	if err := ctx.Run(`bru.setEnvVar()`); err == nil || !strings.Contains(err.Error(), "needs a name") {
		t.Fatalf("setEnvVar() = %v", err)
	}
}

func TestScriptErrorsHaveLines(t *testing.T) {
	ctx := newContext(200, `{}`)
	if err := ctx.Run("const x = 1;\nnull.y;"); err == nil || !strings.Contains(err.Error(), "(line 2:") {
		t.Fatalf("runtime error = %v", err)
	}
	if err := ctx.Run("const x = 1;\nconst = 2;"); err == nil || !strings.HasPrefix(err.Error(), "SyntaxError: ") || strings.Contains(err.Error(), "SyntaxError: SyntaxError") || !strings.Contains(err.Error(), "(line 2:") {
		t.Fatalf("syntax error = %v", err)
	}
}

func TestScriptCancelAndSleep(t *testing.T) {
	ctx := newContext(200, `{}`)
	c, cancel := context.WithCancel(t.Context())
	ctx.Ctx = c
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if err := ctx.Run("while (true) {}"); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("cancel took %s: %v", time.Since(start), err)
	}
	ctx = newContext(200, `{}`)
	ctx.Timeout = 200 * time.Millisecond
	if err := ctx.Run("await bru.sleep(300); bru.setVar('slept', true)"); err != nil || ctx.Scope.Runtime["slept"] != true {
		t.Fatalf("sleep counted against the limit: %v", err)
	}
}

func TestScriptVarsAndSteering(t *testing.T) {
	ctx := newContext(200, `{}`)
	ctx.Scope.Environment["port"] = "8080"
	ctx.Scope.Environment["baseUrl"] = "http://localhost:{{port}}"
	if err := ctx.Run(`bru.setVar("u", bru.getEnvVar("baseUrl")); bru.setNextRequest(null)`); err != nil {
		t.Fatal(err)
	}
	if ctx.Scope.Runtime["u"] != "http://localhost:8080" || !ctx.Stop || ctx.NextRequest != "" {
		t.Fatalf("u %v stop %v next %q", ctx.Scope.Runtime["u"], ctx.Stop, ctx.NextRequest)
	}
	if v, ok, err := ctx.Eval("res.body.missing"); err != nil || ok || v != nil {
		t.Fatalf("undefined = %v %v %v", v, ok, err)
	}
}

func TestAssertionsFailLikeChai(t *testing.T) {
	ctx := newContext(200, `{"n":null,"count":"10","list":[{"a":1}]}`)
	for _, a := range []collection.Assertion{
		{Expression: "res.body.missing", Operator: "isNotEmpty"},
		{Expression: "res.body.n", Operator: "isNotEmpty"},
		{Expression: "res.body.count", Operator: "gt", Value: "9"},
		{Expression: "res.body.count", Operator: "eq", Value: "NaN"},
	} {
		if r := ctx.Assert(a); r.Passed {
			t.Errorf("%s %s %s passed", a.Expression, a.Operator, a.Value)
		}
	}
	if err := ctx.Run(`
test("strict include", () => expect(res.body.list).to.not.include({a: 1}));
test("deep include", () => expect(res.body.list).to.deep.include({a: 1}));
test("dates", () => { expect(new Date(1)).to.not.eql(new Date(2)); expect(new Date(1)).to.be.a("date"); });`); err != nil {
		t.Fatal(err)
	}
	for _, r := range ctx.Tests {
		if !r.Passed {
			t.Errorf("%s: %s", r.Name, r.Error)
		}
	}
}
