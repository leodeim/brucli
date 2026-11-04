package collection

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGlobalEnvironments(t *testing.T) {
	dir := copyFixture(t)
	workspace := filepath.Dir(dir)
	if err := os.WriteFile(filepath.Join(workspace, "workspace.yml"), []byte("info:\n  name: Workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "environments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "environments", "shared.yml"), []byte("name: Shared\nvariables:\n  - name: sharedUrl\n    value: https://example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, dir)
	env := c.GlobalEnvironment("Shared")
	if c.WorkspaceDir != workspace || env == nil || env.Vars[0].Value != "https://example.com" || c.Environment("Shared") != nil {
		t.Fatalf("workspace environments: %+v", c)
	}
}
