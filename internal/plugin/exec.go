// Package plugin runs a code generation plugin over a CodeGeneratorRequest.
//
// The protocol is the one protoc has always spoken and every published plugin
// already implements: the request arrives on stdin as a serialised
// CodeGeneratorRequest, the response leaves on stdout as a serialised
// CodeGeneratorResponse, and stderr belongs to the plugin's own diagnostics.
// Nothing here is invented; inventing any of it would cut this tool off from
// the plugins that are the whole reason it does not have to generate code
// itself.
//
// The care in this file is in the failure paths rather than in the happy one.
// A plugin can fail in four different ways — it is not installed, it dies, it
// writes something that is not a response, or it returns a response whose
// error field is set — and each has a different fix. A message that could not
// tell them apart would send the reader looking in the wrong place, so each
// one is reported distinctly and every one of them names the plugin.
package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

// stderrLimit caps how much of a plugin's diagnostics is quoted back. A plugin
// that dies mid-generation can print a great deal; the tail of it is what says
// why, and the rest would bury the error that carries it.
const stderrLimit = 4 << 10

// Run executes bin, handing it req and returning what it answered.
//
// bin is either a name to be found on PATH or a path to an executable, exactly
// as the manifest wrote it.
//
// A response whose error field is set is returned as an error, not as a
// response: the field is the protocol's way of failing, and a plugin that uses
// it exits zero, so a caller that only checked the exit status would report a
// failed generation as a success.
func Run(ctx context.Context, bin string, req *pluginpb.CodeGeneratorRequest) (*pluginpb.CodeGeneratorResponse, error) {
	if bin == "" {
		return nil, errors.New("plugin: no plugin named")
	}
	if req == nil {
		return nil, fmt.Errorf("plugin %s: no request", name(bin))
	}

	path, err := exec.LookPath(bin)
	if err != nil {
		if strings.ContainsRune(bin, filepath.Separator) {
			return nil, fmt.Errorf("plugin %s: %w", name(bin), err)
		}
		return nil, fmt.Errorf("plugin %s: not found in PATH: %w", name(bin), err)
	}

	in, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: encoding the request: %w", name(bin), err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Cancellation is reported as cancellation. The exit status a killed
		// process leaves behind says nothing about the plugin, and blaming it
		// for the caller's own interruption would send the reader hunting a
		// bug that is not there.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("plugin %s: %w", name(bin), ctxErr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("plugin %s: exit status %d%s",
				name(bin), exitErr.ExitCode(), diagnostics(stderr.Bytes()))
		}
		return nil, fmt.Errorf("plugin %s: %w%s", name(bin), err, diagnostics(stderr.Bytes()))
	}

	var resp pluginpb.CodeGeneratorResponse
	if err := proto.Unmarshal(stdout.Bytes(), &resp); err != nil {
		// The byte count is part of the message because the commonest cause is
		// a plugin that printed help, or a wrapper script that printed
		// anything at all, on stdout instead of stderr.
		return nil, fmt.Errorf("plugin %s: the %d bytes it wrote to stdout are not a CodeGeneratorResponse "+
			"(a plugin must write nothing but the response to stdout): %w%s",
			name(bin), stdout.Len(), err, diagnostics(stderr.Bytes()))
	}
	if e := resp.GetError(); e != "" {
		return nil, fmt.Errorf("plugin %s: %s%s", name(bin), e, diagnostics(stderr.Bytes()))
	}
	return &resp, nil
}

// name is the plugin as the reader should see it: the manifest's own spelling,
// quoted, so that a bare name and a path are both unambiguous.
func name(bin string) string { return fmt.Sprintf("%q", bin) }

// diagnostics renders the plugin's stderr for an error message, or nothing
// when it said nothing.
func diagnostics(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return ""
	}
	if len(b) > stderrLimit {
		b = append([]byte("…"), b[len(b)-stderrLimit:]...)
	}
	return "\n" + string(b)
}
