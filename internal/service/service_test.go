package service

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct{ calls []string }

func (f *fakeRunner) run(name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return nil
}

func config(t *testing.T, goos string) (Config, *fakeRunner) {
	f := &fakeRunner{}
	h := t.TempDir()
	return Config{
		GOOS: goos, UID: 501, UserHome: h, Bin: "/opt/tm dir/bin/tm",
		Home: "/data/tm & co", LogDir: filepath.Join(h, ".terminatr", "logs"),
		Path: "/usr/bin:/bin", Run: f.run,
	}, f
}

func TestPlist(t *testing.T) {
	c, _ := config(t, "darwin")
	data, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	// Well-formed XML, with the program and the escaped values.
	d := xml.NewDecoder(strings.NewReader(string(data)))
	d.Strict = false
	for {
		if _, err := d.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("plist is not XML: %v\n%s", err, data)
			}
			break
		}
	}
	for _, want := range []string{
		"<string>dev.terminatr.server</string>",
		"<string>/opt/tm dir/bin/tm</string>\n\t\t<string>server</string>\n\t\t<string>run</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<false/>",
		"<key>TERMINATR_HOME</key>\n\t\t<string>/data/tm &amp; co</string>",
		"<key>PATH</key>\n\t\t<string>/usr/bin:/bin</string>",
		"logs/service.log</string>",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("plist lacks %q:\n%s", want, data)
		}
	}
	if p, _ := c.File(); !strings.HasSuffix(p, "Library/LaunchAgents/dev.terminatr.server.plist") {
		t.Errorf("file %s", p)
	}
}

func TestUnit(t *testing.T) {
	c, _ := config(t, "linux")
	data, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ExecStart="/opt/tm dir/bin/tm" server run`,
		`Environment="TERMINATR_HOME=/data/tm & co"`,
		"Environment=PATH=/usr/bin:/bin",
		"KillMode=mixed", "WantedBy=default.target",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("unit lacks %q:\n%s", want, data)
		}
	}
	if p, _ := c.File(); !strings.HasSuffix(p, ".config/systemd/user/terminatr.service") {
		t.Errorf("file %s", p)
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/tm": "/usr/bin/tm",
		"/a b/tm":     `"/a b/tm"`,
		`a"b$c%d\e`:   `"a\"b$$c%%d\\e"`,
	} {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestInstallUninstall(t *testing.T) {
	for goos, want := range map[string][2][]string{
		"darwin": {
			{"launchctl bootout gui/501/dev.terminatr.server", "launchctl bootstrap gui/501 "},
			{"launchctl bootout gui/501/dev.terminatr.server"},
		},
		"linux": {
			{"systemctl --user daemon-reload", "systemctl --user enable --now terminatr.service"},
			{"systemctl --user disable --now terminatr.service", "systemctl --user daemon-reload"},
		},
	} {
		c, f := config(t, goos)
		c.Bin = "/usr/local/bin/tm"
		path, err := c.Install()
		if err != nil {
			t.Fatal(goos, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: not written: %v", goos, err)
		}
		if _, err := os.Stat(c.LogDir); err != nil {
			t.Errorf("%s: no log dir", goos)
		}
		check := func(calls, want []string) {
			t.Helper()
			if len(calls) != len(want) {
				t.Fatalf("%s: calls %q, want %q", goos, calls, want)
			}
			for i := range want {
				if !strings.HasPrefix(calls[i], want[i]) {
					t.Errorf("%s: call %d = %q, want %q", goos, i, calls[i], want[i])
				}
			}
		}
		check(f.calls, want[0])
		f.calls = nil
		if _, err := c.Uninstall(); err != nil {
			t.Fatal(goos, err)
		}
		check(f.calls, want[1])
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: file left", goos)
		}
		// Uninstalling again is fine and calls nothing.
		f.calls = nil
		if _, err := c.Uninstall(); err != nil || len(f.calls) != 0 {
			t.Errorf("%s: second uninstall: %v %q", goos, err, f.calls)
		}
	}
}

func TestUnsupported(t *testing.T) {
	c, _ := config(t, "windows")
	if _, err := c.Install(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("windows: %v", err)
	}
	c, _ = config(t, "linux")
	c.Bin = "tm"
	if _, err := c.Render(); err == nil {
		t.Fatal("a relative tm path was accepted")
	}
}
