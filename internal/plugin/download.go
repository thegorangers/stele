package plugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A plugin that is not a Go program cannot be installed by a Go toolchain, but
// that is not the same as being unpinnable. Nearly all of them publish a
// release binary at a stable address, and an address plus a digest is a pin of
// exactly the same strength as module@version: the digest names the bytes, and
// the address is only where to look for them.
//
// The rule this file exists to keep is the one the whole tool keeps: verify,
// do not trust. The digest is checked against the bytes as downloaded, before
// they are unpacked, before anything is made executable, before anything is
// run, and before anything reaches the cache. A cache entry is what a later
// run trusts without asking the network again, so an unverified byte must
// never become one.

// downloadLimit caps what will be read from a URL. A plugin binary is a few
// tens of megabytes at most; without a cap, a server that answers a plugin
// request with an endless stream fills the disk instead of failing.
const downloadLimit = 512 << 20

// EnsureURL returns the path of the binary published at url, downloading it
// first if the cache does not already hold it.
//
// digest is the sha256 the download must have, in hex. member names the file
// to take when the download is an archive, in the archive's own coordinates,
// and must be empty when it is a bare binary.
//
// The cache is keyed by the digest, which makes an entry immutable and shared:
// two repositories that declare the same digest use one copy, and re-declaring
// the same binary at a new address does not re-download it.
func (c Cache) EnsureURL(ctx context.Context, url, digest, member string) (string, error) {
	if c.Root == "" {
		return "", errors.New("plugin: no cache root for downloading plugins")
	}
	if url == "" || digest == "" {
		return "", errors.New("plugin: a downloaded plugin needs both a url and a sha256")
	}
	digest = strings.ToLower(digest)
	bin := c.URLPath(url, digest, member)
	if _, err := os.Stat(bin); err == nil {
		return bin, verifyFile(bin, c.recordedDigest(digest), url)
	}
	if err := c.download(ctx, url, digest, member, bin); err != nil {
		return "", err
	}
	return bin, nil
}

// URLPath is where the binary published at url with this digest lives, whether
// or not it has been downloaded. It is exported for the same reason Path is:
// a caller can say which binary would run without fetching anything.
func (c Cache) URLPath(url, digest, member string) string {
	return filepath.Join(c.Root, dirName, "url", strings.ToLower(digest), downloadName(url, member))
}

// recordedDigest is the file beside a cached entry holding the digest of the
// entry itself.
//
// For a bare binary the declared digest already covers the cached file, but
// for an archive it covers the archive, which is not kept. Without this the
// extracted member would be the one artefact in the tool that is verified once
// and trusted forever, which is the property a corrupted or hand-replaced
// cache entry exploits. So the member's own digest is recorded when it is
// extracted and re-checked on every use, exactly as a Go plugin's build
// metadata is re-read on every use.
func (c Cache) recordedDigest(digest string) string {
	return filepath.Join(c.Root, dirName, "url", digest, ".digest")
}

// downloadName is what the cached binary is called. It is cosmetic — the
// digest is the key — but it is what appears in error messages, in a report
// and in a process listing, so it is the name the publisher gave the file
// rather than something invented.
func downloadName(url, member string) string {
	name := member
	if name == "" {
		name = url
		if i := strings.IndexAny(name, "?#"); i >= 0 {
			name = name[:i]
		}
	}
	name = path.Base(strings.TrimSuffix(path.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/"))
	if name == "" || name == "." || name == "/" {
		return "plugin"
	}
	return name
}

// download fetches url, verifies it, and publishes the result at bin.
//
// Everything happens in a staging directory that is removed on any failure, so
// a failed or interrupted download never leaves a file a later run would take
// for a cache hit.
func (c Cache) download(ctx context.Context, url, digest, member, bin string) error {
	if err := os.MkdirAll(filepath.Join(c.Root, dirName), 0o755); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	stage, err := os.MkdirTemp(filepath.Join(c.Root, dirName), ".staging-")
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer os.RemoveAll(stage)

	body, err := fetch(ctx, url)
	if err != nil {
		return err
	}
	// Verification comes before every use of these bytes. Nothing below this
	// line may run before it.
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != digest {
		return fmt.Errorf("%s: the download does not match the declared sha256\n"+
			"  declared %s\n  received %s\n"+
			"Nothing was run and nothing was cached. Either the manifest names the wrong digest, "+
			"or what is being served is not what the manifest pinned.",
			url, digest, hex.EncodeToString(got[:]))
	}

	content, err := extract(url, body, member)
	if err != nil {
		return err
	}
	staged := filepath.Join(stage, downloadName(url, member))
	if err := os.WriteFile(staged, content, 0o755); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	sum := sha256.Sum256(content)
	if err := os.WriteFile(filepath.Join(stage, ".digest"), []byte(hex.EncodeToString(sum[:])), 0o644); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}

	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := os.Rename(filepath.Join(stage, ".digest"), c.recordedDigest(digest)); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := os.Rename(staged, bin); err != nil {
		// A concurrent download of the same digest is not a failure: the
		// entry is content-addressed, so whichever won produced these bytes.
		if _, statErr := os.Stat(bin); statErr == nil {
			return nil
		}
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	return nil
}

// fetch reads url over HTTP, refusing anything that is not an answer.
func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w\n"+
			"This step needs network access the first time a digest is used. "+
			"Once downloaded it is cached and later runs need no network.", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: the server answered %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, downloadLimit+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if len(body) > downloadLimit {
		return nil, fmt.Errorf("downloading %s: the answer is larger than %d bytes, which is not a plugin binary",
			url, downloadLimit)
	}
	return body, nil
}

// Archive kinds this tool unpacks. The list is short and closed on purpose:
// these two are what published plugin releases actually use, both are in the
// standard library, and neither needs an external tool. Anything else is
// refused by name rather than handed to the operating system as if it were a
// program.
const (
	extTarGz = ".tar.gz"
	extTgz   = ".tgz"
	extZip   = ".zip"
)

// extract returns the bytes of the plugin binary inside a download.
//
// Whether the download is an archive is decided by the address, not by
// sniffing the content: the address is in the manifest, where a reader can see
// it, and a rule a reader can apply themselves is what makes the error
// message below actionable.
func extract(url string, body []byte, member string) ([]byte, error) {
	ext := archiveExt(url)
	switch {
	case ext == "" && member == "":
		return body, nil
	case ext == "" && member != "":
		return nil, fmt.Errorf("%s: archive_path names %q, but the url does not name an archive "+
			"(%s, %s or %s); a bare binary has no members", url, member, extTarGz, extTgz, extZip)
	case member == "":
		return nil, fmt.Errorf("%s: the url names a %s archive, so archive_path must say which file in it is the plugin; "+
			"an archive is not a program, and guessing which of its files is one is a guess the sha256 cannot protect you from",
			url, ext)
	case ext == extZip:
		return fromZip(url, body, member)
	default:
		return fromTarGz(url, body, member)
	}
}

// archiveExt is the archive suffix of url's path, or empty when it names no
// archive this tool unpacks. Query and fragment are ignored: they are how a
// download is authorised or versioned, not what it is.
func archiveExt(url string) string {
	p := url
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.ToLower(p)
	for _, ext := range []string{extTarGz, extTgz, extZip} {
		if strings.HasSuffix(p, ext) {
			return ext
		}
	}
	return ""
}

func fromTarGz(url string, body []byte, member string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the archive: %w", url, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var held []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: reading the archive: %w", url, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(h.Name)
		held = append(held, name)
		if name == path.Clean(member) {
			b, err := io.ReadAll(io.LimitReader(tr, downloadLimit))
			if err != nil {
				return nil, fmt.Errorf("%s: reading %s from the archive: %w", url, member, err)
			}
			return b, nil
		}
	}
	return nil, missing(url, member, held)
}

func fromZip(url string, body []byte, member string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the archive: %w", url, err)
	}
	var held []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(f.Name)
		held = append(held, name)
		if name == path.Clean(member) {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s: reading %s from the archive: %w", url, member, err)
			}
			defer rc.Close()
			b, err := io.ReadAll(io.LimitReader(rc, downloadLimit))
			if err != nil {
				return nil, fmt.Errorf("%s: reading %s from the archive: %w", url, member, err)
			}
			return b, nil
		}
	}
	return nil, missing(url, member, held)
}

// missing reports a member that is not in the archive, and says what is. A
// message that only said the member was absent would leave the reader to
// download and open the archive by hand to find the one thing they need.
func missing(url, member string, held []string) error {
	const most = 20
	shown := held
	suffix := ""
	if len(shown) > most {
		shown, suffix = shown[:most], fmt.Sprintf(", and %d more", len(held)-most)
	}
	return fmt.Errorf("%s: the archive holds no file named %q; it holds: %s%s",
		url, member, strings.Join(shown, ", "), suffix)
}

// verifyFile re-checks a cache entry against the digest recorded beside it.
// A warm cache is checked for the same reason an installed Go plugin is: an
// entry that cannot be shown to be what it claims is not evidence of anything.
func verifyFile(bin, record, url string) error {
	want, err := os.ReadFile(record)
	if err != nil {
		return fmt.Errorf("%s carries no recorded digest, so it cannot be shown to be the file at %s: %w; "+
			"delete %s and let the tool download it again", bin, url, err, filepath.Dir(bin))
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		return fmt.Errorf("reading the cached plugin %s: %w", bin, err)
	}
	got := sha256.Sum256(b)
	if hex.EncodeToString(got[:]) != strings.TrimSpace(string(want)) {
		return fmt.Errorf("the cached plugin %s no longer matches the digest recorded when it was downloaded from %s; "+
			"delete %s and let the tool download it again", bin, url, filepath.Dir(bin))
	}
	return nil
}
