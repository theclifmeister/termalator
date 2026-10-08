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

	"github.com/theclifmeister/terminatr/internal/plat/fsx"
)

// ErrNoConsole means nobody is logged in at the Mac's console, so there is
// no GUI launchd domain to run the server in (docs/SPEC.md §3.1).
var ErrNoConsole = errors.New("nobody is logged in at the Mac's console, so the server can't run in the desktop's session " +
	"and its sessions couldn't use the keychain (the PR CLI's login, git credential helpers); log in on the Mac (the screen can stay locked) " +
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
	source := bin
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	c := Config{
		GOOS: runtime.GOOS, UID: os.Getuid(), UserHome: userHome, Bin: bin, Source: source,
		Socket: getenv("TERMINATR_SOCKET"), RunDir: runDir, LogDir: logDir,
		Path: getenv("PATH"), Env: LaunchEnv(env, getenv),
	}
	if getenv("TERMINATR_HOME") != "" {
		c.Home = home
	}
	return c, nil
}

// dropEnv are variables a server started from a terminal would inherit
// that belong to that terminal or login, not to the server: launchd's
// own values (or none) stand in for them.
var dropEnv = []string{
	"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY",
	"SECURITYSESSIONID", "XPC_SERVICE_NAME", "XPC_FLAGS", "__CFBundleIdentifier",
	"LaunchInstanceID", "TMPDIR", "PWD", "OLDPWD", "SHLVL", "_",
}

// LaunchEnv is what of env the server gets under launchd: everything a
// server started from that terminal would inherit, less dropEnv. An SSH
// login's SSH_AUTH_SOCK goes too (it dies with the login; launchd's
// stands in), while a local terminal's (say, a password manager's agent)
// stays.
func LaunchEnv(env []string, getenv func(string) string) []string {
	drop := map[string]bool{}
	for _, k := range dropEnv {
		drop[k] = true
	}
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" || getenv("SSH_CLIENT") != "" {
		drop["SSH_AUTH_SOCK"] = true
	}
	var out []string
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && k != "" && !drop[k] {
			out = append(out, kv)
		}
	}
	return out
}

// launchFile is the launch file's content.
type launchFile struct {
	Env []string `json:"env"`
	// Bin is the tm that started the server, for the server to pin and
	// run (Program runs the pin, which may hold an older build).
	Bin string `json:"bin,omitempty"`
}

// LaunchFile is the path of the file that hands the starting tm's
// environment to the server launchd starts. It is private (0600): the
// environment may hold secrets, which the plist must not.
func (c Config) LaunchFile() string { return filepath.Join(c.RunDir, "launch.json") }

func (c Config) writeLaunchFile() error {
	if c.RunDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.RunDir, 0o700); err != nil {
		return err
	}
	bin := c.Source
	if bin == "" {
		bin = c.Bin
	}
	data, err := json.Marshal(launchFile{Env: c.Env, Bin: bin})
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(c.LaunchFile(), data, 0o600)
}

// ApplyLaunchFile sets this process's environment from the launch file at
// path, over what launchd gave it, and returns the tm that started the
// server ("" when the file doesn't say). A missing file (a login start
// before any tm started the server) changes nothing.
func ApplyLaunchFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var lf launchFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	for _, kv := range lf.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			os.Setenv(k, v)
		}
	}
	return lf.Bin, nil
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
	// Rewrite and reload a plist that differs (an older tm's named its
	// versioned binary rather than the pin).
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
