package plugin_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thegorangers/stele/internal/plugin"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// The tests need plugin binaries that behave badly on purpose. Rather than
// depend on anything installed, the test binary re-executes itself: with
// fakeModeEnv set it stops being a test and starts being a plugin. That keeps
// the tests hermetic and offline, and costs no build step.
const fakeModeEnv = "STELE_TEST_FAKE_PLUGIN"

func TestMain(m *testing.M) {
	if mode, ok := os.LookupEnv(fakeModeEnv); ok {
		os.Exit(fakePlugin(mode))
	}
	os.Exit(m.Run())
}

// fakePlugin implements one badly-behaved plugin, selected by mode.
func fakePlugin(mode string) int {
	verb, arg, _ := strings.Cut(mode, ":")
	switch verb {
	case "error":
		// The protocol's own failure channel: exit zero, report in the field.
		emit(&pluginpb.CodeGeneratorResponse{Error: proto.String(arg)})
		return 0
	case "file":
		emit(&pluginpb.CodeGeneratorResponse{File: []*pluginpb.CodeGeneratorResponse_File{{
			Name: proto.String(arg), Content: proto.String("generated\n"),
		}}})
		return 0
	case "echo-request":
		// Writes the request it was given to the file named by arg, so a test
		// can assert on what the tool sent.
		in, _ := os.ReadFile("/dev/stdin")
		_ = os.WriteFile(arg, in, 0o600)
		emit(&pluginpb.CodeGeneratorResponse{File: []*pluginpb.CodeGeneratorResponse_File{{
			Name: proto.String("out.txt"), Content: proto.String("x"),
		}}})
		return 0
	case "cache-url", "cache-install":
		// Not a plugin at all: a second *process* using the plugin cache, so
		// that concurrency can be measured between processes rather than
		// between goroutines. That is the shape the cache is built for — a CI
		// runner with several jobs on one shared HOME — and it is not the same
		// as concurrency inside one process: stele has no goroutines, and a
		// test that forked and exec'd while writing would be measuring the
		// runtime's fd inheritance rather than this cache.
		return cacheWorker(verb, strings.Split(arg, "|"))
	case "exit":
		fmt.Fprintln(os.Stderr, "the plugin says why it died")
		return 3
	case "garbage":
		fmt.Fprint(os.Stdout, "this is not a protobuf message at all")
		return 0
	case "sleep":
		time.Sleep(30 * time.Second)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown fake plugin mode %q\n", mode)
	return 2
}

// cacheWorker performs one cache operation and prints the path it resolved to.
//
// It exists so that concurrency can be measured between processes rather than
// between goroutines. Between processes is the shape the cache is built for —
// a CI runner with several jobs on one shared HOME — and it is not the same as
// concurrency inside one process: stele has no goroutines at all, and a test
// that forked and exec'd a binary while another goroutine was writing one
// would be measuring the Go runtime's file-descriptor inheritance rather than
// this cache.
func cacheWorker(verb string, args []string) int {
	c := plugin.Cache{Root: args[0]}
	var p string
	var err error
	if verb == "cache-url" {
		p, err = c.EnsureURL(context.Background(), args[1], args[2], args[3])
	} else {
		p, err = c.Ensure(context.Background(), args[1], args[2])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(p)
	return 0
}

func emit(resp *pluginpb.CodeGeneratorResponse) {
	b, err := proto.Marshal(resp)
	if err != nil {
		panic(err)
	}
	if _, err := os.Stdout.Write(b); err != nil {
		panic(err)
	}
}

// fake returns a command line for the fake plugin in the given mode: the path
// of a wrapper script that re-executes this test binary.
func fake(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "protoc-gen-fake")
	script := fmt.Sprintf("#!/bin/sh\nexec env %s=%q %q \"$@\"\n", fakeModeEnv, mode, self)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// request returns a minimal but valid request.
func request() *pluginpb.CodeGeneratorRequest {
	return &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"acme/v1/a.proto"},
		ProtoFile: []*descriptorpb.FileDescriptorProto{{
			Name:    proto.String("acme/v1/a.proto"),
			Package: proto.String("acme.v1"),
			Syntax:  proto.String("proto3"),
		}},
	}
}

// The protocol reports failure in a field of a response returned with exit
// code zero. Dropping it turns a failed generation into a silent success.
func TestRun_ErrorFieldSurfaces(t *testing.T) {
	_, err := plugin.Run(context.Background(), fake(t, "error:boom"), request())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("the plugin's own error must reach the user, got %v", err)
	}
	if !strings.Contains(err.Error(), "protoc-gen-fake") {
		t.Fatalf("error %q does not name the plugin", err)
	}
}

func TestRun_ReturnsFiles(t *testing.T) {
	resp, err := plugin.Run(context.Background(), fake(t, "file:acme/v1/a.pb.go"), request())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.File) != 1 || resp.File[0].GetName() != "acme/v1/a.pb.go" {
		t.Fatalf("response = %v", resp)
	}
}

func TestRun_SendsTheRequestOnStdin(t *testing.T) {
	sent := filepath.Join(t.TempDir(), "req.bin")
	if _, err := plugin.Run(context.Background(), fake(t, "echo-request:"+sent), request()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sent)
	if err != nil {
		t.Fatal(err)
	}
	var got pluginpb.CodeGeneratorRequest
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the plugin did not receive a CodeGeneratorRequest: %v", err)
	}
	if len(got.FileToGenerate) != 1 || got.FileToGenerate[0] != "acme/v1/a.proto" {
		t.Fatalf("file_to_generate = %v", got.FileToGenerate)
	}
}

// The three ways a plugin can fail outside the protocol must be told apart:
// each one has a different fix, and a shared message would hide which.
func TestRun_FailureModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		bin  func(t *testing.T) string
		want []string
	}{
		{
			name: "missing from PATH",
			bin:  func(t *testing.T) string { return "protoc-gen-nosuch-plugin" },
			want: []string{"protoc-gen-nosuch-plugin", "PATH"},
		},
		{
			name: "exits non-zero",
			bin:  func(t *testing.T) string { return fake(t, "exit") },
			want: []string{"protoc-gen-fake", "exit", "the plugin says why it died"},
		},
		{
			name: "writes garbage to stdout",
			bin:  func(t *testing.T) string { return fake(t, "garbage") },
			want: []string{"protoc-gen-fake", "stdout"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plugin.Run(context.Background(), tc.bin(t), request())
			if err == nil {
				t.Fatal("want an error, got none")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestRun_HonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := plugin.Run(ctx, fake(t, "sleep"), request())
	if err == nil {
		t.Fatal("want an error after cancellation, got none")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v does not carry context.Canceled", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("cancellation took %v; the plugin was not stopped", d)
	}
}

// A plugin named by path rather than by PATH lookup must work: configs point
// at binaries in a repository's own toolchain directory.
func TestRun_AcceptsAPath(t *testing.T) {
	bin := fake(t, "file:a.txt")
	if _, err := exec.LookPath(bin); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if _, err := plugin.Run(context.Background(), bin, request()); err != nil {
		t.Fatal(err)
	}
}
