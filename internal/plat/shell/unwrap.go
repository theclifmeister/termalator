package shell

import (
	"strings"
)

// Unwrap strips interpreters and shells from a process's argv, so an
// agent started as `node /usr/lib/node_modules/.bin/claude`,
// `sh -c "claude --resume x"`, `cmd /d /c claude.exe` or
// `pwsh -NoProfile -Command "& claude"` is seen as `claude …`
// (docs/SPEC.md §8.2). Program names match case-insensitively, with
// either path separator and without .exe, and the result's program loses
// a .exe suffix.
func Unwrap(argv []string) []string {
	for i := 0; i < 6 && len(argv) > 0; i++ {
		var next []string
		switch name := progName(argv[0]); name {
		case "node", "bun", "deno":
			next = skipFlags(argv[1:])
			if len(next) > 0 && next[0] == "run" && name == "deno" {
				next = next[1:]
			}
		case "sh", "bash", "zsh", "dash":
			if len(argv) < 3 || argv[1] != "-c" {
				return trimExe(argv)
			}
			next = Split(argv[2])
			if len(next) > 0 && next[0] == "exec" {
				next = next[1:]
			}
		case "cmd":
			next = unwrapCmd(argv[1:])
		case "pwsh", "powershell":
			next = unwrapPowerShell(name, argv[1:])
		default:
			return trimExe(argv)
		}
		if len(next) == 0 {
			return trimExe(argv)
		}
		argv = next
	}
	return trimExe(argv)
}

// progName is argv[0]'s base name, lower case, without .exe.
func progName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	p = strings.ToLower(p)
	return strings.TrimSuffix(p, ".exe")
}

// trimExe drops a .exe suffix (any case) from argv[0].
func trimExe(argv []string) []string {
	if len(argv) > 0 && len(argv[0]) > 4 && strings.EqualFold(argv[0][len(argv[0])-4:], ".exe") {
		argv = append([]string{argv[0][:len(argv[0])-4]}, argv[1:]...)
	}
	return argv
}

func skipFlags(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		args = args[1:]
	}
	return args
}

// unwrapCmd takes cmd.exe's arguments: switches, then /c (or /k) and the
// command line, whose outer quotes cmd strips. It returns nil when there
// is no command.
func unwrapCmd(args []string) []string {
	for i, a := range args {
		if !strings.HasPrefix(a, "/") {
			return nil
		}
		switch strings.ToLower(a) {
		case "/c", "/k":
			line := strings.Join(args[i+1:], " ")
			if len(line) >= 2 && line[0] == '"' && line[len(line)-1] == '"' {
				line = line[1 : len(line)-1]
			}
			return nilIfEmpty(Split(line))
		}
	}
	return nil
}

// psValued are PowerShell parameters that take a value.
var psValued = map[string]bool{
	"-executionpolicy": true, "-ex": true, "-ep": true,
	"-workingdirectory": true, "-wd": true,
	"-windowstyle": true, "-w": true,
	"-outputformat": true, "-o": true, "-of": true,
	"-inputformat": true, "-i": true, "-if": true,
	"-configurationname": true, "-settingsfile": true, "-custompipename": true,
}

// unwrapPowerShell takes pwsh's or powershell's arguments: parameters,
// then -Command (the rest is a command line, & dropped) or -File (a script
// and its arguments). A bare first argument is a file for pwsh and a
// command for Windows PowerShell. It returns nil when there is neither.
func unwrapPowerShell(name string, args []string) []string {
	for i := 0; i < len(args); i++ {
		a := strings.ToLower(args[i])
		switch {
		case a == "-command" || a == "-c":
			return psCommand(args[i+1:])
		case a == "-file" || a == "-f":
			return nilIfEmpty(args[i+1:])
		case psValued[a]:
			i++
		case strings.HasPrefix(a, "-"):
		case name == "pwsh":
			return args[i:]
		default:
			return psCommand(args[i:])
		}
	}
	return nil
}

func psCommand(args []string) []string {
	argv := commandLine(args)
	if len(argv) > 0 && argv[0] == "&" {
		argv = argv[1:]
	}
	return nilIfEmpty(argv)
}

// commandLine is a shell's command-line arguments as one argv: the shell
// joins them with spaces and parses the result.
func commandLine(args []string) []string {
	return nilIfEmpty(Split(strings.Join(args, " ")))
}

func nilIfEmpty(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	return argv
}

// Split splits a command line into words at unquoted white space,
// removing '…' and "…" quotes. It is the common subset of the shells'
// syntax, enough to find a program and its arguments, not a parser.
func Split(s string) []string {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
