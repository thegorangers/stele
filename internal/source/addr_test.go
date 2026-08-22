package source_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/source"
)

// testHosts deliberately configures the two shorthands with different
// transports: a shorthand must expand to the transport configured for its
// host, never to a hardcoded one.
func testHosts() map[string]source.HostConfig {
	return map[string]source.HostConfig{
		"gh":   {Host: "github.com", Transport: source.HTTPS},
		"glab": {Host: "gitlab.com", Transport: source.SSH},
	}
}

func TestParseAddr(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        string
		want      string
		wantHost  string
		wantPath  string
		transport source.Transport
	}{
		{
			name:      "https url",
			in:        "https://github.com/acme/example.git",
			want:      "https://github.com/acme/example.git",
			wantHost:  "github.com",
			wantPath:  "acme/example",
			transport: source.HTTPS,
		},
		{
			name:      "https url without suffix",
			in:        "https://github.com/acme/example",
			want:      "https://github.com/acme/example.git",
			wantHost:  "github.com",
			wantPath:  "acme/example",
			transport: source.HTTPS,
		},
		{
			name:      "ssh url with nested groups",
			in:        "ssh://git@gitlab.com/acme/group/project.git",
			want:      "ssh://git@gitlab.com/acme/group/project.git",
			wantHost:  "gitlab.com",
			wantPath:  "acme/group/project",
			transport: source.SSH,
		},
		{
			name:      "shorthand expands to configured https",
			in:        "gh:acme/example",
			want:      "https://github.com/acme/example.git",
			wantHost:  "github.com",
			wantPath:  "acme/example",
			transport: source.HTTPS,
		},
		{
			name:      "shorthand expands to configured ssh",
			in:        "glab:acme/group/project",
			want:      "ssh://git@gitlab.com/acme/group/project.git",
			wantHost:  "gitlab.com",
			wantPath:  "acme/group/project",
			transport: source.SSH,
		},
		{
			name:      "shorthand tolerates surrounding space and .git",
			in:        "  glab:acme/group/project.git ",
			want:      "ssh://git@gitlab.com/acme/group/project.git",
			wantHost:  "gitlab.com",
			wantPath:  "acme/group/project",
			transport: source.SSH,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := source.ParseAddr(tc.in, testHosts())
			if err != nil {
				t.Fatalf("ParseAddr(%q): unexpected error: %v", tc.in, err)
			}
			if got := a.CloneURL(); got != tc.want {
				t.Errorf("CloneURL() = %q, want %q", got, tc.want)
			}
			if a.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", a.Host, tc.wantHost)
			}
			if a.Path != tc.wantPath {
				t.Errorf("Path = %q, want %q", a.Path, tc.wantPath)
			}
			if a.Transport != tc.transport {
				t.Errorf("Transport = %v, want %v", a.Transport, tc.transport)
			}
		})
	}
}

// TestParseAddr_DefaultHosts checks the built-in shorthands are usable with no
// configuration at all.
func TestParseAddr_DefaultHosts(t *testing.T) {
	for in, want := range map[string]string{
		"gh:acme/example":         "https://github.com/acme/example.git",
		"glab:acme/group/project": "https://gitlab.com/acme/group/project.git",
	} {
		a, err := source.ParseAddr(in, nil)
		if err != nil {
			t.Fatalf("ParseAddr(%q): unexpected error: %v", in, err)
		}
		if got := a.CloneURL(); got != want {
			t.Errorf("ParseAddr(%q).CloneURL() = %q, want %q", in, got, want)
		}
	}
}

// TestParseAddr_HostsAreConfiguration checks the caller can redefine a
// built-in shorthand: hosts are configuration, not constants.
func TestParseAddr_HostsAreConfiguration(t *testing.T) {
	hosts := map[string]source.HostConfig{
		"gh": {Host: "git.example.org", Transport: source.SSH},
	}
	a, err := source.ParseAddr("gh:acme/example", hosts)
	if err != nil {
		t.Fatalf("ParseAddr: unexpected error: %v", err)
	}
	if got, want := a.CloneURL(), "ssh://git@git.example.org/acme/example.git"; got != want {
		t.Errorf("CloneURL() = %q, want %q", got, want)
	}
}

// TestParseAddr_RoundTrip checks a parsed address re-parses to itself.
func TestParseAddr_RoundTrip(t *testing.T) {
	for _, in := range []string{
		"gh:acme/example",
		"glab:acme/group/project",
		"https://github.com/acme/example.git",
		"ssh://git@gitlab.com/acme/group/project.git",
	} {
		first, err := source.ParseAddr(in, testHosts())
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", in, err)
		}
		second, err := source.ParseAddr(first.CloneURL(), testHosts())
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", first.CloneURL(), err)
		}
		if first != second {
			t.Errorf("round trip of %q: %+v != %+v", in, first, second)
		}
		if first.CacheKey() != second.CacheKey() {
			t.Errorf("round trip of %q: cache key %q != %q", in, first.CacheKey(), second.CacheKey())
		}
	}
}

// TestAddr_CacheKey checks the key identifies host and repository and is safe
// to use as a filesystem path segment sequence.
func TestAddr_CacheKey(t *testing.T) {
	https, err := source.ParseAddr("https://gitlab.com/acme/group/project.git", testHosts())
	if err != nil {
		t.Fatal(err)
	}
	ssh, err := source.ParseAddr("glab:acme/group/project", testHosts())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := https.CacheKey(), "gitlab.com/acme/group/project"; got != want {
		t.Errorf("CacheKey() = %q, want %q", got, want)
	}
	// The same repository reached over a different transport is the same
	// repository, so it must share a cache entry.
	if https.CacheKey() != ssh.CacheKey() {
		t.Errorf("cache key differs by transport: %q vs %q", https.CacheKey(), ssh.CacheKey())
	}
}

func TestParseAddr_Errors(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{"empty"}},
		{"blank", "   ", []string{"empty"}},
		{"git protocol", "git://github.com/acme/example.git", []string{"git://", "https://", "ssh://"}},
		{"unknown shorthand", "codeberg:acme/example", []string{`"codeberg"`, "gh", "glab"}},
		{"unknown scheme", "ftp://github.com/acme/example.git", []string{"ftp"}},
		{"shorthand without repo", "gh:acme", []string{"gh:acme", "owner/repo"}},
		{"shorthand with empty path", "gh:", []string{"gh:"}},
		{"shorthand with leading slash", "gh:/acme/example", []string{"gh:/acme/example"}},
		{"url without repo path", "https://github.com/acme", []string{"owner/repo"}},
		{"url without host", "https:///acme/example", []string{"host"}},
		{"bare path", "github.com/acme/example", []string{"github.com/acme/example"}},
		{"malformed url", "https://exa mple.com/a/b", []string{"https://exa mple.com/a/b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := source.ParseAddr(tc.in, testHosts())
			if err == nil {
				t.Fatalf("ParseAddr(%q) = %+v, want error", tc.in, a)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("ParseAddr(%q) error %q does not mention %q", tc.in, err, want)
				}
			}
		})
	}
}
