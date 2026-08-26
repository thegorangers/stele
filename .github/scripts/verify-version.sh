#!/usr/bin/env bash
# Refuses to let a release publish a binary that cannot vouch for its own
# version.
#
# This is what stands in for -ldflags. The Go toolchain stamps the tag into the
# binary by itself; this asserts that what it stamped is exactly the tag being
# released. A checkout that lost its tags, a build that lost its VCS data, or a
# dirty tree all produce a version that is not the tag, and all of them stop
# here instead of shipping a binary that says (devel) or v0.1.0+dirty.
#
# It runs as a goreleaser post-build hook, once per target, so it gates the
# release from inside the build rather than reporting after the upload.
#
# Only the runner's own platform can be executed, so the other three targets
# are skipped rather than pretended about. That is the same coverage the
# hand-written workflow had. It is real coverage even so: all four binaries
# come out of one checkout and one toolchain invocation each, and the version
# is a property of the checkout, not of GOARCH.
set -euo pipefail

binary="${1:?usage: verify-version.sh <binary> <target>}"
target="${2:?usage: verify-version.sh <binary> <target>}"

if [ "${IS_SNAPSHOT:-false}" = "true" ]; then
  echo "verify-version: snapshot build, no tag to check against ($target)"
  exit 0
fi

tag="${RELEASE_TAG:?RELEASE_TAG is not set}"

host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
case "$target" in
  "${host_os}_${host_arch}"|"${host_os}_${host_arch}_"*) ;;
  *)
    echo "verify-version: $target is not runnable on ${host_os}/${host_arch}; skipped"
    exit 0
    ;;
esac

reported="$("$binary" version --json |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["components"]["stele"]["version"])')"

echo "verify-version: $target reports $reported"
if [ "$reported" != "$tag" ]; then
  echo "the binary reports $reported but the tag is $tag; refusing to publish" >&2
  exit 1
fi
