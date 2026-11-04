package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUpdate(t *testing.T) {
	asset := "brucli_" + runtime.GOOS + "_" + runtime.GOARCH
	binary := []byte("new executable")
	checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(binary), asset)
	for _, test := range []struct {
		name       string
		current    string
		latest     string
		checksums  string
		status     int
		assetError bool
		wantError  string
		updated    bool
	}{
		{name: "patch", current: "v1.2.9", latest: "v1.2.10", checksums: checksum, updated: true},
		{name: "minor", current: "v1.2.9", latest: "v1.3.0", checksums: checksum, updated: true},
		{name: "major", current: "v1.9.9", latest: "v2.0.0", checksums: checksum, updated: true},
		{name: "current", current: "v1.2.10", latest: "v1.2.10"},
		{name: "no downgrade", current: "v2.0.0", latest: "v1.2.10"},
		{name: "checksum mismatch", current: "v1.0.0", latest: "v1.2.10", checksums: "bad  " + asset, wantError: "checksum verification failed"},
		{name: "missing checksum", current: "v1.0.0", latest: "v1.2.10", checksums: "bad  another_binary", wantError: "no checksum found"},
		{name: "no release", current: "v1.0.0", status: http.StatusNotFound, wantError: "fetch latest release: server returned 404"},
		{name: "missing binary", current: "v1.0.0", latest: "v1.2.10", checksums: checksum, assetError: true, wantError: "server returned 404"},
		{name: "invalid tag", current: "v1.0.0", latest: "../other", wantError: "invalid release tag"},
		{name: "development", current: "dev", wantError: "cannot update a development build"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var downloads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/latest" {
					if r.Header.Get("Accept") != "application/vnd.github+json" {
						t.Error("missing GitHub API accept header")
					}
					if test.status != 0 {
						w.WriteHeader(test.status)
						return
					}
					fmt.Fprintf(w, `{"tag_name":%q}`, test.latest)
					return
				}
				downloads.Add(1)
				switch r.URL.Path {
				case "/download/" + test.latest + "/checksums.txt":
					fmt.Fprint(w, test.checksums)
				case "/download/" + test.latest + "/" + asset:
					if test.assetError {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Write(binary)
				default:
					t.Errorf("unexpected download path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			dir := t.TempDir()
			target := filepath.Join(dir, "brucli")
			if err := os.WriteFile(target, []byte("old executable"), 0o751); err != nil {
				t.Fatal(err)
			}
			u := updater{client: srv.Client(), latestURL: srv.URL + "/latest", downloadURL: srv.URL + "/download/"}
			var out bytes.Buffer
			err := u.update(context.Background(), test.current, target, &out)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				if out.Len() != 0 {
					t.Fatalf("failed update printed success: %s", &out)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := "old executable"
			if test.updated {
				want = string(binary)
				if !strings.Contains(out.String(), "updated brucli to "+test.latest) {
					t.Fatalf("missing update confirmation: %s", &out)
				}
			} else if test.wantError == "" {
				if !strings.Contains(out.String(), "already up to date") || downloads.Load() != 0 {
					t.Fatalf("current version downloaded assets or missing confirmation: %s", &out)
				}
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != want {
				t.Fatalf("installed content = %q, %v; want %q", got, err, want)
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0o751 {
				t.Fatalf("permissions changed: %v, %v", info, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary files left behind: %v, %v", entries, err)
			}
		})
	}
}

func TestReplaceBinarySymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "binary")
	link := filepath.Join(dir, "brucli")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceBinary(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v, %v", info, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new" {
		t.Fatalf("target content = %q, %v", data, err)
	}
}

func TestReplaceBinaryPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write read-only directories")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "brucli")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := replaceBinary(target, []byte("new")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v, want permission error", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "old" {
		t.Fatalf("target content = %q, %v", data, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
}

func TestDownloadLimitsAndCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "longer than limit")
	}))
	defer srv.Close()
	u := updater{client: srv.Client()}
	if _, err := u.download(context.Background(), srv.URL, 4); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized download: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := u.download(ctx, srv.URL, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download: %v", err)
	}
}

func TestVersionParts(t *testing.T) {
	for _, value := range []string{"v1.2.3", "0.0.0", "v10.20.30"} {
		if _, valid := versionParts(value); !valid {
			t.Errorf("valid version rejected: %q", value)
		}
	}
	for _, value := range []string{"dev", "", "v1.2", "v1.2.3.4", "v1.-2.3", "v1.+2.3", "v01.2.3", "v1.2.3/other", "v1.2.3-rc.1", "v1.2.18446744073709551616"} {
		if _, valid := versionParts(value); valid {
			t.Errorf("invalid version accepted: %q", value)
		}
	}
}

func TestUpdateAndVersionCLI(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"update", "--help"}, 0},
		{[]string{"update", "extra"}, 2},
		{[]string{"update", "--unknown"}, 2},
		{[]string{"update"}, 1}, // Development builds must fail before downloading anything.
		{[]string{"--version"}, 0},
		{[]string{"-version"}, 0},
		{[]string{"version"}, 0},
		{[]string{"version", "extra"}, 2},
	} {
		var out, errOut bytes.Buffer
		code, err := run(test.args, &out, &errOut)
		if code != test.code {
			t.Fatalf("%v: code %d, error %v; want %d", test.args, code, err, test.code)
		}
		if test.args[0] == "version" || strings.Contains(test.args[0], "-version") {
			if code == 0 && out.String() != "brucli "+buildVersion()+"\n" {
				t.Fatalf("version output: %q", out.String())
			}
		} else if len(test.args) == 1 && (err == nil || !strings.Contains(err.Error(), "development build")) {
			t.Fatalf("development update error: %v", err)
		} else if len(test.args) == 2 && test.args[1] == "--help" && !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("update help: %v", err)
		}
	}
}
