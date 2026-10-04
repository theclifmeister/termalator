package main

import "strings"

// options are the command-line flags, parsed the way Claude takes them.
type options struct {
	version    bool
	pluginDirs []string
	settings   []string
	sessionID  string
	resume     *string // nil unless --resume was given; "" opens the picker
	briefPath  string
	yolo       bool
	model      string
	prompt     string
}

// valueFlags are the known flags that take a value.
var valueFlags = map[string]bool{
	"--plugin-dir":                true,
	"--settings":                  true,
	"--session-id":                true,
	"--resume":                    true,
	"-r":                          true,
	"--append-system-prompt-file": true,
	"--model":                     true,
}

// parseArgs reads argv (without the program name). Unknown flags are
// ignored and never swallow a following positional; empty positionals
// (a template that rendered nothing) are skipped.
func parseArgs(args []string) options {
	var o options
	var prompt []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			for _, p := range args[i+1:] {
				if p != "" {
					prompt = append(prompt, p)
				}
			}
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if arg != "" {
				prompt = append(prompt, arg)
			}
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if !hasValue && valueFlags[name] {
			switch {
			case i+1 < len(args) && (name == "--resume" || name == "-r") && strings.HasPrefix(args[i+1], "-"):
				// --resume with no id: the interactive picker.
			case i+1 < len(args):
				i++
				value = args[i]
			}
		}
		switch name {
		case "--version", "-v":
			o.version = true
		case "--plugin-dir":
			if value != "" {
				o.pluginDirs = append(o.pluginDirs, value)
			}
		case "--settings":
			if value != "" {
				o.settings = append(o.settings, value)
			}
		case "--session-id":
			o.sessionID = value
		case "--resume", "-r":
			v := value
			o.resume = &v
		case "--append-system-prompt-file":
			o.briefPath = value
		case "--dangerously-skip-permissions":
			o.yolo = true
		case "--model":
			o.model = value
		}
	}
	o.prompt = strings.Join(prompt, " ")
	return o
}
