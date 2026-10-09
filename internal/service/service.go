// Package service writes and loads the server's service files (docs/SPEC.md
// §3.1): the optional start-at-login service (a launchd agent on macOS, a
// systemd user unit on Linux, a Run key value on Windows), and on macOS the launchd job every start
// goes through (Start), so the server runs in the desktop's session
// whatever session tm was started from.
package service

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/theclifmeister/terminatr/internal/plat/autostart"
)

// Names of the service.
const (
	Label    = "dev.terminatr.server"
	UnitName = "terminatr.service"
)

// Config is what a service file is rendered from.
type Config struct {
	GOOS string
	UID  int
	// UserHome is the user's home directory (~).
	UserHome string
	// Bin is the absolute path of tm.
	Bin string
	// Source is the path tm was started by, before resolving symlinks
	// (Homebrew's /opt/homebrew/bin/tm rather than its versioned keg),
	// written to the launch file for the server to pin (macOS). "" uses
	// Bin.
	Source string
	// Home is TERMINATR_HOME when set explicitly, else "".
	Home string
	// Socket is TERMINATR_SOCKET when set, else "".
	Socket string
	// RunDir is the server's run dir, where the launch file and the
	// on-demand job's plist live (macOS).
	RunDir string
	// Env is the environment the server gets on macOS, written to the
	// launch file: the starting tm's own, less what belongs to its
	// terminal or login (LaunchEnv).
	Env []string
	// LogDir is where launchd writes the server's stdout and stderr.
	LogDir string
	// Path is the PATH the server and its agents get: services start
	// with a minimal PATH that usually lacks claude and git.
	Path string
	// Run runs a service manager command; nil uses the real one.
	Run func(name string, args ...string) error
	// Output runs a command and returns its stdout; nil uses the real one.
	Output func(name string, args ...string) (string, error)
	// Store is the Windows startup list; nil uses the real one.
	Store autostart.Store
}

// PinDir and PinName place the server's own copy of its binary, the
// pin, under its run dir: RunDir/bin/tm (docs/SPEC.md §3.6), tm.exe on
// Windows (PinName is in pinname_*.go).
const PinDir = "bin"

// PinPath is the pin's path in runDir.
func PinPath(runDir string) string { return filepath.Join(runDir, PinDir, PinName) }

// Program is the binary the launchd job runs: the pin, never the
// versioned file Bin names. macOS privacy settings (TCC) hold the
// program launchd started the responsible process of every session with,
// and keep it when the server execs another binary, so a versioned path
// would add an entry, and ask again, with every release. Without a run
// dir it is Bin.
func (c Config) Program() string {
	if c.RunDir == "" {
		return c.Bin
	}
	return PinPath(c.RunDir)
}

// ErrUnsupported means there is no service manager support for the OS.
var ErrUnsupported = errors.New("start at login is supported on macOS (launchd), Linux (systemd --user) and Windows (the Run key) only")

// JobLabel is the launchd label: Label for the default home, and one per
// home otherwise, so a dev or test server never takes the real one's.
func (c Config) JobLabel() string {
	if c.Home == "" {
		return Label
	}
	h := sha256.Sum256([]byte(c.Home))
	return fmt.Sprintf("%s.%x", Label, h[:4])
}

// File is the service file's path.
func (c Config) File() (string, error) {
	switch c.GOOS {
	case "darwin":
		return filepath.Join(c.UserHome, "Library", "LaunchAgents", c.JobLabel()+".plist"), nil
	case "linux":
		return filepath.Join(c.UserHome, ".config", "systemd", "user", UnitName), nil
	case "windows":
		return c.runKey(), nil
	}
	return "", ErrUnsupported
}

// absolute reports whether p is an absolute path on c's OS (a Windows
// path is judged by its form, wherever this runs).
func (c Config) absolute(p string) bool {
	if c.GOOS == "windows" {
		return len(p) > 2 && (p[1] == ':' && p[2] == '\\' || strings.HasPrefix(p, `\\`))
	}
	return filepath.IsAbs(p)
}

// Render returns the service file's text.
func (c Config) Render() ([]byte, error) {
	if !c.absolute(c.Bin) {
		return nil, fmt.Errorf("tm path %q is not absolute", c.Bin)
	}
	switch c.GOOS {
	case "darwin":
		return c.plist(true), nil
	case "linux":
		return c.unit(), nil
	case "windows":
		cmd, err := c.runCommand()
		return []byte(cmd + "\n"), err
	}
	return nil, ErrUnsupported
}

// plist renders the launchd job: atLoad for the login service, which
// starts the server when it is loaded; the on-demand job only starts on
// kickstart. Neither has KeepAlive: tm server stop must leave it stopped.
func (c Config) plist(atLoad bool) []byte {
	x := html.EscapeString
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + c.JobLabel() + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + x(c.Program()) + `</string>
		<string>server</string>
		<string>run</string>
		<string>--launchd</string>
`)
	if c.RunDir != "" {
		b.WriteString("\t\t<string>--launch-file</string>\n\t\t<string>" + x(c.LaunchFile()) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<` + fmt.Sprint(atLoad) + `/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>WorkingDirectory</key>
	<string>/</string>
	<key>Umask</key>
	<integer>63</integer>
`)
	env := c.env()
	if len(env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, kv := range env {
			b.WriteString("\t\t<key>" + x(kv[0]) + "</key>\n\t\t<string>" + x(kv[1]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	if c.LogDir != "" {
		log := x(filepath.Join(c.LogDir, "service.log"))
		b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + log + "</string>\n")
		b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + log + "</string>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func (c Config) unit() []byte {
	var b bytes.Buffer
	b.WriteString(`[Unit]
Description=terminatr server (agent sessions)
Documentation=https://github.com/theclifmeister/terminatr

[Service]
Type=simple
ExecStart=` + systemdQuote(c.Bin) + ` server run
`)
	for _, kv := range c.env() {
		b.WriteString("Environment=" + systemdQuote(kv[0]+"="+kv[1]) + "\n")
	}
	// The server stops its sessions itself (SIGHUP, then SIGKILL after
	// 5 s) and records them for resume: send SIGTERM to it alone first.
	b.WriteString(`KillMode=mixed
TimeoutStopSec=20
Restart=no

[Install]
WantedBy=default.target
`)
	return b.Bytes()
}

func (c Config) env() [][2]string {
	var out [][2]string
	if c.Path != "" {
		out = append(out, [2]string{"PATH", c.Path})
	}
	if c.Home != "" {
		out = append(out, [2]string{"TERMINATR_HOME", c.Home})
	}
	if c.Socket != "" && c.GOOS == "darwin" {
		out = append(out, [2]string{"TERMINATR_SOCKET", c.Socket})
	}
	return out
}

// systemdQuote quotes s for an ExecStart= or Environment= line.
func systemdQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$%;") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

func (c Config) run(name string, args ...string) error {
	if c.Run != nil {
		return c.Run(name, args...)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Install writes the service file and loads it, which also starts the
// server unless one runs already (that start then exits at once with
// "already running", which is fine). It returns the file's path.
func (c Config) Install() (string, error) {
	path, err := c.File()
	if err != nil {
		return "", err
	}
	data, err := c.Render()
	if err != nil {
		return "", err
	}
	if c.GOOS == "windows" {
		return path, c.installRunKey()
	}
	if c.LogDir != "" {
		if err := os.MkdirAll(c.LogDir, 0o700); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	switch c.GOOS {
	case "darwin":
		if err := c.writeLaunchFile(); err != nil {
			return "", err
		}
		// bootstrap refuses an already loaded label: unload a previous
		// install, or the on-demand job, first (an error just means it
		// wasn't loaded).
		c.run("launchctl", "bootout", c.target())
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", err
		}
		return path, c.bootstrap(path)
	default:
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", err
		}
		if err := c.run("systemctl", "--user", "daemon-reload"); err != nil {
			return path, err
		}
		return path, c.run("systemctl", "--user", "enable", "--now", UnitName)
	}
}

// Uninstall unloads and removes the service file. Unloading stops a
// server the service manager started, cleanly: its agents are resumed by
// the next server. Uninstalling what isn't installed is fine.
func (c Config) Uninstall() (string, error) {
	path, err := c.File()
	if err != nil {
		return "", err
	}
	if c.GOOS == "windows" {
		return path, c.uninstallRunKey()
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	switch c.GOOS {
	case "darwin":
		c.run("launchctl", "bootout", c.target())
		return path, removeFile(path)
	default:
		c.run("systemctl", "--user", "disable", "--now", UnitName)
		if err := removeFile(path); err != nil {
			return path, err
		}
		return path, c.run("systemctl", "--user", "daemon-reload")
	}
}

func (c Config) domain() string { return fmt.Sprintf("gui/%d", c.UID) }

func (c Config) target() string { return c.domain() + "/" + c.JobLabel() }

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
