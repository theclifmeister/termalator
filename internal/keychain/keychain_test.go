package keychain

import (
	"errors"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestOverSSH(t *testing.T) {
	for _, v := range []string{"SSH_CONNECTION", "SSH_TTY", "SSH_CLIENT"} {
		if !OverSSH(env(map[string]string{v: "x"})) {
			t.Errorf("%s set: not over SSH", v)
		}
	}
	if OverSSH(env(nil)) {
		t.Error("no SSH variable: over SSH")
	}
}

func TestStartWarning(t *testing.T) {
	ssh := env(map[string]string{"SSH_CONNECTION": "10.0.0.2 51000 10.0.0.1 22"})
	if w := StartWarning("darwin", ssh); !strings.Contains(w, "keychain") || !strings.Contains(w, "without launchd") || !strings.Contains(w, "logged in at the Mac") {
		t.Errorf("darwin over SSH: %q", w)
	}
	if w := StartWarning("darwin", env(nil)); w != "" {
		t.Errorf("darwin locally: %q", w)
	}
	if w := StartWarning("linux", ssh); w != "" {
		t.Errorf("linux over SSH: %q", w)
	}
}

// fake answers launchctl managername with session and security with
// secErr (nil: success), and records the calls.
func fake(session string, secErr error, calls *[]string) Runner {
	return func(name string, args ...string) (string, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		switch name {
		case "launchctl":
			return session + "\n", nil
		case "security":
			if secErr != nil {
				return "security: SecKeychainCopySettings login.keychain: " + secErr.Error(), secErr
			}
			return `Keychain "login.keychain" no-timeout`, nil
		}
		return "", errors.New("unexpected " + name)
	}
}

func TestProbe(t *testing.T) {
	var calls []string
	st := Probe("darwin", env(nil), fake("Aqua", nil, &calls))
	if !st.Checked || !st.OK || st.Session != "Aqua" {
		t.Errorf("Aqua: %+v", st)
	}
	// Never anything that reads a secret.
	for _, c := range calls {
		if strings.Contains(c, "find-") || strings.Contains(c, "dump") {
			t.Errorf("probe ran %q", c)
		}
	}

	st = Probe("darwin", env(map[string]string{"SSH_TTY": "/dev/ttys001"}), fake("Background", nil, &calls))
	if !st.Checked || st.OK || !st.OverSSH || !strings.Contains(st.Detail, "Background") || !strings.Contains(st.Detail, "over SSH") {
		t.Errorf("SSH login: %+v", st)
	}

	st = Probe("darwin", env(nil), fake("Aqua", errors.New("Interaction with the Security Server is not allowed."), &calls))
	if st.OK || !strings.Contains(st.Detail, "Security Server") {
		t.Errorf("security refused: %+v", st)
	}

	calls = nil
	st = Probe("linux", env(map[string]string{"SSH_TTY": "x"}), fake("Aqua", nil, &calls))
	if st.Checked || st.OK || len(calls) != 0 {
		t.Errorf("linux: %+v, ran %v", st, calls)
	}
}
