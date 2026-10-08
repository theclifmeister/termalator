// Package shell is what terminatr knows about command shells: the user's
// default interactive shell, quoting for each kind, the hook command line
// a kind of shell runs, and unwrapping shells and interpreters from a
// process's argv to find the program they run.
//
// Quote, Command and Unwrap are pure: they behave the same on every OS, so
// their Windows tables are tested on macOS and Linux too.
package shell

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// Kind is a family of shell syntax.
type Kind string

const (
	Sh         Kind = "sh"
	Bash       Kind = "bash"
	Zsh        Kind = "zsh"
	Pwsh       Kind = "pwsh"       // PowerShell 7
	PowerShell Kind = "powershell" // Windows PowerShell 5.1
	Cmd        Kind = "cmd"
)

// Kinds are every kind, in a stable order.
var Kinds = []Kind{Sh, Bash, Zsh, Pwsh, PowerShell, Cmd}

func (k Kind) powershell() bool { return k == Pwsh || k == PowerShell }

// Interactive is the argv of the user's interactive shell, which a
// session runs when it is given no command: $SHELL -l, else /bin/sh -l;
// on Windows pwsh, else Windows PowerShell, else %COMSPEC%.
func Interactive() []string {
	return interactive(runtime.GOOS, os.Getenv, exec.LookPath)
}

func interactive(goos string, getenv func(string) string, lookPath func(string) (string, error)) []string {
	if goos != "windows" {
		sh := getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		return []string{sh, "-l"}
	}
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := lookPath(name); err == nil {
			return []string{p, "-NoLogo"}
		}
	}
	if c := getenv("COMSPEC"); c != "" {
		return []string{c}
	}
	return []string{"cmd.exe"}
}

// Quote makes s one word for a k shell, always quoted: POSIX '…' (a quote
// ends the quoting, is escaped and reopens it), PowerShell '…' (a quote,
// typographic ones included, is doubled), cmd "…" (a double quote is
// doubled). cmd has no escape for %
// inside quotes, so %NAME% still expands there.
func Quote(k Kind, s string) string {
	switch {
	case k.powershell():
		var b strings.Builder
		b.WriteByte('\'')
		for _, r := range s {
			if r == '\'' || r == '‘' || r == '’' || r == '‚' || r == '‛' {
				b.WriteRune(r)
			}
			b.WriteRune(r)
		}
		b.WriteByte('\'')
		return b.String()
	case k == Cmd:
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	default:
		return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
	}
}

// bare matches words every kind reads literally, unquoted (a ~ only
// expands at the start of a word).
var bare = regexp.MustCompile(`^[A-Za-z0-9_./:-][A-Za-z0-9_./:~-]*$`)

// envRef matches an exe given as "$NAME": the program named by an
// environment variable.
var envRef = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)

func word(k Kind, s string) string {
	if bare.MatchString(s) {
		return s
	}
	return Quote(k, s)
}

// Command is the command line that runs exe with args in a k shell on
// goos, as an agent's hook configuration holds it. Words that need no
// quoting stay bare.
//
// On Windows the exe takes forward slashes, which Git Bash, cmd, pwsh and
// Windows PowerShell all run unquoted (herdr t-0012). A folder with a
// space needs quoting; pass ShortPath(exe) to get its 8.3 form instead.
// A quoted exe in PowerShell needs the call operator: & 'C:/…/tm.exe'.
//
// An exe of the form "$NAME" is the program an environment variable
// names: "$NAME" in POSIX shells, & $env:NAME in PowerShell, "%NAME%" in
// cmd.
func Command(k Kind, goos, exe string, args ...string) string {
	var b strings.Builder
	if m := envRef.FindStringSubmatch(exe); m != nil {
		switch {
		case k.powershell():
			b.WriteString("& $env:" + m[1])
		case k == Cmd:
			b.WriteString(`"%` + m[1] + `%"`)
		default:
			b.WriteString(`"$` + m[1] + `"`)
		}
	} else {
		if goos == "windows" {
			exe = strings.ReplaceAll(exe, `\`, "/")
		}
		switch {
		case bare.MatchString(exe):
			b.WriteString(exe)
		case k.powershell():
			b.WriteString("& " + Quote(k, exe))
		default:
			b.WriteString(Quote(k, exe))
		}
	}
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(word(k, a))
	}
	return b.String()
}
