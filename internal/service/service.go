// Package service writes and loads the optional start-at-login service
// for the server (docs/SPEC.md §3.1): a launchd agent on macOS, a systemd
// user unit on Linux. Both run `tm server run` in the foreground. Nothing
// depends on it, because any tm command starts the server on demand.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Names of the service.
const (
	Label    = "dev.termilator.server"
	UnitName = "termilator.service"
)

// Config is what a service file is rendered from.
type Config struct {
	GOOS string
	UID  int
	// UserHome is the user's home directory (~).
	UserHome string
	// Bin is the absolute path of tm.
	Bin string
	// Home is TERMILATOR_HOME when set explicitly, else "".
	Home string
	// LogDir is where launchd writes the server's stdout and stderr.
	LogDir string
	// Path is the PATH the server and its agents get: services start
	// with a minimal PATH that usually lacks claude and git.
	Path string
	// Run runs a service manager command; nil uses the real one.
	Run func(name string, args ...string) error
}

// ErrUnsupported means there is no service manager support for the OS.
var ErrUnsupported = errors.New("start at login is supported on macOS (launchd) and Linux (systemd --user) only")

// File is the service file's path.
func (c Config) File() (string, error) {
	switch c.GOOS {
	case "darwin":
		return filepath.Join(c.UserHome, "Library", "LaunchAgents", Label+".plist"), nil
	case "linux":
		return filepath.Join(c.UserHome, ".config", "systemd", "user", UnitName), nil
	}
	return "", ErrUnsupported
}

// Render returns the service file's text.
func (c Config) Render() ([]byte, error) {
	if !filepath.IsAbs(c.Bin) {
		return nil, fmt.Errorf("tm path %q is not absolute", c.Bin)
	}
	switch c.GOOS {
	case "darwin":
		return c.plist(), nil
	case "linux":
		return c.unit(), nil
	}
	return nil, ErrUnsupported
}

func (c Config) plist() []byte {
	x := html.EscapeString
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + x(c.Bin) + `</string>
		<string>server</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
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
Description=termilator server (agent sessions)
Documentation=https://github.com/theclifmeister/termilator

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
		out = append(out, [2]string{"TERMILATOR_HOME", c.Home})
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
		// bootstrap refuses an already loaded label: unload a previous
		// install first (an error just means it wasn't loaded).
		c.run("launchctl", "bootout", c.domain()+"/"+Label)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", err
		}
		return path, c.run("launchctl", "bootstrap", c.domain(), path)
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
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	switch c.GOOS {
	case "darwin":
		c.run("launchctl", "bootout", c.domain()+"/"+Label)
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

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
