package collection

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func (c *Collection) GlobalEnvironment(name string) *Environment {
	for _, env := range c.GlobalEnvironments {
		if env.Name == name {
			return env
		}
	}
	return nil
}

func (c *Collection) loadGlobalEnvironments() error {
	for dir := c.Dir; ; dir = filepath.Dir(dir) {
		found := false
		for _, name := range []string{"workspace.yml", "workspace.yaml", "workspace.json"} {
			_, err := os.Stat(filepath.Join(dir, name))
			if err == nil {
				found = true
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if found {
			c.WorkspaceDir = dir
			entries, err := os.ReadDir(filepath.Join(dir, envDir))
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.IsDir() || !slices.Contains([]string{".yml", ".yaml", ".bru"}, filepath.Ext(entry.Name())) {
					continue
				}
				env, err := readEnvironment(filepath.Join(dir, envDir, entry.Name()))
				if err != nil {
					return fmt.Errorf("global environment %s: %w", entry.Name(), err)
				}
				c.GlobalEnvironments = append(c.GlobalEnvironments, env)
			}
			slices.SortFunc(c.GlobalEnvironments, func(a, b *Environment) int { return strings.Compare(a.Name, b.Name) })
			return nil
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}
