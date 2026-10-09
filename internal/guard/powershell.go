package guard

import (
	"regexp"
	"slices"
	"strings"
)

// PowerShell, as Claude's PowerShell tool runs it on Windows (a shell
// tool with Syntax "powershell"). Its commands are split and read as
// Bash's are, with PowerShell's quoting, and judged by the same rules:
// git, gh, az and the HTTP clients as in Bash, plus Remove-Item and its
// aliases (delete-branch), Get-Content, Copy-Item and the like on a
// secret, the Env: drive and $env:AZURE_DEVOPS_EXT_PAT (credentials),
// Invoke-WebRequest and Invoke-RestMethod to Azure DevOps (as curl), and
// a command line handed to pwsh -Command, cmd /c or bash -c.
//
// Windows has no sandbox behind the guard, so what it can't read is put
// to the human (or auto mode's classifier) rather than let through: a
// command named by a variable or an expression (& $x, & (…)), a string
// evaluated (Invoke-Expression, Invoke-Command, Start-Process, Start-Job,
// Add-Type, Set-Alias, pwsh -EncodedCommand) and the .NET methods that
// run, read or delete ([Diagnostics.Process]::Start(…)).

// judgePowerShell checks each command of a PowerShell command line. A
// refusal wins over an ask.
func (r Rules) judgePowerShell(line string) *Denial {
	var ask *Denial
	if m := psMethod.FindStringSubmatch(line); m != nil {
		ask = r.ask("the ." + m[1] + "() method")
	}
	for _, words := range PSCommands(line) {
		d := r.judgePSCommand(words)
		if d != nil && !d.Ask {
			return d
		}
		if ask == nil {
			ask = d
		}
	}
	return ask
}

// ask is the question for a command the guard can't read, what makes
// it so in why: the guard's own words or a name it knows, never the
// call's text.
func (r Rules) ask(why string) *Denial {
	return &Denial{Rule: "unknown", Ask: true, Summary: why,
		Message: "terminatr guard: it can't tell what this PowerShell command runs (" + why + "), so it asks first."}
}

var (
	// psMethod: a .NET method that runs, evaluates, reads or deletes.
	psMethod = regexp.MustCompile(`(?i)(?:::|\.)\s*(Start|Invoke|InvokeScript|AddScript|AddCommand|Create|Run|Exec|ShellExecute|GetEnvironmentVariables?|ReadAllText|ReadAllBytes|ReadAllLines|ReadLines|OpenRead|OpenText|Delete|Move|Copy)\s*\(`)
	// psEnvDrive: a path on the Env: drive, the name after it.
	psEnvDrive = regexp.MustCompile(`(?is)^env:[\\/]?(.*)$`)
	// psAssignOp: the operator of an assignment, and what follows it in
	// the same word.
	psAssignOp = regexp.MustCompile(`(?s)^(?:[-+*/%]|\?\?)?=(.*)$`)
	// psVariable: a variable, perhaps cast ([string]$x, $env:X, ${x}).
	psVariable = regexp.MustCompile(`^(?:\[[^\]]*\])*\$[^=\s]+$`)
	// psAssignment: a variable and its assignment in one word ($x=git).
	psAssignment = regexp.MustCompile(`(?s)^(?:\[[^\]]*\])*\$[^=\s]*?(?:[-+*/%]|\?\?)?=(.*)$`)
	psWildcard   = regexp.MustCompile(`[*?[]`)
)

// The cmdlets and aliases the guard knows, by lowercase name.
var (
	psRemove = setOf("remove-item rm ri del erase rd rmdir")
	// psEval run what they are given as code, or a program with what
	// they are given: asked.
	psEval    = setOf("invoke-expression iex invoke-command icm start-process saps start start-job sajb start-threadjob add-type set-alias sal new-alias nal")
	psReaders = setOf("get-content gc type copy-item copy cpi move-item move mi select-string sls format-hex fhx import-clixml")
	psPrinter = setOf("write-output write write-host write-information out-host out-string set-clipboard scb")
	// psLister list a drive's items, the Env: drive's being variables.
	psLister = setOf("get-childitem gci ls dir get-item gi get-itemproperty gp")
	psHTTP   = setOf("invoke-webrequest iwr invoke-restmethod irm")
)

func setOf(names string) map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(names) {
		m[n] = true
	}
	return m
}

// judgePSCommand judges one simple command of a PowerShell line.
func (r Rules) judgePSCommand(words []string) *Denial {
	argv, assigned := psStrip(words)
	if len(argv) == 0 {
		return nil
	}
	if op := argv[0]; op == "&" || op == "." {
		if len(argv) == 1 {
			return r.ask(op + " of an expression")
		}
		argv = argv[1:]
		if strings.HasPrefix(argv[0], "$") {
			return r.ask(op + " of a variable")
		}
	} else if strings.HasPrefix(argv[0], "$") {
		// An expression: printed unless assigned.
		if v := patVar(argv[0]); v != "" && !assigned && r.has("credentials") {
			return r.deny("credentials", "output of "+safe(v), "")
		}
		return nil
	}
	cmd := psName(argv[0])
	args := argv[1:]
	var d *Denial
	switch {
	case psEval[cmd]:
		return r.ask(cmd)
	case cmd == "pwsh" || cmd == "powershell":
		return r.judgePwsh(args)
	case cmd == "cmd":
		if i := slices.IndexFunc(args, func(a string) bool {
			a = strings.ToLower(a)
			return a == "/c" || a == "/k" || a == "/r"
		}); i >= 0 {
			return r.judgeBash(strings.Join(args[i+1:], " "))
		}
		return nil
	case cmd == "bash" || cmd == "sh":
		if i := slices.IndexFunc(args, shellC.MatchString); i >= 0 && i+1 < len(args) {
			return r.judgeBash(args[i+1])
		}
		return nil
	case cmd == "git":
		d = r.judgeGit(args)
	case cmd == "gh":
		d = r.judgeGh(args)
	case cmd == "az":
		d = r.judgeAz(args)
	case httpClients[cmd] || psHTTP[cmd]:
		// In Windows PowerShell curl and wget are Invoke-WebRequest.
		if httpClients[cmd] {
			d = r.judgeHTTP(args)
		}
		if d == nil && cmd != "http" && cmd != "https" && cmd != "xh" {
			d = r.judgeWebRequest(args)
		}
	case psRemove[cmd] && r.has("delete-branch") && slices.ContainsFunc(args, psRecursive):
		for _, p := range psPaths(args) {
			if r.isWorktreeRoot(r.abs(p)) {
				d = r.deny("delete-branch", "Remove-Item -Recurse of "+safe(r.abs(p)), "")
				break
			}
		}
	}
	if d != nil {
		return d
	}
	if r.has("credentials") {
		if d := r.judgePSSecret(cmd, args); d != nil {
			return d
		}
		// The arguments as paths: -Path:x's x too.
		paths := psPaths(args)
		return r.judgeSecret(cmd, paths, append([]string{argv[0]}, paths...), readers[cmd] || psReaders[cmd], printers[cmd] || psPrinter[cmd])
	}
	return nil
}

var shellC = regexp.MustCompile(`^-[a-zA-Z]*c[a-zA-Z]*$`)

// judgePwsh: pwsh or powershell given a command line (-Command, or the
// first argument), judged as one; an encoded one is asked; a script
// (-File) is a script.
func (r Rules) judgePwsh(args []string) *Denial {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "/") {
			return r.judgePowerShell(strings.Join(args[i:], " "))
		}
		switch {
		case psParam(a, "encodedcommand", 1), strings.EqualFold(a, "-ec"):
			return r.ask("an encoded command")
		case psParam(a, "command", 1):
			return r.judgePowerShell(strings.Join(args[i+1:], " "))
		case psParam(a, "file", 1):
			return nil
		case slices.ContainsFunc(pwshValued, func(n string) bool { return psParam(a, n, 2) }),
			slices.ContainsFunc([]string{"-ep", "-wd", "-of", "-if", "-v"}, func(n string) bool { return strings.EqualFold(a, n) }):
			i++
		}
	}
	return nil
}

// pwshValued are pwsh's options that take a value.
var pwshValued = []string{"executionpolicy", "windowstyle", "workingdirectory", "outputformat", "inputformat",
	"configurationname", "configurationfile", "settingsfile", "version", "psconsolefile", "custompipename"}

// judgeWebRequest: Invoke-WebRequest or Invoke-RestMethod to Azure
// DevOps, judged as curl is: by what it authenticates with, then by
// method and route.
func (r Rules) judgeWebRequest(args []string) *Denial {
	i := slices.IndexFunc(args, azureHost.MatchString)
	if i < 0 {
		return nil
	}
	url := args[i]
	if r.has("credentials") && slices.ContainsFunc(args, func(a string) bool {
		return psParam(a, "headers", 1) || psParam(a, "credential", 2) || psParam(a, "token", 1) ||
			psParam(a, "authentication", 2) || psParam(a, "usedefaultcredentials", 2) || httpAuth.MatchString(a)
	}) {
		return r.deny("credentials", "an HTTP call to Azure DevOps with credentials", "")
	}
	method := ""
	for j, a := range args {
		if psParam(a, "method", 2) || psParam(a, "custommethod", 2) {
			if v, ok := psValue(a); ok {
				method = v
			} else {
				method = at(args, j+1)
			}
		}
	}
	if method == "" {
		method = "GET"
	}
	return r.judgeRoute(method, url)
}

// judgePSSecret: the Env: drive listed or read, all of it or a variable
// that looks secret.
func (r Rules) judgePSSecret(cmd string, args []string) *Denial {
	if !psLister[cmd] && !psReaders[cmd] && !readers[cmd] {
		return nil
	}
	for _, p := range psPaths(args) {
		m := psEnvDrive.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		if m[1] == "" || psWildcard.MatchString(m[1]) {
			return r.deny("credentials", safe(cmd)+" printing the environment", "")
		}
		if secretName.MatchString(m[1]) {
			return r.deny("credentials", safe(cmd)+" of "+safe(m[1]), "")
		}
	}
	return nil
}

// psRecursive: a's an option of Remove-Item's that makes it recursive
// (-Recurse, -r; rm.exe's -rf).
func psRecursive(a string) bool {
	if psParam(a, "recurse", 1) {
		v, ok := psValue(a)
		return !ok || !strings.EqualFold(v, "$false")
	}
	return rmRecursive.MatchString(a) || a == "--recursive"
}

// psPaths are the paths among a cmdlet's arguments: those not options,
// the values of -Name:value options, each of a comma list.
func psPaths(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			v, ok := psValue(a)
			if !ok {
				continue
			}
			a = v
		}
		for _, p := range strings.Split(a, ",") {
			if p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// psParam: a is the option -name, or the start of it at least min
// letters long, case aside, with or without a :value.
func psParam(a, name string, min int) bool {
	if !strings.HasPrefix(a, "-") {
		return false
	}
	a = strings.ToLower(a[1:])
	if i := strings.Index(a, ":"); i >= 0 {
		a = a[:i]
	}
	return len(a) >= min && strings.HasPrefix(name, a)
}

// psValue is the value of an option given as -name:value.
func psValue(a string) (string, bool) {
	if i := strings.Index(a, ":"); i >= 0 && strings.HasPrefix(a, "-") {
		return a[i+1:], true
	}
	return "", false
}

// psName is a command's name as the guard knows it: its file name,
// lowercase, without .exe (C:\Program Files\Git\cmd\git.exe is git).
func psName(w string) string {
	w = strings.ToLower(w[strings.LastIndexAny(w, `/\`)+1:])
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
		if strings.HasSuffix(w, ext) && len(w) > len(ext) {
			return strings.TrimSuffix(w, ext)
		}
	}
	return w
}

// psStrip drops the assignments a command's output goes to ($x = git
// …), and says whether there were any.
func psStrip(words []string) ([]string, bool) {
	assigned := false
	for len(words) > 0 {
		switch {
		case len(words) > 1 && psVariable.MatchString(words[0]) && psAssignOp.MatchString(words[1]):
			rest := psAssignOp.FindStringSubmatch(words[1])[1]
			words = words[2:]
			if rest != "" {
				words = append([]string{rest}, words...)
			}
		case psAssignment.MatchString(words[0]):
			rest := psAssignment.FindStringSubmatch(words[0])[1]
			words = words[1:]
			if rest != "" {
				words = append([]string{rest}, words...)
			}
		default:
			return words, assigned
		}
		assigned = true
	}
	return words, assigned
}

// isSQuote and isDQuote: PowerShell takes typographic quotes as quotes.
func isSQuote(c rune) bool { return c == '\'' || c == '‘' || c == '’' || c == '‚' || c == '‛' }
func isDQuote(c rune) bool { return c == '"' || c == '“' || c == '”' || c == '„' }

// PSCommands splits a PowerShell command line into simple commands, each
// a list of words with quotes and escapes (`) removed: on ; | && ||
// newlines, ( ), $( ), @( ), @{ }, { } and a closing &, the inside of a
// $( ) in double quotes and here-strings read as commands too. & or .
// calling a command stays its first word ("&"); one calling a script
// block is dropped, the block's commands read in its place. Comments
// are skipped; redirections stay words (">"), their stream number
// dropped.
func PSCommands(line string) [][]string {
	var out [][]string
	var words []string
	var word strings.Builder
	inWord := false
	endWord := func() {
		if inWord {
			words = append(words, word.String())
		}
		word.Reset()
		inWord = false
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			out = append(out, words)
		}
		words = nil
	}
	s := []rune(line)
	// expandable is the inside of a double-quoted string from i, to its
	// end (closing(i) > 0 at the end: one past it, or the line's end),
	// its $( ) read as commands. In a plain string "" is a quote.
	expandable := func(i int, closing func(int) int, doubled bool) (string, int) {
		var q strings.Builder
		for i < len(s) {
			if doubled && isDQuote(s[i]) && i+1 < len(s) && isDQuote(s[i+1]) {
				q.WriteRune(s[i])
				i += 2
				continue
			}
			if n := closing(i); n > 0 {
				return q.String(), i + n
			}
			switch {
			case s[i] == '`' && i+1 < len(s):
				q.WriteRune(s[i+1])
				i += 2
			case s[i] == '$' && i+1 < len(s) && s[i+1] == '(':
				end := psClose(s, i+2)
				out = append(out, PSCommands(string(s[i+2:end]))...)
				q.WriteString(string(s[i:min(end+1, len(s))]))
				i = end + 1
			default:
				q.WriteRune(s[i])
				i++
			}
		}
		return q.String(), i
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '`':
			if i+1 < len(s) {
				if s[i+1] == '\r' && i+2 < len(s) && s[i+2] == '\n' {
					i++
				} else if s[i+1] != '\n' {
					word.WriteRune(s[i+1])
					inWord = true
				}
				i++
			}
		case c == '<' && i+1 < len(s) && s[i+1] == '#':
			j := i + 2
			for j+1 < len(s) && !(s[j] == '#' && s[j+1] == '>') {
				j++
			}
			i = j + 1
		case c == '#' && !inWord:
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
		case c == '@' && !inWord && i+1 < len(s) && (isSQuote(s[i+1]) || isDQuote(s[i+1])) && psHereStart(s, i+2) > 0:
			// A here-string: @' or @" ending its line, to a line starting
			// '@ or "@.
			literal := isSQuote(s[i+1])
			// From the newline that ends the opening line.
			j := psHereStart(s, i+2) - 1
			closing := func(k int) int {
				if s[k] == '\n' && k+2 < len(s) && (literal && isSQuote(s[k+1]) || !literal && isDQuote(s[k+1])) && s[k+2] == '@' {
					return 3
				}
				return 0
			}
			var body string
			if literal {
				k := j
				for k < len(s) && closing(k) == 0 {
					k++
				}
				body, j = string(s[j:k]), k+3
			} else {
				body, j = expandable(j, closing, false)
			}
			word.WriteString(body)
			inWord = true
			i = j - 1
		case isSQuote(c):
			j := i + 1
			for j < len(s) {
				if isSQuote(s[j]) {
					if j+1 < len(s) && isSQuote(s[j+1]) {
						word.WriteRune(s[j])
						j += 2
						continue
					}
					break
				}
				word.WriteRune(s[j])
				j++
			}
			inWord = true
			i = j
		case isDQuote(c):
			q, j := expandable(i+1, func(k int) int {
				if isDQuote(s[k]) {
					return 1
				}
				return 0
			}, true)
			word.WriteString(q)
			inWord = true
			i = j - 1
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		case c == '&':
			switch {
			case i+1 < len(s) && s[i+1] == '&':
				endCommand()
				i++
			case len(words) == 0 && !inWord:
				// The call operator, unless nothing follows (a background
				// job's &); calling a script block, the block's commands
				// are read instead.
				if j := psSkipBlank(s, i+1); j < len(s) && !strings.ContainsRune(";|\n\r)}&{", s[j]) {
					words = append(words, "&")
				}
			default:
				endCommand() // a background job
			}
		case c == '.' && !inWord && len(words) == 0 && i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t'):
			// Dot-sourcing, as & is read.
			if j := psSkipBlank(s, i+1); j < len(s) && !strings.ContainsRune(";|\n\r)}&{", s[j]) {
				words = append(words, ".")
			}
		case strings.ContainsRune(";|\n(){}", c):
			endCommand()
		case (c == '$' || c == '@') && i+1 < len(s) && (s[i+1] == '(' || (c == '@' && s[i+1] == '{')):
			endCommand()
			i++
		case c == '>':
			// 2>, *>: the stream number is no word.
			if w := word.String(); inWord && (w == "*" || strings.Trim(w, "0123456789") == "") {
				word.Reset()
				inWord = false
			}
			endWord()
			words = append(words, ">")
			if i+1 < len(s) && s[i+1] == '>' {
				i++
			}
			if i+2 < len(s) && s[i+1] == '&' && s[i+2] >= '0' && s[i+2] <= '9' {
				i += 2
			}
		case c == '<':
			endWord()
			words = append(words, "<")
		case !inWord && (c == '–' || c == '—' || c == '―'):
			// PowerShell takes a dash for an option's hyphen.
			word.WriteRune('-')
			inWord = true
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	endCommand()
	return out
}

// psHereStart is where a here-string's body starts when its opening
// quote ends a line (from i, after @' or @"), or 0.
func psHereStart(s []rune, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	if i < len(s) && s[i] == '\n' {
		return i + 1
	}
	return 0
}

// psSkipBlank is the first index from i that isn't a space or tab.
func psSkipBlank(s []rune, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// psClose is the index of the ) that closes a ( opened before i,
// skipping quoted strings, or len(s).
func psClose(s []rune, i int) int {
	depth := 1
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c == '`':
			i++
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth == 0 {
				return i
			}
		case isSQuote(c) || isDQuote(c):
			for i++; i < len(s) && !(isSQuote(c) && isSQuote(s[i]) || isDQuote(c) && isDQuote(s[i])); i++ {
				if s[i] == '`' && isDQuote(c) {
					i++
				}
			}
		}
	}
	return len(s)
}
