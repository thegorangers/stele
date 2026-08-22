// Package source resolves the address of a dependency: the repository it
// lives in and the URL git is asked to clone.
package source

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Transport is the protocol a repository is reached over. There is no
// git:// transport on purpose: major hosts disabled it, and it is neither
// encrypted nor authenticated.
type Transport int

const (
	// HTTPS clones over https://. Credentials come from git credential.
	HTTPS Transport = iota
	// SSH clones over ssh://git@host/. Credentials come from the ssh agent.
	SSH
)

// String renders the transport as its URL scheme.
func (t Transport) String() string {
	switch t {
	case SSH:
		return "ssh"
	case HTTPS:
		return "https"
	default:
		return fmt.Sprintf("Transport(%d)", int(t))
	}
}

// HostConfig describes the host a shorthand prefix expands to.
//
// Transport is part of the configuration, not of the shorthand: the same
// manifest has to work from a workstation that clones over SSH and from CI
// that clones over HTTPS with a token.
type HostConfig struct {
	// Host is the domain name of the git host, without a scheme.
	Host string
	// Transport is the protocol shorthands of this host expand to.
	Transport Transport
}

// DefaultHosts returns the built-in shorthand prefixes. They are defaults, not
// constants: a caller that configures a prefix replaces the built-in entry,
// and a caller may add prefixes for any other host.
func DefaultHosts() map[string]HostConfig {
	return map[string]HostConfig{
		"gh":   {Host: "github.com", Transport: HTTPS},
		"glab": {Host: "gitlab.com", Transport: HTTPS},
	}
}

// Addr is a parsed dependency address.
type Addr struct {
	// Host is the domain name of the git host, for example "github.com".
	Host string
	// Path is the repository path on that host, without a leading slash and
	// without the ".git" suffix, for example "acme/group/project".
	Path string
	// Transport is the protocol to clone over.
	Transport Transport
}

// CloneURL renders the address as a URL git can clone.
func (a Addr) CloneURL() string {
	if a.Transport == SSH {
		return "ssh://git@" + a.Host + "/" + a.Path + ".git"
	}
	return "https://" + a.Host + "/" + a.Path + ".git"
}

// CacheKey identifies the repository independently of the transport it is
// reached over, as "<host>/<path>". Fetching the same repository over SSH from
// a workstation and over HTTPS from CI must not produce two cache entries, and
// the transport is not part of what the fetched content is. Callers add the
// resolved commit SHA to obtain the full immutable cache coordinate.
//
// Every segment is a plain path element already validated by parsing, so the
// key is usable as a sequence of directory names.
func (a Addr) CacheKey() string { return a.Host + "/" + a.Path }

// ParseAddr parses a dependency address.
//
// Three forms are accepted:
//
//	https://github.com/acme/example.git
//	ssh://git@gitlab.com/acme/group/project.git
//	gh:acme/example            (a shorthand, expanded through hosts)
//
// hosts maps a shorthand prefix to the host it expands to. A nil or empty map
// means DefaultHosts; a non-empty map replaces the defaults entirely, so a
// caller stays in control of what prefixes exist.
func ParseAddr(s string, hosts map[string]HostConfig) (Addr, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Addr{}, fmt.Errorf("dependency address is empty; expected a shorthand such as gh:acme/example, or a full https:// or ssh:// URL")
	}
	if len(hosts) == 0 {
		hosts = DefaultHosts()
	}
	if strings.Contains(raw, "://") {
		return parseURL(raw)
	}
	prefix, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return Addr{}, fmt.Errorf("%q is not a dependency address: it has neither a scheme nor a shorthand prefix; write a full https:// or ssh:// URL, or a shorthand such as %s", raw, exampleShorthand(hosts))
	}
	h, ok := hosts[prefix]
	if !ok {
		return Addr{}, fmt.Errorf("%q: unknown shorthand prefix %q; configured prefixes are %s", raw, prefix, strings.Join(prefixNames(hosts), ", "))
	}
	if h.Host == "" {
		return Addr{}, fmt.Errorf("%q: shorthand prefix %q is configured with an empty host", raw, prefix)
	}
	if strings.HasPrefix(rest, "/") {
		// A shorthand carries a repository path, not a URL path: a leading
		// slash is a typo, and accepting it would hide it.
		return Addr{}, fmt.Errorf("%q: a shorthand must not have a slash after the prefix; write %s:%s", raw, prefix, strings.TrimLeft(rest, "/"))
	}
	path, err := repoPath(rest)
	if err != nil {
		return Addr{}, fmt.Errorf("%q: %w", raw, err)
	}
	return Addr{Host: h.Host, Path: path, Transport: h.Transport}, nil
}

// parseURL parses a full URL form of an address.
func parseURL(raw string) (Addr, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Addr{}, fmt.Errorf("%q is not a valid URL: %w", raw, err)
	}
	var transport Transport
	switch u.Scheme {
	case "https":
		transport = HTTPS
	case "ssh":
		transport = SSH
	case "git":
		return Addr{}, fmt.Errorf("%q: the git:// protocol is not supported: it is unauthenticated and unencrypted, and major hosts have disabled it; use https:// or ssh:// instead", raw)
	default:
		return Addr{}, fmt.Errorf("%q: unsupported URL scheme %q; use https:// or ssh://", raw, u.Scheme)
	}
	if u.Hostname() == "" {
		return Addr{}, fmt.Errorf("%q: the URL has no host", raw)
	}
	path, err := repoPath(u.Path)
	if err != nil {
		return Addr{}, fmt.Errorf("%q: %w", raw, err)
	}
	return Addr{Host: u.Hostname(), Path: path, Transport: transport}, nil
}

// repoPath normalises the repository part of an address: no surrounding
// slashes, no ".git" suffix, at least two segments, no empty or relative
// segment.
func repoPath(p string) (string, error) {
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if p == "" {
		return "", fmt.Errorf("the repository part is missing; expected owner/repo")
	}
	segments := strings.Split(p, "/")
	if len(segments) < 2 {
		return "", fmt.Errorf("the repository part %q is incomplete; expected owner/repo, optionally with subgroups", p)
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("the repository part %q has an empty or relative segment", p)
		}
	}
	return strings.Join(segments, "/"), nil
}

// prefixNames returns the configured shorthand prefixes, sorted, so that error
// messages are stable.
func prefixNames(hosts map[string]HostConfig) []string {
	names := make([]string, 0, len(hosts))
	for name := range hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// exampleShorthand renders one configured prefix as an example.
func exampleShorthand(hosts map[string]HostConfig) string {
	names := prefixNames(hosts)
	if len(names) == 0 {
		return "gh:acme/example"
	}
	return names[0] + ":acme/example"
}
