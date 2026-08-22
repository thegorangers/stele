# Contributing to stele

Thanks for your interest. A few things are worth knowing before you open a pull
request.

## Ground rules

- **This is a general-purpose tool.** Anything that is true only for one
  organisation's repositories belongs in that organisation's configuration, not
  in this code. Repository names, host names, package prefixes and directory
  layouts are configuration.
- **Everything public is written in English**: code comments, error messages,
  documentation and commit messages.
- **New behaviour arrives with a test that was red first.** A test that has
  never failed demonstrates only that it compiles. If you fix a bug, show the
  failing test before the fix.
- **`go test ./...` on a clean clone must pass** for anyone, with no environment
  variables set and no network access. Tests that need a local corpus of
  repositories live behind the `parity` build tag and skip when their corpus
  environment variable is unset.

## Before you start

For anything larger than a small fix, please open an issue first. It is cheaper
to disagree about the design in an issue than in a finished branch.

## Making a change

```bash
go build ./...
go test ./...
go vet ./...
gofmt -l .
```

All four must be clean. `gofmt -l .` should print nothing.

## Commit messages

Use the conventional-commit style already present in the history, for example
`feat(export): ...`, `fix(resolve): ...`, `chore: ...`. Explain why the change
is needed, not only what it does.

## Licensing

By contributing you agree that your contribution is licensed under the Apache
License, Version 2.0, the same licence as the project.
