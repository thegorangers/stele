<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo-dark.svg">
  <img alt="stele" src="docs/brand/logo-light.svg" width="44" align="left" hspace="14" vspace="4">
</picture>

# stele

**Protobuf contracts, distributed by git.** Generate code, export schemas, lint
contracts and catch breaking changes — with dependencies fetched straight from
git repositories instead of a schema registry.

[![CI](https://github.com/thegorangers/stele/actions/workflows/ci.yml/badge.svg)](https://github.com/thegorangers/stele/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/thegorangers/stele)](https://github.com/thegorangers/stele/releases)
[![Licence](https://img.shields.io/badge/licence-Apache--2.0-blue)](LICENSE)

**English** · [Русский](README.ru.md)

```yaml
# stele.yaml
version: 1
modules:
  - path: proto
deps:
  - name: orders
    git: gh:acme/orders
    ref: v2.0.1
generate:
  - name: go
    inputs:
      - module: proto
    plugins:
      - local: protoc-gen-go
        module: google.golang.org/protobuf/cmd/protoc-gen-go
        version: v1.36.12
        out: gen
        opt: paths=source_relative
```

```console
$ stele generate
```

## Why stele

**A dependency is a git repository, a ref and a module root.** There is no
registry in the middle, and none is planned — no account to create, no service
to run, no third party between you and a schema you already have access to.
Authentication is your system `git`: SSH agents, credential helpers and
`insteadOf` rewrites all work, because stele shells out to the tool that already
knows your credentials.

**It refuses rather than guesses.** An unknown configuration key or flag is an
error naming it. A migration that cannot translate something exits non-zero and
says what it could not decide. A run that produced zero files is a failure, not
a success. Config that is quietly half-understood produces output that is
quietly wrong, and this tool would rather stop.

**Everything that decides your bytes is pinned.** Dependencies resolve to
commits in `stele.lock`; plugins are pinned by module and version or by
per-platform digest, installed into stele's own cache and verified after
installation. `stele version` prints every input that determined a run's output.

### Compared with buf

|                  | stele                                        | buf                          |
| ---------------- | -------------------------------------------- | ---------------------------- |
| Distribution     | git repository + ref                          | schema registry (BSR) or git |
| Config           | one `stele.yaml`                              | `buf.yaml` + `buf.gen.yaml`  |
| Unknown config   | error, naming the key                         | varies                       |
| Plugins          | the public ones you already use, pinned       | local or remote              |
| Releases         | signed, reproducible, four platforms          | —                            |

Where their jobs overlap, the goal is **byte-for-byte identical output**: the
acceptance suite compares stele against a pinned buf on a committed corpus, and
you can run it yourself with `go test -tags parity ./test/parity/`.

Full compatibility is deliberately **not** promised — that claim cannot be
verified, so it is not made. Only a measured subset of buf's configuration
format is supported, and anything outside it is refused by name. See
[boundaries](docs/GUIDE.md#honest-boundaries) for the complete list, and
[ROADMAP.md](docs/ROADMAP.md) for what is not built yet.

## Install

Download a binary for `linux/amd64`, `linux/arm64`, `darwin/amd64` or
`darwin/arm64` from the [releases page](https://github.com/thegorangers/stele/releases) —
no Go toolchain required.

```bash
tag=v0.5.0 os=linux arch=amd64
base="https://github.com/thegorangers/stele/releases/download/$tag"
curl -fsSLO "$base/stele_${tag}_${os}_${arch}"
install -m 0755 "stele_${tag}_${os}_${arch}" /usr/local/bin/stele
```

With a Go toolchain:

```bash
go install github.com/thegorangers/stele/cmd/stele@v0.5.0
```

<details>
<summary><b>Verifying the release signature</b> (recommended)</summary>

`SHA256SUMS` is signed with [cosign](https://github.com/sigstore/cosign),
keyless — there is no public key to fetch. The digest alone only proves the
download arrived intact; whoever can publish a release can publish a matching
`SHA256SUMS` beside it. The signature proves the bytes came out of this
repository's release workflow, at this tag.

```bash
curl -fsSLO "$base/SHA256SUMS" "$base/SHA256SUMS.sig" "$base/SHA256SUMS.pem"

cosign verify-blob SHA256SUMS \
  --signature SHA256SUMS.sig \
  --certificate SHA256SUMS.pem \
  --certificate-identity-regexp \
    '^https://github.com/thegorangers/stele/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# Only now does the digest mean anything.
sha256sum --ignore-missing -c SHA256SUMS
```

Run the checks in that order: `sha256sum -c` against an unverified `SHA256SUMS`
compares a download to a claim from the same source. Binaries are also
reproducible from source — see [RELEASING.md](RELEASING.md).
</details>

**Requirements:** a `git` binary on `PATH` at runtime; Go 1.26+ only to build
from source.

## Quick start

**1. Write `stele.yaml`** at the repository root. The smallest one that does
anything is three lines:

```yaml
version: 1
modules:
  - path: proto
```

Add a `generate:` block to produce code, and a `deps:` block for schemas from
other repositories — as in the example at the top of this page.

**2. Generate.** Plugins install themselves on first use:

```bash
stele generate
```

`protoc-gen-go` needs each file to carry an `option go_package`, as it does
under any tool; a `managed:` block can supply the prefix instead, so the
`.proto` files do not have to name it. This writes generated code to each
plugin's `out` directory and records the
commit every dependency resolved to in `stele.lock`. Commit that file: later
runs use the pinned commits, and `--update` is what re-resolves them.

**3. Wire the checks into CI.**

```bash
stele lint                    # contract rules over what this repo owns
stele breaking --base main    # compare against the correct previous revision
```

`stele breaking` compares against the merge-base with the base branch on a topic
branch, or the first parent when already on it — you never name a revision by
hand. It needs full history: run `git fetch --unshallow` first if your CI clones
shallow.

Adopting `lint` on a repository that is not clean yet? `stele lint
--update-baseline` writes `stele.baseline`, which accepts today's findings so
that only new ones fail the build. Commit and review it like any other file.

## Migrating from buf

```bash
stele migrate            # prints the translated manifest
stele migrate --write    # writes stele.yaml
```

It reads `buf.gen.yaml`, `buf.yaml`, and the `Makefile` — the only place a
vendored tree usually records where its plugins came from — plus your own
`.proto` files, to work out which third-party modules you actually import.

**A migration that leaves anything undecided fails**, naming every item: an
import with no declared source, an unpinned export, a plugin whose version it
could not resolve to an exact one. Nothing is invented to fill a hole. A
manifest that looks migrated and is not would be worse than no manifest.

buf's own `lint` and `breaking` blocks are read but not carried over — the rule
sets are not the same, and translating them would be a guess. Configure those
fresh in `stele.yaml`.

## Commands

| Command           | What it does                                                                     |
| ----------------- | -------------------------------------------------------------------------------- |
| `stele generate`  | Compile `.proto` files and run your code generator plugins                        |
| `stele export`    | Materialise a dependency's `.proto` files into a directory                        |
| `stele lint`      | Check this repository's contracts against rules                                   |
| `stele breaking`  | Compare against the previous revision and report wire and source breakages        |
| `stele migrate`   | Translate a `buf.yaml` / `buf.gen.yaml` pair into a `stele.yaml`                  |
| `stele plugins`   | Install the plugins the manifest declares, or list which binary each resolves to  |
| `stele version`   | Print the versions that determine generated bytes                                 |

Every command takes `--help`; the ones that read a manifest also take `--dir DIR`
and `--cache-dir DIR` (default `$XDG_CACHE_HOME/stele`, else `~/.cache/stele`;
`$STELE_CACHE_DIR` is honoured). Full flag reference: [the guide](docs/GUIDE.md).

## Editor support

JSON Schemas for `stele.yaml`, `stele.lock` and `stele.baseline` ship in
[`schema/`](schema/). One line at the top of your manifest gives you completion
and validation:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/thegorangers/stele/main/schema/stele.schema.json
```

## Known limits

Worth knowing before you rely on it:

- **`stele breaking` has a blind zone**, printed in every report: `json_name`
  renames, `int32` widening to `int64` under protojson, and `google.api.http`
  changes pass green. If your fleet does gRPC transcoding, read
  [the blind zone](docs/GUIDE.md#the-blind-zone).
- **Lint is a first slice** — five `aip/` rules across three AIPs, plus general
  contract rules. The inventory of what a descriptor can and cannot decide is
  [docs/AIP.md](docs/AIP.md).
- **`stele lint` judges only what this repository owns.** Dependencies are
  compiled, not linted.
- **No Windows support**, deliberately.

## Documentation

- [The guide](docs/GUIDE.md) — complete reference for every command, field and rule
- [ROADMAP.md](docs/ROADMAP.md) — where it stands, and what is not built yet
- [RELEASING.md](RELEASING.md) — versioning policy and how releases are made
- [CHANGELOG.md](CHANGELOG.md) — what changed
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute
- [SECURITY.md](SECURITY.md) — reporting a vulnerability

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
