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
	"archive/zip"
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
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/plat/fsx"
	"github.com/theclifmeister/terminatr/internal/plat/proc"
)

// Method is how a tm binary was installed.
type Method string

const (
	Dev      Method = "source"   // built from source (make, go build)
	Homebrew Method = "homebrew" // the terminatr formula
	Direct   Method = "direct"   // a release archive, unpacked by hand or by tm update
)

// Formula is the Homebrew formula's name (Formula/terminatr.rb).
const Formula = "terminatr"

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
	// DefaultAPI is the release API of the terminatr repository.
	DefaultAPI = "https://api.github.com/repos/theclifmeister/terminatr"
	// DefaultWeb is the repository's web address, whose releases/latest
	// redirects to the latest release's tag.
	DefaultWeb = "https://github.com/theclifmeister/terminatr"
)

// EnvAPI overrides DefaultAPI (tests point it at a local server); "off"
// disables every network check, so `tm doctor` makes no request.
const EnvAPI = "TERMINATR_UPDATE_URL"

// ErrOff says EnvAPI turned update checks off.
var ErrOff = errors.New("update checks are off (" + EnvAPI + "=off)")

// releaseFiles are the assets every release has (.goreleaser.yaml); the
// web fallback, which sees no asset list, assumes them.
var releaseFiles = []string{
	"checksums.txt",
	ArchiveName("darwin", "arm64"), ArchiveName("darwin", "amd64"),
	ArchiveName("linux", "arm64"), ArchiveName("linux", "amd64"),
	ArchiveName("windows", "arm64"), ArchiveName("windows", "amd64"),
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

// ArchiveName is the release archive for a platform (.goreleaser.yaml): a
// tar.gz, and on Windows a zip.
func ArchiveName(goos, goarch string) string {
	if goos == "windows" {
		return "tm_" + goos + "_" + goarch + ".zip"
	}
	return "tm_" + goos + "_" + goarch + ".tar.gz"
}

// Files are the files of an archive that an update installs, by name in
// the install directory. The program is first. On Windows Microsoft's
// ConPTY comes with it (the three belong together).
func Files(goos string) []string {
	if goos == "windows" {
		return []string{"tm.exe", "conpty.dll", "OpenConsole.exe"}
	}
	return []string{"tm"}
}

// Verifier checks a downloaded binary before it replaces the running one.
// On macOS it requires a valid Developer ID signature (of team, when team
// is set); elsewhere it does nothing.
type Verifier func(path, team string) (string, error)

// Staged is one file of a downloaded release, in a new file next to where
// it will go.
type Staged struct {
	// Name is the file's name in the install directory ("tm").
	Name string
	// Path is where it is now.
	Path string
}

// Bundle is what Download stages: the program first, then its companions.
type Bundle []Staged

// Program is the new tm's path, for checking it before installing.
func (b Bundle) Program() string { return b[0].Path }

// Remove deletes whatever of the bundle is still staged.
func (b Bundle) Remove() {
	for _, s := range b {
		os.Remove(s.Path)
	}
}

// Download fetches the platform's archive and checksums.txt from r,
// checks the archive's sha256, and writes the files of Files(goos) into
// new files in dir (so they can be renamed into place). The caller
// removes them (Bundle.Remove) after Install, and on failure.
func (c *Client) Download(ctx context.Context, r Release, goos, goarch, dir string) (Bundle, error) {
	name := ArchiveName(goos, goarch)
	a, ok := r.asset(name)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s", r.Tag, name)
	}
	sa, ok := r.asset("checksums.txt")
	if !ok {
		return nil, fmt.Errorf("release %s has no checksums.txt", r.Tag)
	}
	sums, err := c.fetch(ctx, sa)
	if err != nil {
		return nil, err
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return nil, err
	}
	archive, err := c.fetch(ctx, a)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s: sha256 %x does not match checksums.txt (%s)", name, got, want)
	}
	return extract(archive, goos, dir)
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

// extract stages the files of Files(goos) from a release archive.
func extract(archive []byte, goos, dir string) (Bundle, error) {
	want := Files(goos)
	got := map[string]string{}
	stage := func(name string, r io.Reader) error {
		f, err := os.CreateTemp(dir, ".tm-update-*")
		if err != nil {
			return err
		}
		got[name] = f.Name()
		if _, err := io.Copy(f, r); err != nil {
			f.Close()
			return err
		}
		if err := f.Chmod(0o755); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	var err error
	if goos == "windows" {
		err = unzip(archive, want, stage)
	} else {
		err = untar(archive, want, stage)
	}
	var b Bundle
	for _, name := range want {
		if p, ok := got[name]; ok {
			b = append(b, Staged{Name: name, Path: p})
		} else if err == nil {
			err = fmt.Errorf("the archive holds no %s", name)
		}
	}
	if err != nil {
		b.Remove()
		return nil, err
	}
	return b, nil
}

func untar(archive []byte, want []string, stage func(string, io.Reader) error) error {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if name := filepath.Clean(h.Name); h.Typeflag == tar.TypeReg && slices.Contains(want, name) {
			if err := stage(name, tr); err != nil {
				return err
			}
		}
	}
}

func unzip(archive []byte, want []string, stage func(string, io.Reader) error) error {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := path.Clean(f.Name)
		if !f.Mode().IsRegular() || !slices.Contains(want, name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = stage(name, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
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

// Install puts the staged files into dir, over the old ones, and
// restores the old ones if one fails. Each goes in by a rename
// (fsx.SwapIn): a tm starting meanwhile runs the old or the new binary,
// never half of one, and a running one keeps its file (on Windows it
// moves aside; see CleanOld). The program goes in last, so a failure
// leaves tm as it was. A server has its own copy of its binary
// (internal/server, pinBinary).
//
// A file that can't be replaced fails with a *BusyError naming the
// processes that hold it, where the system can say.
func (b Bundle) Install(dir string) error {
	var undo []func() error
	for i := len(b) - 1; i >= 0; i-- {
		s := b[i]
		dst := filepath.Join(dir, s.Name)
		restore, err := fsx.SwapIn(s.Path, dst)
		if err != nil {
			for j := len(undo) - 1; j >= 0; j-- {
				undo[j]()
			}
			return busy(dst, err)
		}
		if restore != nil {
			undo = append(undo, restore)
		}
	}
	return nil
}

// CleanOld removes the files an earlier Install moved aside in dir, as
// far as nothing runs them. (Only Windows moves files aside.)
func CleanOld(dir, goos string) {
	for _, name := range Files(goos) {
		fsx.CleanAside(filepath.Join(dir, name))
	}
}

// BusyError says a file couldn't be replaced and who holds it.
type BusyError struct {
	Path    string
	Holders []proc.Holder
	Err     error
}

func (e *BusyError) Error() string {
	msg := fmt.Sprintf("can't replace %s: %v", e.Path, e.Err)
	if len(e.Holders) > 0 {
		var hs []string
		for _, h := range e.Holders {
			hs = append(hs, h.String())
		}
		msg += "; in use by " + strings.Join(hs, ", ") + " (close it and try again)"
	}
	return msg
}

func (e *BusyError) Unwrap() error { return e.Err }

func busy(path string, err error) error {
	hs, _ := proc.Holders(path)
	return &BusyError{Path: path, Holders: hs, Err: err}
}
