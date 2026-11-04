package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// version is replaced with the Git tag in release builds.
var version = "dev"

func buildVersion() string {
	if version != "dev" {
		return version
	}
	// go install records the module version without our release linker flags.
	if info, ok := debug.ReadBuildInfo(); ok {
		if _, valid := versionParts(info.Main.Version); valid {
			return info.Main.Version
		}
	}
	return version
}

func runUpdate(args []string, stdout, stderr io.Writer) (int, error) {
	flags := flag.NewFlagSet("brucli update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprintln(stderr, "usage: brucli update\nUpdate to the latest release.") }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, err
		}
		return 2, err
	}
	if flags.NArg() != 0 {
		return 2, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	executable, err := os.Executable()
	if err != nil {
		return 1, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := updater{
		client:      http.DefaultClient,
		latestURL:   "https://api.github.com/repos/leodeim/brucli/releases/latest",
		downloadURL: "https://github.com/leodeim/brucli/releases/download/",
	}
	if err := u.update(ctx, buildVersion(), executable, stdout); err != nil {
		return 1, err
	}
	return 0, nil
}

type updater struct {
	client      *http.Client
	latestURL   string
	downloadURL string
}

func (u updater) update(ctx context.Context, current, executable string, out io.Writer) error {
	currentParts, valid := versionParts(current)
	if !valid {
		return errors.New("cannot update a development build; install a release binary first")
	}
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return fmt.Errorf("updates are not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	data, err := u.download(ctx, u.latestURL, 1<<20)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &release); err != nil {
		return fmt.Errorf("read latest release: %w", err)
	}
	latestParts, valid := versionParts(release.TagName)
	if !valid {
		return fmt.Errorf("invalid release tag %q", release.TagName)
	}
	newer := false
	for i := range latestParts {
		if latestParts[i] != currentParts[i] {
			newer = latestParts[i] > currentParts[i]
			break
		}
	}
	if !newer {
		_, err := fmt.Fprintf(out, "brucli %s is already up to date\n", current)
		return err
	}

	asset := "brucli_" + runtime.GOOS + "_" + runtime.GOARCH
	base := u.downloadURL + release.TagName + "/"
	checksums, err := u.download(ctx, base+"checksums.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	binary, err := u.download(ctx, base+asset, 100<<20)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	if err := verifyChecksum(asset, binary, checksums); err != nil {
		return err
	}
	target, err := replaceBinary(executable, binary)
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("cannot replace %s; install brucli in a writable directory: %w", target, err)
	}
	if err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	_, err = fmt.Fprintf(out, "updated brucli to %s at %s\n", release.TagName, target)
	return err
}

func versionParts(value string) ([3]uint64, bool) {
	var parts [3]uint64
	values := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(values) != len(parts) {
		return parts, false
	}
	for i, value := range values {
		if value == "" || (len(value) > 1 && value[0] == '0') {
			return parts, false
		}
		for _, char := range value {
			if char < '0' || char > '9' {
				return parts, false
			}
		}
		number, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return parts, false
		}
		parts[i] = number
	}
	return parts, true
}

func (u updater) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "brucli")
	if url == u.latestURL {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return data, nil
}

func verifyChecksum(filename string, data, checksums []byte) error {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		sum := sha256.Sum256(data)
		if !strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
			return fmt.Errorf("checksum verification failed for %s", filename)
		}
		return nil
	}
	return fmt.Errorf("no checksum found for %s", filename)
}

// replaceBinary follows symlinks and renames a file in the same directory so
// a failed update leaves the installed executable intact.
func replaceBinary(path string, data []byte) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return target, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".brucli-update-*")
	if err != nil {
		return target, err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return target, err
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		return target, err
	}
	if err := temporary.Sync(); err != nil {
		return target, err
	}
	if err := temporary.Close(); err != nil {
		return target, err
	}
	return target, os.Rename(temporary.Name(), target)
}
