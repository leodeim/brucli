package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScript(t *testing.T) {
	for _, tc := range []struct {
		name, os, arch, asset, failure, wantError string
		defaultDir, fallbackChecksum              bool
	}{
		{name: "linux amd64", os: "Linux", arch: "x86_64", asset: "brucli_linux_amd64"},
		{name: "linux arm64", os: "Linux", arch: "aarch64", asset: "brucli_linux_arm64"},
		{name: "mac amd64", os: "Darwin", arch: "x86_64", asset: "brucli_darwin_amd64", fallbackChecksum: true},
		{name: "mac arm64 default directory", os: "Darwin", arch: "arm64", asset: "brucli_darwin_arm64", defaultDir: true, fallbackChecksum: true},
		{name: "corrupt download", os: "Linux", arch: "amd64", asset: "brucli_linux_amd64", failure: "corrupt", wantError: "Checksum verification failed"},
		{name: "missing checksum", os: "Linux", arch: "amd64", asset: "brucli_linux_amd64", failure: "missing-checksum", wantError: "No checksum found"},
		{name: "failed download", os: "Linux", arch: "amd64", asset: "brucli_linux_amd64", failure: "download", wantError: "fixture download failed"},
		{name: "missing release", os: "Linux", arch: "amd64", failure: "release", wantError: "Could not find the latest"},
		{name: "unsupported os", os: "FreeBSD", arch: "amd64", wantError: "supports macOS and Linux"},
		{name: "unsupported cpu", os: "Linux", arch: "armv7l", wantError: "supports amd64 and arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			binDir := filepath.Join(root, "tools")
			installDir := filepath.Join(root, "install with spaces")
			homeDir := filepath.Join(root, "home")
			if tc.defaultDir {
				installDir = filepath.Join(homeDir, ".local", "bin")
			}
			for _, dir := range []string{binDir, installDir} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			// Give the installer only these tools, including macOS's checksum fallback.
			checksumTool := "sha256sum"
			if tc.fallbackChecksum {
				checksumTool = "shasum"
			} else if _, err := exec.LookPath(checksumTool); err != nil {
				checksumTool = "shasum"
			}
			for _, name := range []string{"awk", "cp", "rm", "mktemp", "install", "mv", "mkdir", checksumTool} {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(binDir, name)); err != nil {
					t.Fatal(err)
				}
			}
			writeFile := func(path, content string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(filepath.Join(binDir, "uname"), "#!/bin/sh\ncase \"$1\" in -s) echo \"$TEST_OS\";; -m) echo \"$TEST_ARCH\";; esac\n", 0755)
			writeFile(filepath.Join(binDir, "curl"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    --proto|--retry|-w) shift 2 ;;
    --tlsv1.2|-fsSL) shift ;;
    -o) output=$2; shift 2 ;;
    https://*) url=$1; shift ;;
    *) echo "unexpected curl argument: $1" >&2; exit 1 ;;
  esac
done
printf '%s\n' "$url" >> "$TEST_ROOT/urls"
case "$url" in
  https://github.com/leodeim/brucli/releases/latest)
    if [ "$TEST_FAILURE" = release ]; then
      printf 'https://github.com/leodeim/brucli/releases'
    else
      printf 'https://github.com/leodeim/brucli/releases/tag/v1.2.3'
    fi ;;
  https://github.com/leodeim/brucli/releases/download/v1.2.3/checksums.txt)
    cp "$TEST_ROOT/checksums.txt" "$output" ;;
  "https://github.com/leodeim/brucli/releases/download/v1.2.3/$TEST_ASSET")
    if [ "$TEST_FAILURE" = download ]; then
      echo 'fixture download failed' >&2; exit 22
    fi
    cp "$TEST_ROOT/binary" "$output" ;;
  *) echo "unexpected download URL: $url" >&2; exit 1 ;;
esac
`, 0755)
			binary := "release binary fixture\n"
			checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(binary)), tc.asset)
			if tc.failure == "corrupt" {
				binary += "corrupted"
			}
			if tc.failure == "missing-checksum" {
				checksum = ""
			}
			writeFile(filepath.Join(root, "binary"), binary, 0644)
			writeFile(filepath.Join(root, "checksums.txt"), checksum, 0644)
			target := filepath.Join(installDir, "brucli")
			writeFile(target, "existing install", 0755)

			cmd := exec.Command("/bin/sh", "scripts/install.sh")
			cmd.Env = []string{
				"PATH=" + binDir, "HOME=" + homeDir, "TMPDIR=" + root,
				"TEST_ROOT=" + root, "TEST_OS=" + tc.os, "TEST_ARCH=" + tc.arch,
				"TEST_ASSET=" + tc.asset, "TEST_FAILURE=" + tc.failure,
			}
			if !tc.defaultDir {
				cmd.Env = append(cmd.Env, "INSTALL_DIR="+installDir)
			}
			out, err := cmd.CombinedOutput()
			if tc.wantError != "" {
				if err == nil || !strings.Contains(string(out), tc.wantError) {
					t.Fatalf("expected %q; error %v, output %s", tc.wantError, err, out)
				}
				binary = "existing install"
			} else {
				if err != nil {
					t.Fatalf("install: %v\n%s", err, out)
				}
				if !strings.Contains(string(out), "Installed brucli v1.2.3") || !strings.Contains(string(out), "to PATH") {
					t.Fatalf("missing install or PATH instructions: %s", out)
				}
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != 0755 {
					t.Fatalf("executable permissions: %v, %v", info, err)
				}
			}
			installed, err := os.ReadFile(target)
			if err != nil || string(installed) != binary {
				t.Fatalf("installed content %q, error %v", installed, err)
			}
			for _, pattern := range []string{filepath.Join(root, "brucli-install.*"), filepath.Join(installDir, ".brucli.*")} {
				matches, err := filepath.Glob(pattern)
				if err != nil || len(matches) != 0 {
					t.Fatalf("leftover installer files: %v, error %v", matches, err)
				}
			}
		})
	}
}
