package plugin

import (
	"fmt"
	"strings"
)

// A downloaded plugin is pinned to a digest, and a digest names bytes. Bytes
// are per-platform: the linux/amd64 binary and the darwin/arm64 one are
// different files with different digests, and no single address serves both.
//
// So the download tier is a list, one entry per platform, and the entry is
// chosen by comparing the manifest's own spelling against runtime.GOOS and
// runtime.GOARCH. The spellings are Go's for one reason: they are what the
// comparison is made against. Accepting the spellings publishers use — x64,
// x86_64, macos, aarch64 — would need a translation table, and a table nobody
// can audit is a place where a platform gets matched to the wrong bytes and
// the digest verifies them happily.
//
// The two failure modes are refusals, never silent. Two entries for one
// platform would make the tool pick one and leave the manifest describing a
// binary that never ran. No entry for this platform must not fall back to a
// PATH lookup: that is precisely how the un-pinned binary gets back in, on the
// machine of whoever the manifest's author did not think about.

// Download is one published binary of a plugin, for one platform.
type Download struct {
	// OS is the GOOS the binary is for, as runtime.GOOS spells it.
	OS string
	// Arch is the GOARCH the binary is for, as runtime.GOARCH spells it.
	Arch string
	// URL is where the binary, or an archive holding it, is fetched from.
	URL string
	// SHA256 is the hex digest the download must have. It is the pin.
	SHA256 string
	// ArchivePath is the member to take when the download is an archive.
	ArchivePath string
}

// Platform is the entry's platform as an error message writes it.
func (d Download) Platform() string { return d.OS + "/" + d.Arch }

// Select returns the one entry of ds that is for goos/goarch.
//
// name is the plugin's name in the manifest, and is only ever used to say
// which plugin an error is about.
func Select(name string, ds []Download, goos, goarch string) (Download, error) {
	want := goos + "/" + goarch
	var matched []Download
	for _, d := range ds {
		if d.OS == goos && d.Arch == goarch {
			matched = append(matched, d)
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return Download{}, fmt.Errorf("plugin %q: the manifest declares no download for %s; "+
			"it covers %s. Add an entry for %s with the url and sha256 of the binary published for it. "+
			"Nothing is looked up on PATH instead: a plugin that is pinned on one machine and found on another "+
			"is not pinned at all",
			name, want, covered(ds), want)
	default:
		return Download{}, fmt.Errorf("plugin %q: %d downloads are declared for %s, and the tool cannot honour two: %s. "+
			"Exactly one entry may match a platform; whichever was honoured, the manifest would describe a binary that never ran",
			name, len(matched), want, strings.Join(urls(matched), " and "))
	}
}

// covered lists the platforms a manifest does cover, so that an author on an
// uncovered one can see what to copy rather than what to guess.
func covered(ds []Download) string {
	if len(ds) == 0 {
		return "no platforms at all"
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Platform())
	}
	return strings.Join(out, ", ")
}

func urls(ds []Download) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.URL)
	}
	return out
}
