package runner

import (
	"testing"

	"github.com/leodeim/brucli/internal/collection"
)

func TestGlobalScope(t *testing.T) {
	c, _ := fixture(t)
	s := NewSession(c, nil, false)
	s.GlobalEnv = &collection.Environment{Name: "Shared", Vars: []collection.EnvVar{
		{KV: collection.KV{Name: "shared", Value: "global", Enabled: true}},
		{KV: collection.KV{Name: "sharedUrl", Value: "https://example.com", Enabled: true}},
		{KV: collection.KV{Name: "secret", Value: "must not load", Enabled: true}, Secret: true},
		{KV: collection.KV{Name: "off", Value: "must not load", Enabled: false}},
	}}
	scope := s.Scope(nil)
	if scope.Interpolate("{{sharedUrl}}") != "https://example.com" || scope.Interpolate("{{secret}} {{off}}") != "{{secret}} {{off}}" {
		t.Fatalf("global scope: %+v", scope)
	}
	c.Root.Defaults.Vars = append(c.Root.Defaults.Vars, collection.KV{Name: "shared", Value: "collection", Enabled: true})
	if s.Scope(nil).Interpolate("{{shared}}") != "collection" {
		t.Fatal("global environment overrode collection")
	}
	fork := s.Fork()
	fork.GlobalEnv.Vars[0].Value = "fork"
	if s.GlobalEnv.Vars[0].Value != "global" {
		t.Fatal("fork shares global variables")
	}
}
