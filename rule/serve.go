package rule

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Serve runs rules as a rule plugin, speaking the protocol on stdin and
// stdout. It is what the main function of a rule binary calls:
//
//	func main() {
//		if err := rule.Serve(myRule{}); err != nil {
//			fmt.Fprintln(os.Stderr, err)
//			os.Exit(1)
//		}
//	}
//
// It exists so that writing a rule is writing a Check method. An author who
// had to implement the framing themselves would be implementing this tool's
// private details, and every one of them would get the failure paths slightly
// differently wrong.
//
// Serve returns when the host closes the connection, which it does at the end
// of a run. A rule that panics is left to panic: the process dies, the host
// reports which rule was checking which file, and a rule that swallowed its
// own panic would answer with silence and be read as a clean file.
func Serve(rules ...Rule) error { return serve(os.Stdin, os.Stdout, os.Getenv(ProtocolEnv), rules) }

func serve(in io.Reader, out io.Writer, env string, rules []Rule) error {
	if len(rules) == 0 {
		return errors.New("stele rule: Serve was given no rules")
	}
	if env == "" {
		// The commonest way to see this message is running the binary by
		// hand, so it says what the binary is rather than only refusing.
		return fmt.Errorf("stele rule: this is a stele lint rule plugin, not a command. "+
			"It speaks a protocol on stdin and stdout and is run by `stele lint`. "+
			"Declare it under lint.plugins in stele.yaml. (%s is unset)", ProtocolEnv)
	}
	if env != fmt.Sprint(Protocol) {
		return fmt.Errorf("stele rule: the host speaks protocol %s and this rule speaks %d; "+
			"upgrade whichever of the two is older", env, Protocol)
	}

	byID := make(map[string]Rule, len(rules))
	hello := Hello{Protocol: Protocol}
	for _, r := range rules {
		id := r.ID()
		if err := CheckID(id); err != nil {
			return fmt.Errorf("stele rule: %w", err)
		}
		if _, dup := byID[id]; dup {
			return fmt.Errorf("stele rule: two rules in this binary claim the ID %q; an ID names one rule", id)
		}
		byID[id] = r
		hello.Rules = append(hello.Rules, Descriptor{ID: id, Description: r.Description()})
	}

	w := bufio.NewWriter(out)
	if err := WriteFrame(w, hello); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}

	br := bufio.NewReader(in)
	for {
		var req Request
		if err := ReadFrame(br, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("stele rule: reading a request: %w", err)
		}
		resp := answer(byID, req)
		if err := WriteFrame(w, resp); err != nil {
			return fmt.Errorf("stele rule: writing an answer: %w", err)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
}

// answer runs one request. Every way it can go wrong sets Error, because the
// alternative — an empty finding list — is indistinguishable from a file that
// is clean.
func answer(byID map[string]Rule, req Request) Response {
	r, ok := byID[req.Rule]
	if !ok {
		return Response{Error: fmt.Sprintf("this binary serves no rule %q", req.Rule)}
	}
	f, err := fileOf(req)
	if err != nil {
		return Response{Error: err.Error()}
	}
	found, err := r.Check(f)
	if err != nil {
		return Response{Error: err.Error()}
	}
	resp := Response{}
	for _, fi := range found {
		resp.Findings = append(resp.Findings, WireFinding{
			Line: fi.Pos.Line, Col: fi.Pos.Col, Message: fi.Message, Fix: fi.Fix,
		})
	}
	return resp
}

// fileOf rebuilds the linked file from the descriptor set on the wire.
//
// It links the whole set rather than the subject alone: a rule reaches an
// imported type through the descriptor, and a subject linked without its
// imports would give a rule a different view of the same file depending on
// which side of the boundary it ran on.
func fileOf(req Request) (File, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(req.FileDescriptorSet, &set); err != nil {
		return File{}, fmt.Errorf("the %d bytes sent for %s are not a FileDescriptorSet: %w",
			len(req.FileDescriptorSet), req.Path, err)
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return File{}, fmt.Errorf("linking the descriptors for %s: %w", req.Path, err)
	}
	fd, err := files.FindFileByPath(req.Path)
	if err != nil {
		return File{}, fmt.Errorf("the set sent does not hold %s: %w", req.Path, err)
	}
	return File{Desc: fd}, nil
}
