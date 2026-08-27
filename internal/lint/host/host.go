// Package host runs lint rules that live outside this repository.
//
// It is a transport for the rule interface and not a second interface: what it
// returns are rule.Rule values, indistinguishable to the engine from the rules
// that ship here. That was the claim slice 1 made when it designed the
// interface around what survives a process boundary, and a host that had to
// introduce a second interface would be the evidence that the claim was wrong.
//
// The care in this file is in the failure paths. A rule in another process can
// fail in ways an in-tree rule cannot — it can be missing, die halfway, hang,
// answer with rubbish, or claim to be a rule it is not — and each has a
// different fix. Every one of them is reported naming the rule, the plugin and
// the file, and none of them is ever a silent skip: a rule that could not run
// is not a rule that found nothing, and a build that went green because a rule
// crashed is the failure this tool exists to remove.
package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thegorangers/stele/rule"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Timeouts. They are constants rather than configuration on purpose: a
// timeout somebody can raise is a timeout somebody raises until the hang stops
// being reported, and a rule is a pure function over one file — the honest
// budget is generous already.
const (
	// helloTimeout is how long a rule has to announce itself. It covers
	// process start on a cold page cache and nothing else.
	helloTimeout = 20 * time.Second
	// stopTimeout is how long a rule is given to exit when the run is over
	// before it is killed.
	stopTimeout = 5 * time.Second
)

// checkTimeout is how long a rule has to answer about one file. It is a
// variable only so that a test can watch it expire without waiting a minute;
// nothing outside this package's own tests changes it.
var checkTimeout = 60 * time.Second

// stderrLimit caps how much of a rule's diagnostics is quoted back, as it does
// for a code generation plugin: the tail is what says why, and the rest would
// bury the error carrying it.
const stderrLimit = 4 << 10

// Plugin is one rule plugin resolved to something runnable.
//
// Path is a binary the caller has already pinned and verified — this package
// does not decide which bytes run, because that decision is the same one code
// generation plugins make and it is made in one place, internal/plugin, for
// both.
type Plugin struct {
	// Name is the manifest's own spelling of the plugin. It is what errors
	// call it, and it is not the rule ID: one plugin may serve several rules,
	// and the name is what a reader has to go and edit.
	Name string
	// Path is the executable to run.
	Path string
}

// Set is the rules loaded from a set of plugins, and the processes serving
// them.
type Set struct {
	procs []*process
	rules []rule.Rule
	// owner maps a rule ID to the plugin serving it. A rule ID is what
	// configuration names, and the plugin is what a reader has to go and
	// edit; a listing that showed one without the other would name a rule
	// nobody can find the declaration of.
	owner map[string]string
}

// Rules returns the loaded rules, sorted by ID.
func (s *Set) Rules() []rule.Rule {
	if s == nil {
		return nil
	}
	return s.rules
}

// PluginFor names the plugin serving the rule with this ID, or the empty
// string when this set serves no such rule.
func (s *Set) PluginFor(id string) string {
	if s == nil {
		return ""
	}
	return s.owner[id]
}

// Close stops every rule process. It is safe to call twice.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, p := range s.procs {
		if err := p.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Load starts every plugin and returns the rules they serve.
//
// Everything that can be known before a file is checked is checked here: that
// the binary starts, that it speaks a protocol version this tool knows, that
// every ID it claims is well formed, that none of them claims the reserved
// namespace, and that no two plugins claim one ID. A run whose rules are
// broken must fail before it compiles anything, because that is the moment the
// error can name the manifest line that caused it.
//
// A failure here stops every process already started. A tool that left them
// running would leave a machine with a process per broken run.
func Load(ctx context.Context, plugins []Plugin) (*Set, error) {
	set := &Set{owner: make(map[string]string)} // rule ID -> plugin name
	owner := set.owner
	for _, pl := range plugins {
		p, err := start(ctx, pl)
		if err != nil {
			set.Close()
			return nil, err
		}
		set.procs = append(set.procs, p)
		for _, d := range p.hello.Rules {
			if err := check(pl, d, owner); err != nil {
				set.Close()
				return nil, err
			}
			owner[d.ID] = pl.Name
			set.rules = append(set.rules, &hosted{proc: p, desc: d})
		}
		if len(p.hello.Rules) == 0 {
			set.Close()
			return nil, fmt.Errorf("lint plugin %q: it started and announced no rules; "+
				"a plugin that serves nothing is a line in %s that protects nothing — "+
				"remove it, or fix the plugin to serve the rules it was meant to",
				pl.Name, "stele.yaml")
		}
	}
	sort.Slice(set.rules, func(i, j int) bool { return set.rules[i].ID() < set.rules[j].ID() })
	return set, nil
}

// check validates one announced rule.
func check(pl Plugin, d rule.Descriptor, owner map[string]string) error {
	if err := rule.CheckID(d.ID); err != nil {
		return fmt.Errorf("lint plugin %q: %w", pl.Name, err)
	}
	if ns := rule.Namespace(d.ID); rule.Reserved(ns) {
		// The namespace is reserved, and refusing it is not tidiness. A rule
		// ID is a public contract that appears in somebody's manifest and
		// ignore list; a third-party rule that could claim `stele/...` or
		// `aip/...` could take over the configuration of a rule that ships
		// here, or be silently replaced by one of the same name in a later
		// release.
		return fmt.Errorf("lint plugin %q claims the rule ID %q, and the %q namespace is reserved "+
			"for the rules that ship with this tool; a rule from outside declares its own namespace, "+
			"such as %s/%s — ask its author to rename it",
			pl.Name, d.ID, ns, pl.Name, strings.TrimPrefix(d.ID, ns+"/"))
	}
	if first, dup := owner[d.ID]; dup {
		// Whichever ran, the other's configuration would silently describe
		// nothing: one `severity: warning` line would be read as exempting a
		// rule that is still failing the build. There is no resolution order
		// worth inventing here, because any of them makes the meaning of a
		// manifest depend on the order plugins happen to be declared in.
		return fmt.Errorf("lint plugins %q and %q both serve the rule ID %q; an ID names exactly one rule, "+
			"and configuration written for it would apply to whichever ran — "+
			"drop one of the two plugins, or ask its author to rename the rule",
			first, pl.Name, d.ID)
	}
	return nil
}

// process is one running rule plugin.
type process struct {
	name  string
	path  string
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	errs  *tail
	hello rule.Hello

	// mu serialises requests. One process answers one question at a time:
	// the protocol has no request IDs, and inventing them would be inventing
	// concurrency the rule interface does not have.
	mu sync.Mutex
	// dead is set once the process has failed. Every later call returns it
	// rather than blocking on a pipe nobody is reading, so a plugin that
	// died on the first file reports the same cause on all the rest instead
	// of a timeout per file.
	dead error
}

func start(ctx context.Context, pl Plugin) (*process, error) {
	cmd := exec.Command(pl.Path)
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("%s=%d", rule.ProtocolEnv, rule.Protocol))
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lint plugin %q: %w", pl.Name, err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lint plugin %q: %w", pl.Name, err)
	}
	errs := &tail{limit: stderrLimit}
	cmd.Stderr = errs
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lint plugin %q: %s could not be run: %w; "+
			"check that the path in stele.yaml names an executable this machine can run",
			pl.Name, pl.Path, err)
	}
	p := &process{name: pl.Name, path: pl.Path, cmd: cmd, in: in, out: bufio.NewReader(out), errs: errs}

	var hello rule.Hello
	if err := p.await(ctx, helloTimeout, func() error { return rule.ReadFrame(p.out, &hello) }); err != nil {
		p.kill()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("lint plugin %q: it did not announce its rules within %s and was stopped%s; "+
				"a rule plugin must write its first frame at startup — "+
				"check that its main function calls rule.Serve",
				pl.Name, helloTimeout, p.errs.quote())
		}
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("lint plugin %q: it exited without announcing any rules%s; "+
				"check that its main function calls rule.Serve, and that %s runs it",
				pl.Name, p.errs.quote(), pl.Path)
		}
		return nil, fmt.Errorf("lint plugin %q: its first frame is not a rule announcement: %w%s; "+
			"a rule plugin must write nothing but protocol frames to stdout",
			pl.Name, err, p.errs.quote())
	}
	if hello.Protocol != rule.Protocol {
		p.kill()
		return nil, fmt.Errorf("lint plugin %q speaks rule protocol %d and this tool speaks %d; "+
			"upgrade whichever of the two is older",
			pl.Name, hello.Protocol, rule.Protocol)
	}
	p.hello = hello
	return p, nil
}

// await runs fn with a deadline, and reports context.DeadlineExceeded when it
// does not finish in time.
//
// The goroutine it leaves behind on a timeout is blocked on a pipe belonging
// to a process that is about to be killed, so it ends when the pipe does.
func (p *process) await(ctx context.Context, d time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return context.DeadlineExceeded
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ask sends one request and returns the answer, or the reason there is none.
func (p *process) ask(id string, req rule.Request) (*rule.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead != nil {
		return nil, p.dead
	}
	if err := rule.WriteFrame(p.in, req); err != nil {
		return nil, p.die(fmt.Errorf("lint rule %s (plugin %q): the request about %s could not be sent: %w; "+
			"the rule process is gone, and nothing in that file was checked by this rule%s",
			id, p.name, req.Path, err, p.errs.quote()))
	}
	var resp rule.Response
	err := p.await(context.Background(), checkTimeout, func() error { return rule.ReadFrame(p.out, &resp) })
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		p.kill()
		return nil, p.die(fmt.Errorf("lint rule %s (plugin %q): it did not answer about %s within %s "+
			"and was stopped%s; a rule is a pure check over one file and should not take that long — "+
			"report the file to the rule's author, or drop the plugin from stele.yaml while it is fixed",
			id, p.name, req.Path, checkTimeout, p.errs.quote()))
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return nil, p.die(fmt.Errorf("lint rule %s (plugin %q): the rule process died while checking %s%s; "+
			"nothing in that file was checked by this rule — report it to the rule's author, "+
			"or drop the plugin from stele.yaml while it is fixed%s",
			id, p.name, req.Path, p.exitNote(), p.errs.quote()))
	default:
		return nil, p.die(fmt.Errorf("lint rule %s (plugin %q): its answer about %s is not a rule response: %w%s; "+
			"a rule plugin must write nothing but protocol frames to stdout — "+
			"a stray print, or a log line on stdout instead of stderr, is the usual cause",
			id, p.name, req.Path, err, p.errs.quote()))
	}
	if resp.Error != "" {
		// The rule reached the boundary and said it could not answer. That is
		// still not a finding and still not silence.
		return nil, fmt.Errorf("lint rule %s (plugin %q): it could not check %s: %s%s",
			id, p.name, req.Path, resp.Error, p.errs.quote())
	}
	return &resp, nil
}

// die records why the process is finished, so every later question about every
// later file reports the cause rather than a fresh timeout.
func (p *process) die(err error) error {
	if p.dead == nil {
		p.dead = err
	}
	return err
}

// exitNote is what the process's exit status says, when it has one. A signal
// is spelled out: "exit status -1" tells a reader nothing about a rule that
// was killed by the kernel for the memory it asked for.
func (p *process) exitNote() string {
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return " (it exited cleanly, in the middle of a run)"
		}
		return fmt.Sprintf(" (%s)", err)
	case <-time.After(stopTimeout):
		return ""
	}
}

func (p *process) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// close ends the process by closing its stdin, which is how the protocol says
// a run is over, and kills it if it does not take the hint.
func (p *process) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead != nil {
		return nil
	}
	p.dead = errors.New("the rule process has been stopped: the run is over")
	_ = p.in.Close()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("lint plugin %q: it exited with %w%s", p.name, err, p.errs.quote())
		}
		return nil
	case <-time.After(stopTimeout):
		p.kill()
		return fmt.Errorf("lint plugin %q: it did not exit within %s of the run finishing and was stopped",
			p.name, stopTimeout)
	}
}

// hosted is one rule served by a process. It is a rule.Rule and nothing else:
// the engine cannot tell it from a built-in, which is the point.
type hosted struct {
	proc *process
	desc rule.Descriptor
}

func (h *hosted) ID() string          { return h.desc.ID }
func (h *hosted) Description() string { return h.desc.Description }

func (h *hosted) Check(f rule.File) ([]rule.Finding, error) {
	if f.Desc == nil {
		return nil, fmt.Errorf("lint rule %s (plugin %q): no file to check", h.desc.ID, h.proc.name)
	}
	path := string(f.Desc.Path())
	set, err := setOf(f.Desc)
	if err != nil {
		return nil, fmt.Errorf("lint rule %s (plugin %q): %s could not be sent: %w", h.desc.ID, h.proc.name, path, err)
	}
	resp, err := h.proc.ask(h.desc.ID, rule.Request{Rule: h.desc.ID, Path: path, FileDescriptorSet: set})
	if err != nil {
		return nil, err
	}
	out := make([]rule.Finding, 0, len(resp.Findings))
	for i, w := range resp.Findings {
		if w.Message == "" || w.Fix == "" {
			// Both halves are required of a built-in for the same reason they
			// are required here: a reader in a CI log has to know what is
			// wrong and what to do, and a rule that supplies only the first
			// is the kind whose output gets skipped.
			return nil, fmt.Errorf("lint rule %s (plugin %q): its finding %d about %s has no %s; "+
				"a finding states what is wrong and what to do about it — report it to the rule's author",
				h.desc.ID, h.proc.name, i+1, path, missingHalf(w))
		}
		out = append(out, rule.Finding{
			Pos:     rule.Position{Line: w.Line, Col: w.Col},
			Message: w.Message,
			Fix:     w.Fix,
		})
	}
	return out, nil
}

func missingHalf(w rule.WireFinding) string {
	if w.Message == "" {
		return "message"
	}
	return "fix"
}

// setOf serialises a file and its transitive imports, with source information,
// as a FileDescriptorSet in dependency order.
//
// The order matters: protodesc links a set in the order it is given, and a
// file that arrives before the import it needs cannot be linked. It is the
// host's job to get that right once rather than every rule author's to
// discover it.
func setOf(fd protoreflect.FileDescriptor) ([]byte, error) {
	set := &descriptorpb.FileDescriptorSet{}
	seen := make(map[string]bool)
	var add func(protoreflect.FileDescriptor)
	add = func(f protoreflect.FileDescriptor) {
		if f == nil || seen[string(f.Path())] {
			return
		}
		seen[string(f.Path())] = true
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			add(imports.Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(f))
	}
	add(fd)
	return proto.Marshal(set)
}

// tail keeps the last of a stream, so that a rule that prints a great deal
// before it dies still reports the part that says why.
type tail struct {
	mu    sync.Mutex
	limit int
	buf   bytes.Buffer
	over  bool
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(b)
	if t.buf.Len() > t.limit {
		trimmed := t.buf.Bytes()[t.buf.Len()-t.limit:]
		kept := append([]byte(nil), trimmed...)
		t.buf.Reset()
		t.buf.Write(kept)
		t.over = true
	}
	return len(b), nil
}

// quote renders the diagnostics for an error message, or nothing when the rule
// said nothing.
func (t *tail) quote() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := bytes.TrimSpace(t.buf.Bytes())
	if len(b) == 0 {
		return ""
	}
	prefix := "\n"
	if t.over {
		prefix = "\n…"
	}
	return prefix + string(b)
}
