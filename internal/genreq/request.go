// Package genreq builds the CodeGeneratorRequest that is handed to a code
// generation plugin.
//
// Everything this package does is a decision about output bytes, not about
// convenience. A plugin reads only what the request contains, in the order the
// request contains it, so the ordering of proto_file, the presence of
// SourceCodeInfo and the shape of the options are all part of the generated
// code. The design notes (§6.3) list what determines those bytes; this package
// is where the items protocompile does not do for us are done.
package genreq

import (
	"errors"
	"fmt"

	"github.com/bufbuild/protocompile/linker"
	"github.com/thegorangers/stele/internal/managed"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protopath"
	"google.golang.org/protobuf/reflect/protorange"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// Compiler version reported to the plugin.
//
// OPEN QUESTION (design §6.3): what a plugin does with this value, and what
// another tool puts here, is not measured. We report our own version because
// it is the only number we can state truthfully; whether byte parity with
// another generator requires reporting that generator's number instead is an
// open parity question, to be settled by comparing artefacts, not by guessing
// here. Plugins in use read the request's parameter, not this field, so a
// wrong guess would be worse than an honest one.
const (
	versionMajor = 0
	versionMinor = 1
	versionPatch = 0
)

// Target is what the caller supplies beyond the compiled files: everything
// that comes from configuration rather than from the sources.
type Target struct {
	// Parameter is the plugin's opt string, passed through unchanged.
	Parameter string

	// Managed, when non-nil, asks for managed-mode file options to be
	// synthesised into every descriptor in the request before it is sent.
	// The options travel inside generated code, so applying them is part of
	// producing comparable output, not a cosmetic step.
	Managed *managed.Config
}

// Build assembles the request for one input: the files to generate, plus the
// transitive closure of their imports.
//
// files are the compiled targets, exactly what compile.Compile returns —
// imports are reached through each file's own descriptor rather than being
// listed by the caller, because the caller does not know the closure and
// guessing it is how an import goes missing.
func Build(files linker.Files, t Target) (*pluginpb.CodeGeneratorRequest, error) {
	// An empty request is never a success: a plugin asked to generate nothing
	// exits happily, and the run looks like it worked.
	if len(files) == 0 {
		return nil, errors.New("genreq: no files to generate")
	}

	targets := make([]string, 0, len(files))
	isTarget := make(map[string]bool, len(files))
	for _, f := range files {
		name := f.Path()
		if isTarget[name] {
			continue
		}
		isTarget[name] = true
		targets = append(targets, name)
	}

	ordered, err := topological(files)
	if err != nil {
		return nil, err
	}

	protoFiles := make([]*descriptorpb.FileDescriptorProto, 0, len(ordered))
	for _, d := range ordered {
		// ToFileDescriptorProto builds a fresh message, so the request owns
		// its descriptors and the mutations below cannot leak back into the
		// compiled files — managed.Apply rewrites in place.
		fd := protodesc.ToFileDescriptorProto(d)

		// SourceCodeInfo is kept for the files being generated, because every
		// comment in the generated code comes from it, and dropped for the
		// imports, whose comments nothing reads while their locations would
		// bloat each embedded descriptor. protocompile has no per-file switch
		// for this, so the choice is made here.
		if !isTarget[fd.GetName()] {
			fd.SourceCodeInfo = nil
		}

		if t.Managed != nil && !isWellKnown(fd.GetName()) {
			managed.Apply(fd, *t.Managed)
		}

		stripSourceRetentionOptions(fd)
		protoFiles = append(protoFiles, fd)
	}

	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: targets,
		ProtoFile:      protoFiles,
		CompilerVersion: &pluginpb.Version{
			Major: proto.Int32(versionMajor),
			Minor: proto.Int32(versionMinor),
			Patch: proto.Int32(versionPatch),
		},
	}
	if t.Parameter != "" {
		req.Parameter = proto.String(t.Parameter)
	}
	return req, nil
}

// topological returns every file in the closure of files, with each import
// placed before the file that imports it, and each file once.
//
// protocompile returns exactly the files it was asked for, in the order they
// were named; the closure and its order are built here. The walk is a
// depth-first post-order over the targets in the order given — compile.Compile
// sorts them — visiting each file's imports in the order they are declared, so
// the result does not depend on map iteration or on the caller's argument
// order.
func topological(files linker.Files) ([]protoreflect.FileDescriptor, error) {
	var out []protoreflect.FileDescriptor
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}

	var visit func(d protoreflect.FileDescriptor) error
	visit = func(d protoreflect.FileDescriptor) error {
		switch state[d.Path()] {
		case done:
			return nil
		case visiting:
			// proto forbids import cycles, so reaching one means the input is
			// not what it claims to be; saying so beats looping forever.
			return fmt.Errorf("genreq: import cycle through %s", d.Path())
		}
		state[d.Path()] = visiting
		imports := d.Imports()
		for i := 0; i < imports.Len(); i++ {
			if err := visit(imports.Get(i).FileDescriptor); err != nil {
				return err
			}
		}
		state[d.Path()] = done
		out = append(out, d)
		return nil
	}

	for _, f := range files {
		if err := visit(f); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// stripSourceRetentionOptions clears every option declared
// retention = RETENTION_SOURCE anywhere in the descriptor.
//
// google.golang.org/protobuf v1.36.12 exports no helper for this: the only
// implementation in the module is unexported, inside protoc-gen-go's own
// package (cmd/protoc-gen-go/internal_gengo). What it does is reproduced here
// with the public protorange/protopath packages rather than by hand-walking
// the descriptor tree, so that a new option field in a future descriptor.proto
// is covered without this code being touched.
func stripSourceRetentionOptions(fd *descriptorpb.FileDescriptorProto) {
	// Range returns an error only when the walk function returns one, and
	// this one never does.
	_ = protorange.Range(fd.ProtoReflect(), func(vs protopath.Values) error {
		m, ok := vs.Index(-1).Value.Interface().(protoreflect.Message)
		if !ok {
			return nil
		}
		var clear []protoreflect.FieldDescriptor
		m.Range(func(f protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
			if o, ok := f.Options().(*descriptorpb.FieldOptions); ok &&
				o.GetRetention() == descriptorpb.FieldOptions_RETENTION_SOURCE {
				clear = append(clear, f)
			}
			return true
		})
		for _, f := range clear {
			m.Clear(f)
		}
		return nil
	})
}

// isWellKnown reports whether a file is one of the descriptors shipped with
// protobuf itself. Their options are part of the runtime that generated code
// links against, so rewriting them would point generated imports at packages
// that do not exist.
func isWellKnown(name string) bool {
	return len(name) > len("google/protobuf/") && name[:len("google/protobuf/")] == "google/protobuf/"
}
