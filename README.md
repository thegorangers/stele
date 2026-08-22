# stele

`stele` is a protobuf contract tool that fetches dependencies straight from git.
There is no registry in the middle: a dependency is a git repository, a ref, and
a module root, and that is the whole distribution model.

**Status: early development.** The commands described below are being built out;
this repository currently contains the project scaffolding.

## What it is meant to do

- `stele export` — materialise a dependency's `.proto` files into a directory.
- `stele generate` — compile `.proto` files and drive existing code generator
  plugins over the standard `CodeGeneratorRequest` protocol.
- `stele migrate` — translate an existing `buf.yaml` / `buf.gen.yaml` pair into
  a `stele.yaml`.

Parsing is done with [`bufbuild/protocompile`](https://github.com/bufbuild/protocompile);
descriptors use [`google.golang.org/protobuf`](https://google.golang.org/protobuf).
`stele` writes no code generator plugins of its own — it runs the public ones you
already use, named in your configuration.

## Honest boundaries

Read these before adopting the tool. They are design decisions, not gaps that
will quietly close later.

- **Only a subset of buf's configuration format is supported.** The subset was
  chosen from configurations that were actually measured, not from the buf
  documentation. If your configuration uses something outside it, `stele` will
  tell you so instead of guessing.
- **An unknown configuration key or command-line flag is an error**, naming the
  key. There is no silent skipping. This is a load-bearing property: a config
  that is quietly half-understood produces output that is quietly wrong.
- **An empty result is an error.** Neither `export` nor `generate` exits
  successfully having produced zero files; an empty `paths` match reports which
  path matched nothing.
- **Dependencies come from git only**, over `https://` or `ssh://`, plus the
  configurable `gh:` and `glab:` shorthands. Hosts and shorthands are
  configuration, not built-in constants. The unauthenticated `git://` protocol
  is rejected.
- **There is no registry, and none is planned.** `stele` never contacts a schema
  registry, for schemas or for plugins.
- **Full compatibility with buf is not promised.** We cannot verify a claim that
  broad, so we do not make it. What we do verify is byte-for-byte parity with
  buf's output on the corpus of repositories we test against.
- **The configuration format is a public contract.** `version: 1` means a
  breaking change requires a new version, with the previous one still supported.

Authentication is delegated entirely to your system `git`: SSH agents,
credential helpers and `insteadOf` rewrites work because it is real `git` doing
the fetching.

## Requirements

- Go 1.26 or newer to build.
- A `git` binary on `PATH` at runtime.

## Building

```bash
go build ./...
go test ./...
```

Tests run offline on a clean clone and require no environment variables.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
