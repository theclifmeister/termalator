// Package update implements `tm update` (docs/SPEC.md §10.1): how this tm
// was installed, the latest release on GitHub, and replacing a directly
// installed binary with it after checking its checksum and, on macOS, its
// Developer ID signature.
//
// Homebrew owns the files it installs, so a Homebrew install is only ever
// told to `brew upgrade`; a source build is told to rebuild.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Method is how a tm binary was installed.
type Method string

const (
	Dev      Method = "source"   // built from source (make, go build)
	Homebrew Method = "homebrew" // the termalator formula
	Direct   Method = "direct"   // a release archive, unpacked by hand or by tm update
)

// Formula is the Homebrew formula's name (Formula/termalator.rb).
const Formula = "termalator"

// Install says how the running tm was installed.
type Install struct {
	Method Method `json:"method"`
	// Path is the executable with symlinks resolved: the file an update
	// replaces.
	Path string `json:"path"`
	// Upgrade is the command that upgrades a Homebrew install.
	Upgrade string `json:"upgrade,omitempty"`
}

// Detect classifies the binary at exe. channel is version.Channel: empty
// for a source build, "release" or "snapshot" for one built by goreleaser.
func Detect(exe, channel string) Install {
	path := exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		path = r
	}
	in := Install{Path: path}
	slash := filepath.ToSlash(path)
	switch {
	case channel == "":
		in.Method = Dev
	case strings.Contains(slash, "/Cellar/") || strings.Contains(slash, "/Caskroom/"):
		in.Method = Homebrew
		in.Upgrade = "brew upgrade " + Formula
	default:
		in.Method = Direct
	}
	return in
}

// Release is one GitHub release.
type Release struct {
	Tag    string  `json:"tag_name"`
	URL    string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Asset is one file of a release.
type Asset struct {
	Name     string `json:"name"`
	Download string `json:"browser_download_url"`
}

func (r Release) asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

const (
	// DefaultAPI is the release API of the termalator repository.
	DefaultAPI = "https://api.github.com/repos/theclifmeister/termalator"
	// DefaultWeb is the repository's web address, whose releases/latest
	// redirects to the latest release's tag.
	DefaultWeb = "https://github.com/theclifmeister/termalator"
)

// EnvAPI overrides DefaultAPI (tests point it at a local server); "off"
// disables every network check, so `tm doctor` makes no request.
const EnvAPI = "TERMALATOR_UPDATE_URL"

// ErrOff says EnvAPI turned update checks off.
var ErrOff = errors.New("update checks are off (" + EnvAPI + "=off)")

// releaseFiles are the assets every release has (.goreleaser.yaml); the
// web fallback, which sees no asset list, assumes them.
var releaseFiles = []string{
	"checksums.txt",
	ArchiveName("darwin", "arm64"), ArchiveName("darwin", "amd64"),
	ArchiveName("linux", "arm64"), ArchiveName("linux", "amd64"),
}

// Client finds and downloads releases, unauthenticated: the repository
// is public.
type Client struct {
	API  string
	Web  string
	HTTP *http.Client
}

// NewClient reads the API override from getenv.
func NewClient(getenv func(string) string) *Client {
	c := &Client{API: DefaultAPI, Web: DefaultWeb, HTTP: &http.Client{Timeout: 2 * time.Minute}}
	if u := getenv(EnvAPI); u != "" {
		c.API = strings.TrimRight(u, "/")
	}
	return c
}

func (c *Client) get(ctx context.Context, url, accept string) ([]byte, error) {
	if c.API == "off" {
		return nil, ErrOff
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "tm-update")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Release archives are a few tens of MB; refuse anything absurd.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
			msg = "API rate limit reached"
		}
		return nil, fmt.Errorf("GET %s: %s: %s", url, resp.Status, msg)
	}
	return body, nil
}

// Latest returns the latest published release (drafts and prereleases
// are not "latest" on GitHub). The API allows 60 unauthenticated requests
// an hour per address; when it refuses (or fails otherwise), the tag
// comes from the releases/latest redirect of the web site instead.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	r, err := c.latestAPI(ctx)
	if err == nil || errors.Is(err, ErrOff) {
		return r, err
	}
	r, werr := c.latestWeb(ctx)
	if werr != nil {
		return Release{}, fmt.Errorf("%v; %v", err, werr)
	}
	return r, nil
}

func (c *Client) latestAPI(ctx context.Context) (Release, error) {
	body, err := c.get(ctx, c.API+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	var r Release
	if err := json.Unmarshal(body, &r); err != nil {
		return Release{}, fmt.Errorf("release: %w", err)
	}
	if r.Tag == "" {
		return Release{}, errors.New("release: no tag_name")
	}
	return r, nil
}

// latestWeb reads the tag from where github.com/<repo>/releases/latest
// redirects (…/releases/tag/<tag>) and names the assets by convention.
func (c *Client) latestWeb(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.Web+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("User-Agent", "tm-update")
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return Release{}, err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	_, tag, ok := strings.Cut(loc, "/releases/tag/")
	if resp.StatusCode/100 != 3 || !ok || tag == "" || strings.Contains(tag, "/") {
		return Release{}, fmt.Errorf("%s/releases/latest: no release (%s)", c.Web, resp.Status)
	}
	r := Release{Tag: tag, URL: c.Web + "/releases/tag/" + tag}
	for _, f := range releaseFiles {
		r.Assets = append(r.Assets, Asset{Name: f, Download: c.Web + "/releases/download/" + tag + "/" + f})
	}
	return r, nil
}

// fetch downloads an asset from its public URL.
func (c *Client) fetch(ctx context.Context, a Asset) ([]byte, error) {
	return c.get(ctx, a.Download, "application/octet-stream")
}

// Newer reports whether release version latest is newer than current.
// Both are "v1.2.3" with an optional "-prerelease"; ok is false when
// either doesn't parse (a source build's git describe output, say).
func Newer(latest, current string) (newer, ok bool) {
	l, lok := parse(latest)
	c, cok := parse(current)
	if !lok || !cok {
		return false, false
	}
	for i := 0; i < 3; i++ {
		if l.n[i] != c.n[i] {
			return l.n[i] > c.n[i], true
		}
	}
	// 1.2.3 is newer than 1.2.3-rc1 (and than 1.2.3-snapshot-abc).
	switch {
	case l.pre == c.pre:
		return false, true
	case l.pre == "":
		return true, true
	case c.pre == "":
		return false, true
	}
	return l.pre > c.pre, true
}

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) (semver, bool) {
	v = strings.TrimPrefix(v, "v")
	v, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var s semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		s.n[i] = n
	}
	s.pre = pre
	return s, true
}

// ArchiveName is the release archive for a platform (.goreleaser.yaml).
func ArchiveName(goos, goarch string) string {
	return "tm_" + goos + "_" + goarch + ".tar.gz"
}

// Verifier checks a downloaded binary before it replaces the running one.
// On macOS it requires a valid Developer ID signature (of team, when team
// is set); elsewhere it does nothing.
type Verifier func(path, team string) (string, error)

// Download fetches the platform's archive and checksums.txt from r,
// checks the archive's sha256, and writes its tm into a new file in dir
// (so it can be renamed over the old one). It returns that file's path;
// the caller removes it on failure.
func (c *Client) Download(ctx context.Context, r Release, goos, goarch, dir string) (string, error) {
	name := ArchiveName(goos, goarch)
	a, ok := r.asset(name)
	if !ok {
		return "", fmt.Errorf("release %s has no %s", r.Tag, name)
	}
	sa, ok := r.asset("checksums.txt")
	if !ok {
		return "", fmt.Errorf("release %s has no checksums.txt", r.Tag)
	}
	sums, err := c.fetch(ctx, sa)
	if err != nil {
		return "", err
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return "", err
	}
	archive, err := c.fetch(ctx, a)
	if err != nil {
		return "", err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("%s: sha256 %x does not match checksums.txt (%s)", name, got, want)
	}
	return extractTM(archive, dir)
}

func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			if len(f[0]) != 64 {
				break
			}
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no sha256 for %s", name)
}

// extractTM writes the archive's top-level tm into a new 0755 file in dir.
func extractTM(archive []byte, dir string) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", errors.New("the archive holds no tm")
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag != tar.TypeReg || filepath.Clean(h.Name) != "tm" {
			continue
		}
		f, err := os.CreateTemp(dir, ".tm-update-*")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		if err := f.Chmod(0o755); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		if err := f.Close(); err != nil {
			os.Remove(f.Name())
			return "", err
		}
		return f.Name(), nil
	}
}

// Codesign is the macOS Verifier: `codesign --verify --strict`, then the
// signing authority must be a Developer ID Application with the hardened
// runtime, of team when it is set. It returns the team identifier.
func Codesign(path, team string) (string, error) {
	if out, err := exec.Command("codesign", "--verify", "--strict", path).CombinedOutput(); err != nil {
		return "", fmt.Errorf("codesign --verify: %s", firstLine(string(out), err))
	}
	out, err := exec.Command("codesign", "-d", "--verbose=2", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("codesign -d: %s", firstLine(string(out), err))
	}
	info := string(out)
	if !strings.Contains(info, "\nAuthority=Developer ID Application:") {
		return "", errors.New("not signed with a Developer ID")
	}
	if !hardened.MatchString(info) {
		return "", errors.New("signed without the hardened runtime")
	}
	got := ""
	for _, l := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(l, "TeamIdentifier="); ok {
			got = strings.TrimSpace(v)
		}
	}
	if team != "" && got != team {
		return "", fmt.Errorf("signed by team %q, want %q", got, team)
	}
	return got, nil
}

// hardened matches codesign's "flags=0x10000(runtime)".
var hardened = regexp.MustCompile(`flags=0x[0-9a-f]+\([^)]*\bruntime\b`)

func firstLine(s string, err error) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if s == "" {
		return err.Error()
	}
	return s
}

// DefaultVerifier is Codesign on macOS and nothing elsewhere.
func DefaultVerifier() Verifier {
	if runtime.GOOS == "darwin" {
		return Codesign
	}
	return nil
}

// Replace renames the new binary over path, keeping path's mode. The
// rename is atomic: a tm starting meanwhile runs the old or the new
// binary, never half of one. A running server keeps its own copy
// (internal/server, pinBinary).
func Replace(newBin, path string) error {
	if fi, err := os.Stat(path); err == nil {
		if err := os.Chmod(newBin, fi.Mode().Perm()|0o100); err != nil {
			return err
		}
	}
	return os.Rename(newBin, path)
}
