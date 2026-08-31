# Security

## Reporting a vulnerability

Please do not open a public issue for a suspected vulnerability. Instead, use
GitHub's private reporting: **Security → Advisories → Report a vulnerability**
on this repository. That opens a private channel between you and the
maintainer without exposing the report, or a working exploit, to everyone
watching the repository before there is a fix.

If GitHub's private reporting is unavailable to you for some reason, open an
issue asking for another contact and say nothing about the vulnerability
itself in it — a maintainer will follow up privately.

Include what you have: the manifest or `.proto` input that triggers it, the
command and flags, the version (`stele version`), and what you expected
against what happened. A reproduction is worth more than a description.

## What to expect

This is a single-maintainer project. That is worth being honest about rather
than promising a response time nobody is staffed to meet:

- Reports are read as soon as the maintainer sees them, which in practice is
  within a few days, not hours. There is no on-call rotation behind this
  repository.
- A confirmed vulnerability gets a fix and a release; how fast depends on its
  severity and how much of the fleet it reaches. `stele` is a build-time
  tool — its bytes end up in other repositories' CI, not in a running
  service — so a fix that lands in a release before the next `plugins`
  install or CI image rebuild is normally enough to be worth calling a fix,
  rather than an SLA measured in hours.
- Credit is given in the release notes and the advisory, unless you ask not
  to be named.

## Supported versions

Only the latest `0.x` release is supported, in line with
[RELEASING.md](RELEASING.md): the major version is `0` while the tool's
output is still settling, and there is no maintenance branch carrying fixes
backward into an older minor. Upgrading to the latest release is the
supported path to a fix.

## Scope

In scope: `stele` itself — the CLI, the code it generates the invocation of
(not the third-party plugins it shells out to), the release artefacts, and
the signing and verification path described in
[RELEASING.md](RELEASING.md#signing). A compromise of a plugin you configured
`stele` to run is that plugin's advisory, not this project's — but a report
that `stele` makes such a compromise easier than it should (an unpinned
`ref`, an unverified download) is very much in scope.

Out of scope: vulnerabilities in a dependency that are not reachable from any
code path `stele` executes. `govulncheck` runs in CI for exactly this reason
— see [CONTRIBUTING.md](CONTRIBUTING.md) — and a dependency update for an
unreachable finding is a normal pull request, not a security report.
