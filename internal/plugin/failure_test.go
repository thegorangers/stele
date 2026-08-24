package plugin_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thegorangers/stele/internal/plugin"
)

// Milestone 5 for the plugin cache: what an interrupted install, an
// interrupted download, a write that cannot complete and two concurrent runs
// leave behind. The rule under test is one rule — an entry that reaches the
// cache is an entry a later run trusts without asking anything, so nothing
// unverified may ever become one.

// targzTwo builds an archive holding two executable members, which is how
// several published plugin releases actually ship: one tarball, two binaries.
func targzTwo(t *testing.T, a, aBody, b, bBody string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range []struct{ name, body string }{{a, aBody}, {b, bBody}} {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o755, Size: int64(len(m.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestCache_TwoMembersOfOneArchiveDoNotEvictEachOther is the shape a
// content-addressed cache keyed by the *archive's* digest gets wrong: the two
// binaries in one release tarball share the digest, so anything recorded per
// digest rather than per member is written twice and describes one of them.
func TestCache_TwoMembersOfOneArchiveDoNotEvictEachOther(t *testing.T) {
	const goBody = "#!/bin/sh\necho protoc-gen-go\n"
	const grpcBody = "#!/bin/sh\necho protoc-gen-go-grpc\n"
	archive := targzTwo(t, "bin/protoc-gen-go", goBody, "bin/protoc-gen-go-grpc", grpcBody)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()
	url := srv.URL + "/release.tar.gz"
	digest := sum(archive)
	cache := plugin.Cache{Root: t.TempDir()}

	first, err := cache.EnsureURL(context.Background(), url, digest, "bin/protoc-gen-go")
	if err != nil {
		t.Fatalf("first member: %v", err)
	}
	if _, err := cache.EnsureURL(context.Background(), url, digest, "bin/protoc-gen-go-grpc"); err != nil {
		t.Fatalf("second member: %v", err)
	}
	// Using the first one again is what a second repository in the same CI run
	// does. It must still be usable, and it must still be the right binary.
	again, err := cache.EnsureURL(context.Background(), url, digest, "bin/protoc-gen-go")
	if err != nil {
		t.Fatalf("the first member became unusable once its sibling was cached: %v", err)
	}
	if again != first {
		t.Fatalf("EnsureURL = %q, want the entry it wrote first, %q", again, first)
	}
	out, err := exec.Command(again).Output()
	if err != nil {
		t.Fatalf("running the cached plugin: %v", err)
	}
	if strings.TrimSpace(string(out)) != "protoc-gen-go" {
		t.Fatalf("the cached entry is %q, not the member that was asked for", strings.TrimSpace(string(out)))
	}
}

// stagingLeftovers lists staging directories still in the cache. A failure that
// leaves one behind is not a correctness defect on its own, but it is unbounded
// growth on a machine that fails repeatedly.
func stagingLeftovers(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".staging-") {
			out = append(out, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// cachedFiles lists every regular file under the cache root, so a test can
// assert on what a failed run published rather than on the one path it knows.
func cachedFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			out = append(out, rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCache_DownloadClosedMidResponsePublishesNothing serves a truncated
// answer and closes the connection. The digest cannot match half a file, and
// half a file must not be on disk for a later run to find.
func TestCache_DownloadClosedMidResponsePublishesNothing(t *testing.T) {
	body := []byte(script + strings.Repeat("# padding\n", 20000))
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		if served == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			w.Write(body[:len(body)/2])
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
				}
			}
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	url := srv.URL + "/protoc-gen-dart"
	cache := plugin.Cache{Root: t.TempDir()}

	if _, err := cache.EnsureURL(context.Background(), url, sum(body), ""); err == nil {
		t.Fatal("a truncated download must not be accepted")
	}
	if files := cachedFiles(t, cache.Root); len(files) > 0 {
		t.Fatalf("a truncated download published %v", files)
	}
	if dirs := stagingLeftovers(t, cache.Root); len(dirs) > 0 {
		t.Errorf("staging directories left behind: %v", dirs)
	}
	// And the run after it works.
	bin, err := cache.EnsureURL(context.Background(), url, sum(body), "")
	if err != nil {
		t.Fatalf("the run after a truncated download must succeed: %v", err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
}

// TestCache_DownloadCancelledMidResponsePublishesNothing interrupts the run
// itself rather than the server: the context is cancelled while the body is
// still arriving.
func TestCache_DownloadCancelledMidResponsePublishesNothing(t *testing.T) {
	body := []byte(script + strings.Repeat("# padding\n", 200000))
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(body[:1024])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		cancel()
		// Hold the connection open so the cancellation, not the end of the
		// body, is what ends the read.
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	cache := plugin.Cache{Root: t.TempDir()}

	_, err := cache.EnsureURL(ctx, srv.URL+"/protoc-gen-dart", sum(body), "")
	if err == nil {
		t.Fatal("a cancelled download must not be accepted")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if files := cachedFiles(t, cache.Root); len(files) > 0 {
		t.Fatalf("a cancelled download published %v", files)
	}
	if dirs := stagingLeftovers(t, cache.Root); len(dirs) > 0 {
		t.Errorf("staging directories left behind: %v", dirs)
	}
}

// TestCache_UnreachableServerSaysWhatToDo is the download half of a cold cache
// with no network: the address is answered by nothing at all.
func TestCache_UnreachableServerSaysWhatToDo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String() + "/protoc-gen-dart"
	ln.Close() // nothing is listening there now

	cache := plugin.Cache{Root: t.TempDir()}
	_, err = cache.EnsureURL(context.Background(), url, strings.Repeat("0", 64), "")
	if err == nil {
		t.Fatal("want an error when nothing answers")
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("the message must name the url; got %q", err)
	}
	if !strings.Contains(err.Error(), "cached") {
		t.Errorf("the message must say what the cache means for this failure; got %q", err)
	}
	if files := cachedFiles(t, cache.Root); len(files) > 0 {
		t.Fatalf("a failed download published %v", files)
	}
}

// TestCache_DownloadCannotWriteSaysSo is the closest this suite gets to a full
// disk. A genuine ENOSPC cannot be produced portably without a filesystem of
// its own and privileges to mount it, so the write is made to fail at the same
// syscall for the neighbouring reason: the cache directory cannot be written
// to. What that weakens is stated where it matters — this does not exercise a
// *partial* write, only a refused one. The partial case is covered for real on
// the fetch side, where a clone is interrupted with its tree half laid out.
func TestCache_DownloadCannotWriteSaysSo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions do not refuse a write")
	}
	body := []byte(script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(plugins, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(plugins, 0o755) })
	cache := plugin.Cache{Root: root}

	url := srv.URL + "/protoc-gen-dart"
	_, err := cache.EnsureURL(context.Background(), url, sum(body), "")
	if err == nil {
		t.Fatal("want an error when the cache cannot be written to")
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("the message must name what was being downloaded; got %q", err)
	}
	if files := cachedFiles(t, root); len(files) > 0 {
		t.Fatalf("a download that could not be written published %v", files)
	}
}

// TestCache_ConcurrentDownloadsOfOneDigest is several runs downloading the
// same pinned binary into one cold cache at the same time. The binaries are
// not executed here — that is the next test's job, in the process shape where
// it means something — but every run must come back with the published entry
// and the bytes that were pinned.
func TestCache_ConcurrentDownloadsOfOneDigest(t *testing.T) {
	body := []byte(script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	cache := plugin.Cache{Root: t.TempDir()}
	url := srv.URL + "/protoc-gen-dart"

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bin, err := cache.EnsureURL(context.Background(), url, sum(body), "")
			if err != nil {
				errs <- err
				return
			}
			got, err := os.ReadFile(bin)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, body) {
				errs <- fmt.Errorf("the cached entry is not the pinned bytes: %q", got)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// worker starts this test binary again as a plain process using the cache, and
// returns the command ready to run.
func worker(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fakeModeEnv+"="+mode)
	return cmd
}

// runWorkers starts n processes at once and returns what each printed,
// failing the test on the first one that could not do its job.
func runWorkers(t *testing.T, n int, mode string) []string {
	t.Helper()
	type outcome struct {
		out string
		err error
	}
	results := make([]outcome, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := worker(t, mode)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			<-start
			out, err := cmd.Output()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, stderr.String())
			}
			results[i] = outcome{strings.TrimSpace(string(out)), err}
		}()
	}
	close(start)
	wg.Wait()
	paths := make([]string, 0, n)
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("a concurrent run failed: %v", r.err)
		}
		paths = append(paths, r.out)
	}
	return paths
}

// TestCache_ConcurrentProcessesDownloadOneDigest is the real shape of the
// concurrency this cache exists to survive: several stele processes on one
// machine, sharing one cache root, all needing a plugin that is not there yet.
// Each must end up with the same entry, and the entry must be runnable.
func TestCache_ConcurrentProcessesDownloadOneDigest(t *testing.T) {
	body := []byte(script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	root := t.TempDir()
	url := srv.URL + "/protoc-gen-dart"

	paths := runWorkers(t, 6, strings.Join([]string{"cache-url:" + root, url, sum(body), ""}, "|"))
	for _, p := range paths {
		if p != paths[0] {
			t.Fatalf("two runs resolved the same pin to %q and %q", p, paths[0])
		}
		out, err := exec.Command(p).Output()
		if err != nil {
			t.Fatalf("the entry several runs wrote is not runnable: %v", err)
		}
		if strings.TrimSpace(string(out)) != "served-plugin" {
			t.Fatalf("the entry printed %q", out)
		}
	}
}

// TestCache_ConcurrentProcessesInstallOnePlugin is the same question for the
// managed tier, where the writer is the Go toolchain rather than this package.
func TestCache_ConcurrentProcessesInstallOnePlugin(t *testing.T) {
	hermeticGo(t)
	root := t.TempDir()

	paths := runWorkers(t, 4, strings.Join([]string{"cache-install:" + root, fakeModule, fakeVersion}, "|"))
	for _, p := range paths {
		if p != paths[0] {
			t.Fatalf("two runs installed the same version to %q and %q", p, paths[0])
		}
	}
	out, err := exec.Command(paths[0]).Output()
	if err != nil {
		t.Fatalf("the entry several runs installed is not runnable: %v", err)
	}
	if strings.TrimSpace(string(out)) != "fake plugin" {
		t.Fatalf("the installed plugin printed %q", out)
	}
}

// TestCache_InterruptedInstallIsNotACacheHit cancels an install while the
// toolchain is running. A half-written binary that reached the cache would be
// trusted by every later run without reinstalling, so what must be on disk
// afterwards is nothing.
func TestCache_InterruptedInstallIsNotACacheHit(t *testing.T) {
	hermeticGo(t)
	cache := plugin.Cache{Root: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if len(stagingLeftovers(t, cache.Root)) > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	_, err := cache.Ensure(ctx, fakeModule, fakeVersion)
	wg.Wait()
	if err == nil {
		t.Skip("the install finished before it could be interrupted; nothing to assert")
	}
	if _, statErr := os.Stat(cache.Path(fakeModule, fakeVersion)); statErr == nil {
		t.Fatal("an interrupted install published a binary a later run would trust")
	}
	if dirs := stagingLeftovers(t, cache.Root); len(dirs) > 0 {
		t.Errorf("staging directories left behind: %v", dirs)
	}

	bin, err := cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err != nil {
		t.Fatalf("the run after an interrupted install must succeed: %v", err)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "fake plugin" {
		t.Fatalf("the reinstalled plugin printed %q", out)
	}
}

// TestCache_FailedInstallPublishesNothing is the cold cache with no network on
// the managed tier: the proxy is off, so the version cannot be fetched. The
// message has to name the plugin and say where the network is needed, and the
// cache has to be as empty afterwards as it was before.
func TestCache_FailedInstallPublishesNothing(t *testing.T) {
	hermeticGo(t)
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	cache := plugin.Cache{Root: t.TempDir()}

	_, err := cache.Ensure(context.Background(), fakeModule, "v9.9.9")
	if err == nil {
		t.Fatal("want an error when the version cannot be fetched")
	}
	for _, want := range []string{fakeModule, "v9.9.9", "network", "cached"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message must mention %q; got %q", want, err)
		}
	}
	if _, statErr := os.Stat(cache.Path(fakeModule, "v9.9.9")); statErr == nil {
		t.Error("a failed install published a binary")
	}
	if dirs := stagingLeftovers(t, cache.Root); len(dirs) > 0 {
		t.Errorf("staging directories left behind: %v", dirs)
	}
}
