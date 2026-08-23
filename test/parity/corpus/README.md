# The parity corpus

Byte-for-byte parity with the reference tool is this project's acceptance
criterion. This directory is what makes that claim reproducible by anyone who
checks the repository out, instead of a fact known to one machine.

`go test -tags parity ./test/parity/` runs both tools over these checkouts and
compares the two output trees byte for byte. Nothing here is fetched: no
network, no registry, no clone.

## What is in it

Two checkouts, each configured twice — `buf.yaml` / `buf.gen.yaml` inside the
checkout for the reference tool, and a `stele.yaml` in `manifests/` for this
one. The two configurations are deliberately different; that difference is the
translation under test. What is compared is output.

| Checkout | The shape it exists to measure |
| --- | --- |
| `repos/managed` | **Managed mode.** The most expensive part of parity: the generator synthesises eleven file options that appear in no source file, and they are serialised into the descriptor the generated code embeds, so getting one wrong changes bytes in every file. `order.proto` declares a `go_package` that is deliberately wrong, because replacement and defaulting differ only on a file that already has one. |
| `repos/managed` | **A target with two inputs.** Two module roots, `proto` and `internal/proto`. An input is not merged with another input, and one input over two directories is not one request either — `billing` and `order` are separate directories of one module for that reason. |
| `repos/managed` | **Two plugins over one input**, one of which (`protoc-gen-go-grpc`) generates only for the file that declares a service. |
| `repos/consumer` | **An input that reads a dependency rather than a local module**, narrowed by `paths` in the *producer's* coordinates rather than the consumer's — the rebasing that a consumer-relative reading would get wrong without failing. |
| `repos/consumer` | **A producer whose own vendored tree resolves its imports.** `third_party/telemetry` has no `stele.yaml`: it is an unmigrated producer, and its `buf.yaml` is the fallback stele reads to learn that its second module root is what satisfies its own import. |
| both | **Plugins pinned by module and version**, installed by stele into its own cache. |

The shapes are not invented. They are the ones measured across the twelve
repositories of the fleet this tool was built for, and the ones the design
notes identify as where parity is actually at risk.

`google/type/money.proto` is the genuine googleapis file, unmodified. A real,
stable, public contract is a better import than one written to be easy.

## What it does not cover

A synthetic corpus only exercises what somebody thought to include, and saying
so is part of shipping it. Compared with a run against the private fleet, this
corpus does not cover:

- **Scale.** Two checkouts and eight generated files, against twelve
  repositories and closures of thousands of files. Nothing here would catch a
  defect that needs a large graph to appear.
- **Fetching.** The dependency address is answered from a directory, not
  cloned. Neither `git`, the fetch cache, `--update`, nor the lock's
  `(git, ref)` identity is exercised here; they have unit tests, and the fleet
  run exercises them for real.
- **Export.** `export` is compared against a committed vendored tree, and this
  corpus has none. That is milestone 4, and until then the export test skips
  here and says so rather than reporting a pass.
- **Real API surfaces.** Options, extensions, deeply nested imports, `proto2`,
  and the kind of file that has grown for five years are all absent.
- **Other plugins.** `protoc-gen-go` and `protoc-gen-go-grpc` only. The fleet
  also generates for other languages, including plugins pinned by digest rather
  than by module.

The fleet run is therefore still the stronger measurement, and it stays
possible: point `STELE_PARITY_CORPUS` at an external corpus file and it is used
instead of this one.

## The reference tool is pinned

`buf_version` in `corpus.yaml` states the exact build this corpus was measured
against, and the harness refuses to run against any other — on a workstation as
well as in CI. Parity against a moving reference measures nothing: a change in
the other tool would arrive as a failure in this one, and the reader would have
no way to tell which had moved.

The plugins are pinned the same way and in one place: the manifests declare
`module` and `version`, stele installs them into its own cache, and the harness
puts that cache on the reference tool's `PATH`. Both tools therefore run the
same plugin binaries, and neither takes whatever the runner happens to have.
