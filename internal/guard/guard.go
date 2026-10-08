// Package guard is the guard's matcher (docs/SPEC.md §8.6, Guard): the
// standing rules as checks on each tool call. It is the Go port of the
// Claude mod's hooks/guard.ts, for agents without a mod: the server
// judges each call a PreToolUse hook reports and the agent's manifest
// answers with the refusal (.Guard in a respond template). The two
// matchers pass the same test vectors
// (internal/agent/claude/mod/tests/guard-vectors.ts): change them
// together.
//
// It only ever refuses: a call no rule matches goes on to the agent's
// own permission check. Bash commands are split into simple commands on
// ; & | && || newlines, $( ) and backticks, and read shell-style
// (quotes, env assignments, sudo, git -C). That catches what agents
// type; the sandbox and the permission rules stay the backstop for what
// is written to hide.
package guard

import (
	"regexp"
	"slices"
	"strings"
)

// Rules are what a session's guard refuses, as the server works them out
// from the human's settings and the session's role. Off, nothing is
// refused. It is also the body of GET /v1/rules on the mod socket.
type Rules struct {
	On   bool   `json:"on"`
	Role string `json:"role,omitempty"`
	// Rules are the config.GuardRules ids in force.
	Rules []string `json:"rules,omitempty"`
	// Home is the user's home directory, for ~ in paths and commands;
	// Cwd the folder the session started in, for relative paths.
	Home string `json:"home,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
	// Writable are the folders the file tools may write in under
	// worktree-only: the thread's worktree first, then the temporary
	// folders and Claude's own (plans, memory). Each as given and
	// with its symlinks resolved.
	Writable []string `json:"writable,omitempty"`
	// Worktrees is tm's worktrees folder: under delete-branch, no rm -r
	// takes it, a project's folder in it or a worktree.
	Worktrees string `json:"worktrees,omitempty"`
	// Protected are the branches no push may target: the repos' default
	// branches, main and master.
	Protected []string `json:"protected,omitempty"`
	// Secrets are the files and folders no tool may read under
	// credentials.
	Secrets []string `json:"secrets,omitempty"`
}

// Denial is a refusal: the rule, what the model reads, and a short
// account for the journal that carries no text of the call beyond a
// path or branch.
type Denial struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
	Summary string `json:"summary"`
}

// Judge is the rule the call of tool with input breaks, or nil. Tools
// are Claude's (Bash, Edit, Write, MultiEdit, NotebookEdit, Read, Grep,
// Glob) and Codex's (Bash, apply_patch).
func (r Rules) Judge(tool string, input map[string]any) *Denial {
	if !r.On || len(r.Rules) == 0 {
		return nil
	}
	str := func(k string) string {
		s, _ := input[k].(string)
		return s
	}
	switch tool {
	case "Bash":
		return r.judgeBash(str("command"))
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		p := str("file_path")
		if p == "" {
			p = str("notebook_path")
		}
		if p != "" && r.has("worktree-only") && !r.writable(r.abs(p)) {
			return r.deny("worktree-only", tool+" of "+safe(r.abs(p)), "")
		}
		return nil
	case "apply_patch":
		// Codex: the patch is the command; each file it adds, updates,
		// deletes or moves to is a write.
		if !r.has("worktree-only") {
			return nil
		}
		for _, p := range PatchPaths(str("command")) {
			if !r.writable(r.abs(p)) {
				return r.deny("worktree-only", tool+" of "+safe(r.abs(p)), "")
			}
		}
		return nil
	case "Read", "Grep", "Glob":
		if !r.has("credentials") {
			return nil
		}
		pattern := ""
		if tool == "Glob" {
			pattern = str("pattern")
		}
		for _, p := range []string{str("file_path"), str("path"), pattern} {
			if p != "" && r.isSecret(r.abs(p)) {
				return r.deny("credentials", tool+" of "+safe(r.abs(p)), "")
			}
		}
		return nil
	}
	return nil
}

var patchFile = regexp.MustCompile(`(?m)^\*\*\* (?:(?:Add|Update|Delete) File|Move to): (.+?)\s*$`)

// PatchPaths are the files a Codex patch (*** Begin Patch …) writes.
func PatchPaths(patch string) []string {
	var out []string
	for _, m := range patchFile.FindAllStringSubmatch(patch, -1) {
		out = append(out, m[1])
	}
	return out
}

func (r Rules) has(id string) bool { return slices.Contains(r.Rules, id) }

func (r Rules) writable(p string) bool {
	return slices.ContainsFunc(r.Writable, func(w string) bool { return under(p, w) })
}

// deny is the refusal under rule; the sentence's end says what to do,
// which differs for a thread (its report) and the coordinator (the user).
func (r Rules) deny(rule, summary, target string) *Denial {
	tell := "ask the user"
	if r.Role == "thread" {
		tell = "say so in your report"
	}
	first := ""
	if len(r.Writable) > 0 {
		first = r.Writable[0]
	}
	if target == "" {
		target = "the default branch"
	}
	var m string
	switch rule {
	case "force-push":
		m = "force-pushing is not allowed here. Push without force; if the branch has diverged, merge instead of rewriting it, or " + tell + "."
	case "push-default":
		m = "nothing is pushed straight to " + target + ": work lands through a pull request. Push your own branch and open a PR."
	case "worktree-only":
		m = "a thread writes only inside its worktree (" + first + "). Write the file there, or " + tell + " what needs changing elsewhere."
	case "delete-branch":
		m = "agents don't delete branches or worktrees; tm cleans them up once the work has merged. Leave them, and " + tell + " if one is in the way."
	case "merge":
		m = `merging is the coordinator's job in this project (merge = "coordinator"). Leave the PR open with CI green and say so in your report.`
	case "credentials":
		m = "credential files and tokens are not read here. If the task needs access you don't have, " + tell + " exactly what is missing."
	}
	return &Denial{Rule: rule, Message: "terminatr guard (" + rule + "): " + m, Summary: summary}
}

// judgeBash checks each simple command of a Bash command line.
func (r Rules) judgeBash(command string) *Denial {
	for _, words := range Commands(command) {
		argv := strip(words)
		if len(argv) == 0 {
			continue
		}
		cmd := base(argv[0])
		args := argv[1:]
		var d *Denial
		switch {
		case cmd == "git":
			d = r.judgeGit(args)
		case cmd == "gh":
			d = r.judgeGh(args)
		case cmd == "az":
			d = r.judgeAz(args)
		case httpClients[cmd]:
			d = r.judgeHTTP(args)
		case cmd == "rm" && r.has("delete-branch") && slices.ContainsFunc(args, func(a string) bool { return rmRecursive.MatchString(a) || a == "--recursive" }):
			for _, a := range args {
				if strings.HasPrefix(a, "-") {
					continue
				}
				if r.isWorktreeRoot(r.abs(a)) {
					d = r.deny("delete-branch", "rm -r of "+safe(r.abs(a)), "")
					break
				}
			}
		}
		if d != nil {
			return d
		}
		if r.has("credentials") {
			if d := r.judgeSecret(cmd, args, words); d != nil {
				return d
			}
		}
	}
	return nil
}

var (
	rmRecursive = regexp.MustCompile(`^-[a-zA-Z]*[rR]`)
	shortFlags  = regexp.MustCompile(`^-[a-zA-Z]+$`)
	branchUpper = regexp.MustCompile(`^-[a-zA-Z]*D`)
)

func (r Rules) judgeGit(args []string) *Denial {
	// Global options before the subcommand.
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			i++
		}
		i++
	}
	sub := at(args, i)
	var rest []string
	if i+1 < len(args) {
		rest = args[i+1:]
	}
	// long: one of the long options, with or without a value; flag: that
	// or the short one, alone or among others (-fu).
	long := func(names ...string) bool {
		return slices.ContainsFunc(rest, func(a string) bool {
			return slices.ContainsFunc(names, func(l string) bool { return a == l || strings.HasPrefix(a, l+"=") })
		})
	}
	flag := func(short string, names ...string) bool {
		return long(names...) || slices.ContainsFunc(rest, func(a string) bool { return shortFlags.MatchString(a) && strings.Contains(a, short) })
	}
	switch sub {
	case "push":
		if r.has("force-push") && (flag("f", "--force", "--force-with-lease", "--force-if-includes", "--mirror") || slices.ContainsFunc(rest, func(a string) bool { return strings.HasPrefix(a, "+") })) {
			return r.deny("force-push", "git push with force", "")
		}
		if r.has("delete-branch") && (flag("d", "--delete", "--prune") || slices.ContainsFunc(rest, func(a string) bool { return strings.HasPrefix(a, ":") && len(a) > 1 })) {
			return r.deny("delete-branch", "git push deleting a branch", "")
		}
		if r.has("push-default") {
			if long("--all", "--branches", "--mirror") {
				return r.deny("push-default", "git push --all", "")
			}
			pos := positionals(rest, []string{"-o", "--push-option", "--repo", "--receive-pack", "--exec"})
			for j := 1; j < len(pos); j++ {
				spec := pos[j]
				if k := strings.Index(spec, ":"); k >= 0 {
					spec = spec[k+1:]
				}
				target := branchOf(spec)
				if target != "" && slices.Contains(r.Protected, target) {
					return r.deny("push-default", "git push to "+safe(target), target)
				}
			}
		}
	case "branch":
		if r.has("delete-branch") && flag("d", "--delete") {
			return r.deny("delete-branch", "git branch --delete", "")
		}
		if r.has("delete-branch") && slices.ContainsFunc(rest, branchUpper.MatchString) {
			return r.deny("delete-branch", "git branch -D", "")
		}
	case "worktree":
		if w := at(rest, 0); r.has("delete-branch") && (w == "remove" || w == "prune") {
			return r.deny("delete-branch", "git worktree "+w, "")
		}
	case "update-ref":
		if r.has("delete-branch") && flag("d", "--delete") {
			return r.deny("delete-branch", "git update-ref -d", "")
		}
	case "credential":
		if r.has("credentials") && at(rest, 0) == "fill" {
			return r.deny("credentials", "git credential fill", "")
		}
	}
	return nil
}

var (
	ghMerge     = regexp.MustCompile(`pulls/[^/]+/merge/?$`)
	ghRefs      = regexp.MustCompile(`git/refs/heads/`)
	ghMethodOpt = regexp.MustCompile(`^(-X|--method=)`)
)

func (r Rules) judgeGh(args []string) *Denial {
	a, b := at(args, 0), at(args, 1)
	if a == "pr" && b == "merge" && r.has("merge") {
		return r.deny("merge", "gh pr merge", "")
	}
	if a == "repo" && b == "delete" && r.has("delete-branch") {
		return r.deny("delete-branch", "gh repo delete", "")
	}
	if a == "auth" && r.has("credentials") && (b == "token" || slices.Contains(args, "--show-token") || slices.Contains(args, "-t")) {
		if b == "token" {
			return r.deny("credentials", "gh auth token", "")
		}
		return r.deny("credentials", "gh auth status --show-token", "")
	}
	if a == "api" {
		var paths []string
		for _, x := range args[1:] {
			if !strings.HasPrefix(x, "-") {
				paths = append(paths, x)
			}
		}
		if r.has("merge") && slices.ContainsFunc(paths, ghMerge.MatchString) {
			return r.deny("merge", "gh api merging a PR", "")
		}
		method, found := "", false
		for i := 1; i < len(args); i++ {
			if args[i-1] == "-X" || args[i-1] == "--method" {
				method, found = strings.ToUpper(args[i]), true
				break
			}
		}
		if !found {
			for _, x := range args {
				if ghMethodOpt.MatchString(x) && len(x) > 2 {
					method = strings.ToUpper(ghMethodOpt.ReplaceAllString(x, ""))
					break
				}
			}
		}
		if r.has("delete-branch") && method == "DELETE" && slices.ContainsFunc(paths, ghRefs.MatchString) {
			return r.deny("delete-branch", "gh api deleting a branch", "")
		}
	}
	return nil
}

var methodOverride = regexp.MustCompile(`(?i)^x-http-method-override[=:]\s*(\w+)`)

// judgeAz judges az (the Azure CLI with its azure-devops extension) like
// gh, with the same rules and sentences. The extension's verbs:
// completing a PR (`az repos pr update --status completed`,
// `--auto-complete`, `--bypass-policy`, also on create) is the merge;
// `--delete-source-branch`, `az repos delete` and `az repos ref delete`
// delete; the REST routes under az rest, az devops invoke and curl are
// held to the same, by method and route.
func (r Rules) judgeAz(args []string) *Denial {
	// The command words come first; flags (any order, --x=v or --x v) after.
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if azValued[args[i]] {
			i += 2
		} else {
			i++
		}
	}
	var words []string
	for ; i < len(args) && !strings.HasPrefix(args[i], "-"); i++ {
		words = append(words, strings.ToLower(args[i]))
	}
	var rest []string
	if i < len(args) {
		rest = args[i:]
	}
	a, b, c := at(words, 0), at(words, 1), at(words, 2)
	if a == "repos" && b == "pr" && (c == "update" || c == "create") {
		completes := slices.ContainsFunc(opts(rest, "--status"), func(v string) bool { return strings.ToLower(v) == "completed" }) ||
			truthy(rest, "--auto-complete") || truthy(rest, "--bypass-policy")
		if r.has("merge") && completes {
			return r.deny("merge", "az repos pr "+c+" completing a PR", "")
		}
		if r.has("delete-branch") && truthy(rest, "--delete-source-branch") {
			return r.deny("delete-branch", "az repos pr "+c+" --delete-source-branch", "")
		}
	}
	if a == "repos" && b == "delete" && r.has("delete-branch") {
		return r.deny("delete-branch", "az repos delete", "")
	}
	if a == "repos" && b == "ref" && c == "delete" && r.has("delete-branch") {
		return r.deny("delete-branch", "az repos ref delete", "")
	}
	if r.has("credentials") {
		if a == "account" && b == "get-access-token" {
			return r.deny("credentials", "az account get-access-token", "")
		}
		if a == "devops" && b == "login" {
			return r.deny("credentials", "az devops login", "")
		}
	}
	if a == "rest" {
		// A method override header is the method the service acts on.
		method := ""
		for _, h := range optsMulti(rest, "--headers") {
			if m := methodOverride.FindStringSubmatch(h); m != nil {
				method = m[1]
				break
			}
		}
		if method == "" {
			method = first(opts(rest, "--method", "-m"), "GET")
		}
		return r.judgeRoute(method, first(opts(rest, "--url", "--uri", "-u"), ""))
	}
	if a == "devops" && b == "invoke" {
		resource := strings.ToLower(first(opts(rest, "--resource"), ""))
		method := first(opts(rest, "--http-method"), "GET")
		var params []string
		for _, p := range optsMulti(rest, "--route-parameters") {
			params = append(params, strings.ToLower(p))
		}
		area := strings.ToLower(first(opts(rest, "--area"), "git"))
		if area != "git" || !isWrite(method) {
			return nil
		}
		if r.has("merge") && (resource == "merges" || (resource == "pullrequests" && slices.ContainsFunc(params, func(p string) bool { return strings.HasPrefix(p, "pullrequestid=") }))) {
			return r.deny("merge", "az devops invoke "+safe(strings.ToUpper(method))+" on "+safe(resource), "")
		}
		if r.has("delete-branch") && (resource == "refs" || (resource == "repositories" && strings.ToUpper(method) == "DELETE")) {
			return r.deny("delete-branch", "az devops invoke "+safe(strings.ToUpper(method))+" on "+safe(resource), "")
		}
	}
	return nil
}

// httpClients speak HTTP; given an Azure DevOps address they are judged
// by route and method as az rest is, and by what they authenticate with.
var httpClients = map[string]bool{"curl": true, "wget": true, "http": true, "https": true, "xh": true}

var (
	azureHost = regexp.MustCompile(`(?i)(^|[/@.])(dev\.azure\.com|[a-z0-9-]+\.visualstudio\.com|vssps\.dev\.azure\.com)([/:?#]|$)`)
	httpUser  = regexp.MustCompile(`^(-u.*|--user(=.*)?|--oauth2-bearer(=.*)?)$`)
	httpAuth  = regexp.MustCompile(`(?i)authorization:|AZURE_DEVOPS_EXT_PAT`)
	shortX    = regexp.MustCompile(`^-X.`)
)

func (r Rules) judgeHTTP(args []string) *Denial {
	i := slices.IndexFunc(args, azureHost.MatchString)
	if i < 0 {
		return nil
	}
	url := args[i]
	if r.has("credentials") && slices.ContainsFunc(args, func(a string) bool { return httpUser.MatchString(a) || httpAuth.MatchString(a) }) {
		return r.deny("credentials", "an HTTP call to Azure DevOps with credentials", "")
	}
	method := ""
	for i, a := range args {
		switch {
		case a == "-X" || a == "--request":
			method = at(args, i+1)
		case strings.HasPrefix(a, "--request="):
			method = a[len("--request="):]
		case shortX.MatchString(a):
			method = a[2:]
		}
	}
	if method == "" {
		method = "GET"
	}
	return r.judgeRoute(method, url)
}

var (
	routeSplit = regexp.MustCompile(`[?#]`)
	routePR    = regexp.MustCompile(`/pullrequests/[^/]+$`)
	routeMerge = regexp.MustCompile(`/merges$`)
	routeRefs  = regexp.MustCompile(`/refs$`)
	routeRepo  = regexp.MustCompile(`/repositories/[^/]+$`)
)

// judgeRoute: a write method on an Azure DevOps Git route that completes
// a PR or deletes a ref or a repository.
func (r Rules) judgeRoute(method, url string) *Denial {
	m := strings.ToUpper(method)
	if !isWrite(m) {
		return nil
	}
	path := strings.TrimRight(strings.ToLower(routeSplit.Split(url, 2)[0]), "/")
	if !strings.Contains(path, "/_apis/git/") {
		return nil
	}
	if r.has("merge") && (routePR.MatchString(path) || routeMerge.MatchString(path)) {
		return r.deny("merge", safe(m)+" on an Azure DevOps pull request", "")
	}
	if r.has("delete-branch") && (routeRefs.MatchString(path) || (m == "DELETE" && routeRepo.MatchString(path))) {
		return r.deny("delete-branch", safe(m)+" on an Azure DevOps ref or repository", "")
	}
	return nil
}

func isWrite(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS", "":
		return false
	}
	return true
}

// azValued are az's global options that take a value before the
// command words.
var azValued = map[string]bool{"--output": true, "-o": true, "--query": true, "--subscription": true,
	"--organization": true, "--org": true, "--project": true, "-p": true}

// opts are the values of the option under any of names, as --x v or
// --x=v.
func opts(args []string, names ...string) []string {
	var out []string
	for i, a := range args {
		for _, n := range names {
			if a == n {
				v := at(args, i+1)
				if i+1 >= len(args) || strings.HasPrefix(v, "-") {
					v = ""
				}
				out = append(out, v)
			} else if strings.HasPrefix(a, n+"=") {
				out = append(out, a[len(n)+1:])
			}
		}
	}
	return out
}

// optsMulti is every value after the option up to the next option, for
// those that take several (--route-parameters a=1 b=2).
func optsMulti(args []string, name string) []string {
	var out []string
	for i, a := range args {
		if a == name {
			for j := i + 1; j < len(args) && !strings.HasPrefix(args[j], "-"); j++ {
				out = append(out, args[j])
			}
		} else if strings.HasPrefix(a, name+"=") {
			out = append(out, a[len(name)+1:])
		}
	}
	return out
}

// truthy: a boolean option given true, or bare (az takes true/false,
// and a bare flag counts as given).
func truthy(args []string, name string) bool {
	return slices.ContainsFunc(opts(args, name), func(v string) bool {
		switch strings.ToLower(v) {
		case "false", "no", "n", "0", "off":
			return false
		}
		return true
	})
}

// readers print or copy what they are given.
var readers = map[string]bool{}

func init() {
	for _, c := range strings.Fields(`cat head tail less more bat nl tac cp mv scp rsync base64 xxd od
		hexdump strings grep egrep fgrep rg ag awk sed cut sort uniq tar zip
		gzip openssl gpg jq yq diff cmp source . curl dd`) {
		readers[c] = true
	}
}

var (
	// secretName: environment variable names that look like they hold
	// a secret.
	secretName  = regexp.MustCompile(`(?i)token|secret|key|password|passwd|credential|(^|_)pat$`)
	securityCmd = regexp.MustCompile(`^(find-(generic|internet)-password|dump-keychain|export)$`)
	shellVar    = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)
	azurePAT    = regexp.MustCompile(`(?i)^AZURE_DEVOPS_EXT_PAT$|^AZURE_DEVOPS_.*(PAT|TOKEN)$`)
)

func (r Rules) judgeSecret(cmd string, args, words []string) *Denial {
	if cmd == "security" && securityCmd.MatchString(at(args, 0)) {
		return r.deny("credentials", "security "+at(args, 0), "")
	}
	if cmd == "printenv" {
		// printenv NAME... prints only what it is asked for: fine unless a
		// name looks secret. With no name it prints everything.
		var names []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				names = append(names, a)
			}
		}
		if len(names) == 0 {
			return r.deny("credentials", "printenv printing the environment", "")
		}
		if i := slices.IndexFunc(names, secretName.MatchString); i >= 0 {
			return r.deny("credentials", "printenv "+safe(names[i]), "")
		}
	} else if cmd == "env" && !slices.ContainsFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") }) {
		return r.deny("credentials", "env printing the environment", "")
	}
	// echo and the like given a secret variable ($AZURE_DEVOPS_EXT_PAT).
	if cmd == "echo" || cmd == "printf" || cmd == "print" || readers[cmd] {
		for _, w := range words {
			if m := shellVar.FindStringSubmatch(w); m != nil && azurePAT.MatchString(m[1]) {
				return r.deny("credentials", cmd+" of "+safe(m[1]), "")
			}
		}
	}
	// A reader given a secret, or a secret redirected in.
	for i, w := range words {
		redirected := w == "<" || (i > 0 && words[i-1] == "<")
		path := w
		if strings.HasPrefix(w, "<") && len(w) > 1 {
			path = w[1:]
		}
		if (readers[cmd] || redirected) && !strings.HasPrefix(path, "-") && path != "<" && r.isSecret(r.abs(path)) {
			return r.deny("credentials", cmd+" of "+safe(r.abs(path)), "")
		}
	}
	return nil
}

var substitution = regexp.MustCompile("\\$\\(([^)]*)\\)|`([^`]*)`")

// Commands splits a command line into simple commands, each a list of
// words with quotes removed; redirections stay words ("<", ">").
func Commands(line string) [][]string {
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
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			if s[i+1] != '\n' {
				word.WriteRune(s[i+1])
				inWord = true
			}
			i++
		case c == '\'':
			end := len(s)
			if j := slices.Index(s[i+1:], '\''); j >= 0 {
				end = i + 1 + j
			}
			word.WriteString(string(s[i+1 : end]))
			inWord = true
			i = end
		case c == '"':
			// Inside double quotes a $( ) or backtick still runs: its
			// inside is read as commands too.
			j := i + 1
			var q strings.Builder
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) {
					q.WriteRune(s[j+1])
					j += 2
					continue
				}
				q.WriteRune(s[j])
				j++
			}
			for _, m := range substitution.FindAllStringSubmatch(q.String(), -1) {
				inner := m[1]
				if inner == "" {
					inner = m[2]
				}
				out = append(out, Commands(inner)...)
			}
			word.WriteString(q.String())
			inWord = true
			i = j
		case c == ' ' || c == '\t':
			endWord()
		case strings.ContainsRune(";&|\n()`{}", c):
			endCommand()
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			endCommand()
			i++
		case c == '<' || c == '>':
			endWord()
			words = append(words, string(c))
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	endCommand()
	return out
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// strip drops what runs a command without being it: env assignments,
// sudo, command, exec, nohup, time, env with assignments.
func strip(words []string) []string {
	i := 0
	for {
		if i >= len(words) {
			return nil
		}
		w := words[i]
		switch {
		case assignment.MatchString(w):
			i++
		case w == "sudo" || w == "command" || w == "exec" || w == "nohup" || w == "time" || w == "builtin":
			i++
		case w == "env" && i+1 < len(words) && (strings.Contains(words[i+1], "=") || strings.HasPrefix(words[i+1], "-")):
			i++
			for i < len(words) && (strings.Contains(words[i], "=") || strings.HasPrefix(words[i], "-")) {
				i++
			}
		default:
			return words[i:]
		}
	}
}

// positionals are args without their options, valued options skipping
// their value.
func positionals(args, valued []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch {
		case slices.Contains(valued, args[i]), args[i] == "<", args[i] == ">":
			i++
		case !strings.HasPrefix(args[i], "-"):
			out = append(out, args[i])
		}
	}
	return out
}

func branchOf(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "+"), "refs/heads/")
}

func base(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// at is s[i], or "" past its end.
func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// first is s[0], or def for none.
func first(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// abs is p made absolute and clean: ~ is home, a relative path is from
// the session's folder.
func (r Rules) abs(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = r.Home + p[1:]
	} else if p == "$HOME" || strings.HasPrefix(p, "$HOME/") {
		p = r.Home + p[5:]
	}
	if !strings.HasPrefix(p, "/") {
		dir := r.Cwd
		if dir == "" {
			dir = "/"
			if len(r.Writable) > 0 {
				dir = r.Writable[0]
			}
		}
		p = dir + "/" + p
	}
	var parts []string
	for _, s := range strings.Split(p, "/") {
		switch s {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, s)
		}
	}
	return "/" + strings.Join(parts, "/")
}

func under(p, root string) bool {
	if root == "" {
		return false
	}
	if p == root {
		return true
	}
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	return strings.HasPrefix(p, root)
}

var wildcard = regexp.MustCompile(`[*?[{].*$`)

// isSecret: p is a credential store or inside one; a glob counts by the
// part before its first wildcard.
func (r Rules) isSecret(p string) bool {
	fixed := strings.TrimSuffix(wildcard.ReplaceAllString(p, ""), "/")
	if fixed == "" {
		fixed = "/"
	}
	return slices.ContainsFunc(r.Secrets, func(s string) bool {
		return under(fixed, s) || (fixed != p && under(s, fixed) && fixed != "/" && fixed != r.Home)
	})
}

// isWorktreeRoot: removing p takes a worktree with it: the thread's own,
// tm's worktrees folder, a project's folder in it or a worktree in that.
func (r Rules) isWorktreeRoot(p string) bool {
	if r.Role == "thread" && len(r.Writable) > 0 && under(r.Writable[0], p) {
		return true
	}
	wt := r.Worktrees
	if wt == "" {
		return false
	}
	if under(wt, p) {
		return true
	}
	if !under(p, wt) {
		return false
	}
	return len(strings.Split(p[len(wt)+1:], "/")) <= 2
}

// safe keeps a path or branch to the characters a journal line needs.
func safe(s string) string {
	var b strings.Builder
	n := 0
	for _, c := range s {
		ok := c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._/~@+-", c))
		w := 1
		if c > 0xFFFF {
			w = 2 // two UTF-16 units, as guard.ts counts them
		}
		for range w {
			if n == 100 {
				return b.String()
			}
			if ok {
				b.WriteRune(c)
			} else {
				b.WriteByte('?')
			}
			n++
		}
	}
	return b.String()
}
