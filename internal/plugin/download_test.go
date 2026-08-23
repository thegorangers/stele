package plugin_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/plugin"
)

// A plugin that is not a Go program cannot be installed by a Go toolchain, but
// it can still be pinned: the manifest names the published binary and the
// sha256 it must have. Everything here is served from a local test server, so
// the tests never touch the network.

// script is a tiny executable whose output identifies it, so that a test can
// assert not only that the file is there but that it is the file that was
// served.
const script = "#!/bin/sh\necho served-plugin\n"

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// serve starts a server that answers every request with body, and reports how
// many requests it received.
func serve(t *testing.T, body []byte) (url string, hits *int, close func()) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/protoc-gen-dart", &n, srv.Close
}

func TestCache_EnsureURLDownloadsABareBinary(t *testing.T) {
	body := []byte(script)
	url, hits, _ := serve(t, body)
	cache := plugin.Cache{Root: t.TempDir()}

	bin, err := cache.EnsureURL(context.Background(), url, sum(body), "")
	if err != nil {
		t.Fatalf("EnsureURL: %v", err)
	}
	if !strings.HasPrefix(bin, cache.Root) {
		t.Errorf("binary %q is not in the cache %q", bin, cache.Root)
	}
	got, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("running the downloaded plugin: %v", err)
	}
	if strings.TrimSpace(string(got)) != "served-plugin" {
		t.Errorf("the cached binary printed %q, want the served one", got)
	}
	if *hits != 1 {
		t.Errorf("%d requests, want 1", *hits)
	}
}

// The same rule as for a Go plugin: once cached, a run needs no network.
func TestCache_EnsureURLIsOfflineOnceCached(t *testing.T) {
	body := []byte(script)
	url, _, stop := serve(t, body)
	cache := plugin.Cache{Root: t.TempDir()}
	if _, err := cache.EnsureURL(context.Background(), url, sum(body), ""); err != nil {
		t.Fatalf("EnsureURL: %v", err)
	}
	stop() // nothing may reach the server again
	if _, err := cache.EnsureURL(context.Background(), url, sum(body), ""); err != nil {
		t.Fatalf("EnsureURL from a warm cache: %v", err)
	}
}

// The hash is checked before the bytes are used, not after: a download that
// does not match must never be executed and must never reach the cache.
func TestCache_EnsureURLRefusesAMismatchedHash(t *testing.T) {
	served := []byte("#!/bin/sh\ntouch " + filepath.Join(t.TempDir(), "ran") + "\n")
	url, _, _ := serve(t, served)
	root := t.TempDir()
	cache := plugin.Cache{Root: root}

	declared := sum([]byte("something else entirely"))
	bin, err := cache.EnsureURL(context.Background(), url, declared, "")
	if err == nil {
		t.Fatalf("EnsureURL: expected a refusal, got %q", bin)
	}
	for _, want := range []string{declared, sum(served)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	// Nothing may be left behind: a cache entry is what a later run trusts
	// without checking the network again.
	var found []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("the cache holds %v after a failed verification, want nothing", found)
	}
}

func TestCache_EnsureURLExtractsFromArchives(t *testing.T) {
	for _, tc := range []struct {
		name    string
		suffix  string
		archive []byte
	}{
		{"tar.gz", ".tar.gz", targz(t, "bin/protoc-gen-dart", script)},
		{"zip", ".zip", zipped(t, "bin/protoc-gen-dart", script)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(tc.archive)
			}))
			defer srv.Close()
			cache := plugin.Cache{Root: t.TempDir()}
			bin, err := cache.EnsureURL(context.Background(), srv.URL+"/dart"+tc.suffix, sum(tc.archive), "bin/protoc-gen-dart")
			if err != nil {
				t.Fatalf("EnsureURL: %v", err)
			}
			got, err := exec.Command(bin).Output()
			if err != nil {
				t.Fatalf("running the extracted plugin: %v", err)
			}
			if strings.TrimSpace(string(got)) != "served-plugin" {
				t.Errorf("the extracted binary printed %q, want the served one", got)
			}
		})
	}
}

// An archive with no member named is refused with a message that says what to
// write, rather than handing the archive to the operating system as if it were
// a program.
func TestCache_EnsureURLArchiveWithoutAMemberIsExplicit(t *testing.T) {
	archive := targz(t, "bin/protoc-gen-dart", script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()
	cache := plugin.Cache{Root: t.TempDir()}
	_, err := cache.EnsureURL(context.Background(), srv.URL+"/dart.tar.gz", sum(archive), "")
	if err == nil {
		t.Fatal("EnsureURL: expected an error naming archive_path")
	}
	if !strings.Contains(err.Error(), "archive_path") {
		t.Errorf("error %q does not say which field to write", err)
	}
}

func TestCache_EnsureURLMissingMemberNamesTheArchiveContents(t *testing.T) {
	archive := targz(t, "bin/protoc-gen-dart", script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()
	cache := plugin.Cache{Root: t.TempDir()}
	_, err := cache.EnsureURL(context.Background(), srv.URL+"/dart.tar.gz", sum(archive), "bin/wrong")
	if err == nil {
		t.Fatal("EnsureURL: expected an error")
	}
	if !strings.Contains(err.Error(), "bin/protoc-gen-dart") {
		t.Errorf("error %q does not say what the archive holds", err)
	}
}

func targz(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipped(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
