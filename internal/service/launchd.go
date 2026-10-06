package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNoConsole means nobody is logged in at the Mac's console, so there is
// no GUI launchd domain to run the server in (docs/SPEC.md §3.1).
var ErrNoConsole = errors.New("nobody is logged in at the Mac's console, so the server can't run in the desktop's session " +
	"and its sessions couldn't use the keychain (gh, git push over https); log in on the Mac (the screen can stay locked) " +
	"and try again, or start a server without the keychain: tm server start --no-launchd")

// LaunchEnvVar turns the launchd start off when set to "off": tests, and
// starting a server without the keychain on purpose.
const LaunchEnvVar = "TERMINATR_LAUNCHD"

// Wanted reports whether starts go through launchd on goos: on macOS,
// unless TERMINATR_LAUNCHD=off.
func Wanted(goos string, getenv func(string) string) bool {
	return goos == "darwin" && getenv(LaunchEnvVar) != "off"
}

// Current is the service configuration for this tm, this user and the
// server's run and log dirs, taking TERMINATR_HOME, TERMINATR_SOCKET and
// PATH from getenv and the server's environment from env.
func Current(getenv func(string) string, env []string, home, runDir, logDir string) (Config, error) {
	bin, err := os.Executable()
	if err != nil {
		return Config{}, err
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	c := Config{
		GOOS: runtime.GOOS, UID: os.Getuid(), UserHome: userHome, Bin: bin,
		Socket: getenv("TERMINATR_SOCKET"), RunDir: runDir, LogDir: logDir,
		Path: getenv("PATH"), Env: LaunchEnv(env, getenv),
	}
	if getenv("TERMINATR_HOME") != "" {
		c.Home = home
	}
	return c, nil
}

// keepEnv and keepPrefix name what of the caller's environment the server
// (and through it the agents it starts) needs: the search path, who and
// where the user is, the terminal and locale, the ssh agent, and the
// settings of the tools threads run (git, gh, Go, Node, Rust, Homebrew,
// the compilers). Nothing else is kept, so a token someone exported in
// their shell never reaches launch.json; an agent logs in the way it
// always does (Claude's keychain, `gh auth login`).
var keepEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"LANG": true, "LANGUAGE": true, "TZ": true, "COLORTERM": true,
	"EDITOR": true, "VISUAL": true, "PAGER": true, "MANPATH": true, "INFOPATH": true,
	"SSH_AUTH_SOCK": true, "SDKROOT": true, "DEVELOPER_DIR": true, "JAVA_HOME": true,
	"CLAUDE_CONFIG_DIR": true,
	"GOPATH":            true, "GOROOT": true, "GOFLAGS": true, "GOPROXY": true, "GOMODCACHE": true, "GOCACHE": true,
	"GOBIN": true, "GOTOOLCHAIN": true, "GOSUMDB": true, "GONOSUMDB": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "ALL_PROXY": true,
}

var keepPrefix = []string{
	"TERM", "LC_", "XDG_", "TERMINATR_", "GIT_", "GH_", "HOMEBREW_",
	"NVM_", "CARGO_", "RUSTUP_", "VOLTA_", "PNPM_", "BUN_",
}

// secretName says whether an environment variable's name is one that
// holds a secret, whatever else says it is needed.
func secretName(k string) bool {
	k = strings.ToUpper(k)
	for _, w := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE", "API_KEY", "APIKEY", "ACCESS_KEY", "AUTH_KEY"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return strings.HasSuffix(k, "_KEY") || strings.HasSuffix(k, "_AUTH")
}

// keepVar says whether the server keeps variable k=v from the caller.
func keepVar(k, v string) bool {
	if k == "" || secretName(k) {
		return false
	}
	// A proxy's address may carry its user and password.
	if strings.HasSuffix(strings.ToUpper(k), "_PROXY") && strings.Contains(v, "@") {
		return false
	}
	if keepEnv[k] {
		return true
	}
	for _, p := range keepPrefix {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// LaunchEnv is what of env the server gets under launchd: the variables
// keepVar keeps, the ones a server started from that terminal would
// inherit that it needs, and never a token or secret. An SSH login's
// SSH_AUTH_SOCK is dropped too (it dies with the login; launchd's stands
// in), while a local terminal's (say, a password manager's agent) stays.
func LaunchEnv(env []string, getenv func(string) string) []string {
	overSSH := getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" || getenv("SSH_CLIENT") != ""
	var out []string
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !keepVar(k, v) || (overSSH && k == "SSH_AUTH_SOCK") || k == "TMPDIR" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// launchFile is the launch file's content.
type launchFile struct {
	Env []string `json:"env"`
}

// LaunchFile is the path of the file that hands the starting tm's
// environment to the server launchd starts. It is private (0600), though
// LaunchEnv keeps no secrets in it either.
func (c Config) LaunchFile() string { return filepath.Join(c.RunDir, "launch.json") }

func (c Config) writeLaunchFile() error {
	if c.RunDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.RunDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(launchFile{Env: c.Env})
	if err != nil {
		return err
	}
	tmp := c.LaunchFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.LaunchFile())
}

// ApplyLaunchFile sets this process's environment from the launch file at
// path, over what launchd gave it. A missing file (a login start before
// any tm started the server) changes nothing.
func ApplyLaunchFile(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var lf launchFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, kv := range lf.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			os.Setenv(k, v)
		}
	}
	return nil
}

// Start starts the server through launchd in the user's GUI domain
// (gui/<uid>), whatever session the caller is in: it writes the launch
// file, loads the job if it isn't loaded or its plist changed, and
// kickstarts it. The job is the login service when it is installed, and
// otherwise an on-demand job whose plist lives in the run dir, so a start
// never makes the server start at login. It returns ErrNoConsole when
// there is no GUI domain. The caller waits for the server to answer.
func (c Config) Start() error {
	if c.GOOS != "darwin" {
		return ErrUnsupported
	}
	if !filepath.IsAbs(c.Bin) {
		return fmt.Errorf("tm path %q is not absolute", c.Bin)
	}
	if !c.HasConsole() {
		return ErrNoConsole
	}
	if err := c.writeLaunchFile(); err != nil {
		return err
	}
	if c.LogDir != "" {
		if err := os.MkdirAll(c.LogDir, 0o700); err != nil {
			return err
		}
	}
	path, err := c.File()
	if err != nil {
		return err
	}
	atLoad := true
	if _, err := os.Stat(path); err != nil {
		path, atLoad = filepath.Join(c.RunDir, c.JobLabel()+".plist"), false
	}
	// The plist names the tm binary by its real path, which changes when
	// it is upgraded: rewrite and reload a plist that differs.
	data := c.plist(atLoad)
	old, _ := os.ReadFile(path)
	loaded := c.run("launchctl", "print", c.target()) == nil
	if !loaded || !bytes.Equal(old, data) {
		if loaded {
			c.run("launchctl", "bootout", c.target())
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		if err := c.bootstrap(path); err != nil {
			return err
		}
	}
	return c.run("launchctl", "kickstart", c.target())
}

// bootstrap loads the plist at path, retrying for a moment: right after
// a bootout launchd may still be unloading the label.
func (c Config) bootstrap(path string) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = c.run("launchctl", "bootstrap", c.domain(), path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// HasConsole reports whether the user's GUI launchd domain exists, which
// takes someone logged in at the Mac's console (the screen may be locked).
func (c Config) HasConsole() bool { return c.run("launchctl", "print", c.domain()) == nil }

// ServiceLog is where launchd writes the server's own output.
func (c Config) ServiceLog() string { return filepath.Join(c.LogDir, "service.log") }
