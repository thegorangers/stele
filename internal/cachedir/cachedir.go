// Package cachedir decides where fetched repositories are kept.
//
// The design leaves this to the command line on purpose: the packages that
// fetch and resolve take a cache root as a parameter, so that nothing below
// the CLI reads the environment. This is the one place that does.
package cachedir

import (
	"errors"
	"os"
	"path/filepath"
)

// Name is the directory this tool owns inside a cache root.
const Name = "stele"

// EnvOverride names the environment variable that points the cache somewhere
// else. It exists mainly so that a test run can be given a cache of its own
// without a flag reaching every call site.
const EnvOverride = "STELE_CACHE_DIR"

// Root returns the cache root, preferring an explicit override, then
// $STELE_CACHE_DIR, then XDG_CACHE_HOME, then the user's home directory.
func Root(override string) (string, error) {
	if override == "" {
		override = os.Getenv(EnvOverride)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Not fatal here: an override or XDG_CACHE_HOME can still answer, and
		// only the fallback needs a home directory.
		home = ""
	}
	return resolve(override, os.Getenv("XDG_CACHE_HOME"), home)
}

// resolve is the whole decision, as a pure function, so that it can be tested
// without setting environment variables for the rest of the suite.
//
// A relative XDG_CACHE_HOME is ignored rather than honoured: the specification
// says the value must be absolute, and honouring it would put the cache
// wherever the tool happened to be invoked from — a different cache per
// working directory, which is the kind of thing that shows up as an
// unexplained slowdown rather than as an error.
func resolve(override, xdg, home string) (string, error) {
	if override != "" {
		return override, nil
	}
	if filepath.IsAbs(xdg) {
		return filepath.Join(xdg, Name), nil
	}
	if home == "" {
		return "", errors.New("cannot locate a cache directory: neither XDG_CACHE_HOME nor a home directory is set; pass --cache-dir")
	}
	return filepath.Join(home, ".cache", Name), nil
}
