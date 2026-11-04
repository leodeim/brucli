package runner

import (
	"fmt"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
)

// Result is the outcome of one request.
type Result struct {
	Request *collection.Request
	// Sent is the request after scripts and interpolation, as it went out.
	Sent     *script.Request
	Response *script.Response
	// Err is a failure to build or send the request; there is no Response then.
	Err          error
	ScriptErrors []string
	Assertions   []script.AssertResult
	Tests        []script.TestResult
	Logs         []string
	// Unresolved lists {{names}} that no scope defines.
	Unresolved []string
	Warnings   []string
	Skipped    bool
	// EnvSet lists bru.setEnvVar calls; Saved reports that the persist: true ones were written to the file.
	EnvSet      []script.EnvSet
	Saved       bool
	NextRequest string
	Stop        bool
}

// Passed reports a sent request whose scripts, assertions and tests all passed.
func (r *Result) Passed() bool { return !r.Skipped && !r.Failed() }

// Failed reports a request that could not be sent, or whose scripts, assertions or tests failed.
// A request skipped by its script has neither passed nor failed, as in bru run.
func (r *Result) Failed() bool {
	if r.Skipped {
		return false
	}
	if r.Err != nil || len(r.ScriptErrors) > 0 {
		return true
	}
	for _, a := range r.Assertions {
		if !a.Passed {
			return true
		}
	}
	for _, t := range r.Tests {
		if !t.Passed {
			return true
		}
	}
	return false
}

// Counts tallies results the way bru run's summary does.
type Counts struct{ Passed, Failed, Skipped int }

// Add counts one result.
func (c *Counts) Add(r *Result) {
	switch {
	case r.Skipped:
		c.Skipped++
	case r.Failed():
		c.Failed++
	default:
		c.Passed++
	}
}

func (c Counts) String() string {
	s := fmt.Sprintf("%d passed, %d failed", c.Passed, c.Failed)
	if c.Skipped > 0 {
		s += fmt.Sprintf(", %d skipped", c.Skipped)
	}
	return s
}
