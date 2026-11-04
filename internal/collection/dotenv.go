package collection

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

func readDotEnv(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseDotEnv(b), nil
}

// dotenvLine is dotenv 16's LINE pattern: optional export, KEY= or KEY:, then a single-,
// double- or back-quoted value (which may span lines) or a bare value up to a # comment.
var dotenvLine = regexp.MustCompile(`(?m)^\s*(?:export\s+)?([\w.-]+)(?:\s*=\s*?|:\s+?)(\s*'(?:\\'|[^'])*'|\s*"(?:\\"|[^"])*"|\s*` + "`(?:\\\\`|[^`])*`" + `|[^#\r\n]+)?\s*(?:#.*)?$`)

// ParseDotEnv reads a .env file the way the dotenv package (and so Bruno) does.
func ParseDotEnv(b []byte) map[string]string {
	src := strings.TrimPrefix(string(b), "\uFEFF")
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	vars := map[string]string{}
	for _, m := range dotenvLine.FindAllStringSubmatch(src, -1) {
		value := strings.TrimSpace(m[2])
		quote := byte(0)
		if len(value) >= 2 && strings.IndexByte("'\"`", value[0]) >= 0 && value[len(value)-1] == value[0] {
			quote = value[0]
			value = value[1 : len(value)-1]
		}
		if quote == '"' {
			value = strings.NewReplacer(`\n`, "\n", `\r`, "\r").Replace(value)
		}
		vars[m[1]] = value
	}
	return vars
}
