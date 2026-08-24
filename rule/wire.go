package rule

// The wire protocol between this tool and a rule that lives in another
// process, and the reasoning behind each part of it.
//
// # Why there is a wire protocol at all rather than a plugin framework
//
// hashicorp/go-plugin was the design's first choice, and it was measured
// before it was adopted. In a scratch module it pulls in 40 modules —
// grpc, yamux, hclog, x/net, x/crypto, jhump/protoreflect, testify — against
// the four this repository depends on today, and it pins
// google.golang.org/protobuf to an older release than this tool uses. That
// cost buys machinery this interface does not use: a long-lived bidirectional
// connection, host callbacks into the tool, streaming, and multiplexed
// services. A Rule is pure and answers one question, and every rule author
// outside this repository would inherit the whole tree in order to say so.
//
// What go-plugin genuinely provides — a separate process, so that a rule which
// crashes takes nothing with it, and a version handshake so that a mismatched
// binary is refused rather than misread — is provided here by a subprocess, a
// protocol version in the first frame, and a pin on which binary runs that is
// stronger than a handshake: the same digest and module verification the code
// generation plugins already use. Nothing else about go-plugin was needed, so
// nothing else was taken.
//
// # The framing
//
// Length-prefixed frames on the rule's stdin and stdout: four bytes of
// big-endian length, then that many bytes of JSON. stderr is the rule's own
// diagnostics and is never parsed — it is quoted back when something goes
// wrong, which is the only reason a rule should write to it.
//
// The frames are JSON, and the descriptors inside them are base64 of a
// serialised FileDescriptorSet. One encoding on the wire is worth the third
// the base64 costs on a local pipe: a rule author needs a JSON parser, which
// every language has, plus the protobuf runtime they were always going to need
// to read a descriptor. Two framings would be two things to version and two
// ways for a malformed answer to be misread.
//
// A frame is capped. A rule that has gone wrong can produce an unbounded
// answer, and a host that read it into memory would fail as an out-of-memory
// kill with no idea which rule caused it.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Protocol is the version of this wire protocol. It is in the first frame a
// rule sends, and a rule speaking a version this tool does not know is refused
// by name rather than misread.
const Protocol = 1

// ProtocolEnv is set on a rule process by the host. A rule binary run by hand
// sees no value and says so, instead of hanging on a terminal waiting for a
// frame that will never come — which is what a protocol on stdin does by
// default, and it is the first thing anybody writing a rule does.
const ProtocolEnv = "STELE_RULE_PROTOCOL"

// MaxFrame is the largest frame either side will read or write. A descriptor
// set for a very large file with source information is a few megabytes; this
// is generous against that and still bounded.
const MaxFrame = 64 << 20

// Hello is the first frame a rule sends, unprompted, when it starts.
//
// It is sent at startup rather than in answer to a request because it is how
// the host learns what the process is: a rule that cannot start must fail when
// the rules are loaded, where the error can name the plugin and stop the run,
// and not in the middle of checking somebody's file.
type Hello struct {
	// Protocol is the wire version the rule speaks.
	Protocol int `json:"protocol"`
	// Rules are the rules this binary serves. A binary may serve several: a
	// pack of related rules is one thing to pin and one process to start.
	Rules []Descriptor `json:"rules"`
}

// Descriptor is one rule as it announces itself.
type Descriptor struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Request is one file, for one rule.
type Request struct {
	// Rule is the ID of the rule being asked. It is explicit because one
	// process may serve several.
	Rule string `json:"rule"`
	// Path is the import path of the file to check. It names which member of
	// Files is the subject; the rest are there so that it links.
	Path string `json:"path"`
	// FileDescriptorSet is a serialised descriptorpb.FileDescriptorSet,
	// base64 encoded, holding the subject and its transitive imports with
	// source information retained. Source information is what a finding's
	// line number comes from, and a set sent without it would produce
	// findings a reader has to go hunting for.
	FileDescriptorSet []byte `json:"file_descriptor_set"`
}

// Response is one rule's answer about one file.
//
// Error is a field rather than a separate frame type because a rule that
// cannot answer must be able to say so in the same place the answer would
// have been. A rule that returned an empty finding list instead would be
// reporting a failure as a clean file.
type Response struct {
	Findings []WireFinding `json:"findings,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// WireFinding is a Finding as it crosses the boundary: what the rule decides,
// and nothing the engine stamps.
//
// Rule, Path and Severity are deliberately absent. A rule that could name its
// own ID could name somebody else's, a rule that named a path could report a
// finding about a file it was not given, and severity is the reader's
// judgement about their own repository.
type WireFinding struct {
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// WriteFrame writes v as one length-prefixed JSON frame.
func WriteFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return fmt.Errorf("frame of %d bytes exceeds the %d byte limit", len(b), MaxFrame)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadFrame reads one length-prefixed JSON frame into v.
//
// A short read is reported as io.ErrUnexpectedEOF and a clean end of stream as
// io.EOF, because the callers tell them apart: the first is a process that
// died halfway through an answer, the second one that exited between answers,
// and they are different messages to the person reading them.
func ReadFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrame {
		return fmt.Errorf("announced a frame of %d bytes, over the %d byte limit", n, MaxFrame)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
