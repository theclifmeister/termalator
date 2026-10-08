//go:build unix

package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

//go:embed scripts/*.toml
var embedded embed.FS

// script is a scenario: steps run in order within one turn.
type script struct {
	Step []step `toml:"step"`
}

// step is one [[step]] table; which fields matter depends on Do.
type step struct {
	Do          string         `toml:"do"`
	MS          int            `toml:"ms"`
	Text        string         `toml:"text"`
	Tool        string         `toml:"tool"`
	Input       map[string]any `toml:"input"`
	Response    map[string]any `toml:"response"`
	Ask         bool           `toml:"ask"`
	Subject     string         `toml:"subject"`
	Header      string         `toml:"header"`
	Question    string         `toml:"question"`
	Options     []string       `toml:"options"`
	Style       string         `toml:"style"` // question: "codex" draws Codex's menu
	At          int            `toml:"at"`    // codex question: its number k of "Question k/N"
	Of          int            `toml:"of"`    // codex question: N
	Type        string         `toml:"type"`
	Message     string         `toml:"message"`
	AfterMS     int            `toml:"after_ms"`
	Description string         `toml:"description"`
	ActiveForm  string         `toml:"active_form"`
	ID          any            `toml:"id"`
	Status      string         `toml:"status"`
	WaitingFor  string         `toml:"waiting_for"`
	Tools       *int           `toml:"tools"`
	Event       string         `toml:"event"`
	Payload     map[string]any `toml:"payload"`
	Cmd         string         `toml:"cmd"`
	Path        string         `toml:"path"`
	Code        int            `toml:"code"`
}

// steps lists every known step kind.
var steps = map[string]bool{
	"stream": true, "sleep": true, "tool": true, "permission": true, "question": true,
	"notify": true, "todo_create": true, "todo_update": true, "subagent": true,
	"suggestion": true, "hook": true, "status": true, "await_key": true,
	"cancel_silently": true, "clear": true, "compact": true, "run": true,
	"write": true, "stop": true, "exit": true, "crash": true,
}

// errNoScript means no script has that name.
var errNoScript = errors.New("no script")

// loadScript finds NAME.toml in $FAKEAGENT_SCRIPTS, then the embedded set.
func loadScript(name string) (script, error) {
	var s script
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return s, errNoScript
	}
	var data []byte
	if dir := os.Getenv("FAKEAGENT_SCRIPTS"); dir != "" {
		data, _ = os.ReadFile(filepath.Join(dir, name+".toml"))
	}
	if data == nil {
		var err error
		if data, err = embedded.ReadFile("scripts/" + name + ".toml"); err != nil {
			return s, errNoScript
		}
	}
	return parseScript(data)
}

// parseScript decodes and checks a script.
func parseScript(data []byte) (script, error) {
	var s script
	md, err := toml.Decode(string(data), &s)
	if err != nil {
		return s, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		var keys []string
		for _, k := range und {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return s, fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
	}
	for i, st := range s.Step {
		if !steps[st.Do] {
			return s, fmt.Errorf("step %d: unknown do %q", i+1, st.Do)
		}
	}
	return s, nil
}

// embeddedScripts lists the shipped script names.
func embeddedScripts() []string {
	var names []string
	_ = fs.WalkDir(embedded, "scripts", func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".toml") {
			names = append(names, strings.TrimSuffix(filepath.Base(p), ".toml"))
		}
		return nil
	})
	return names
}

// runPrompt runs the script a prompt names, or the default answer.
func (a *app) runPrompt(ctx context.Context, j job) error {
	if j.kind == "notification" {
		return a.stream(ctx, "The background agent finished.", 100*time.Millisecond)
	}
	name, ok := strings.CutPrefix(strings.TrimSpace(j.text), "run ")
	if !ok {
		return a.stream(ctx, "ok: "+j.text, 200*time.Millisecond)
	}
	name = strings.TrimSpace(name)
	s, err := loadScript(name)
	if errors.Is(err, errNoScript) {
		return a.stream(ctx, "no script "+name, 200*time.Millisecond)
	}
	if err != nil {
		return a.stream(ctx, "script "+name+": "+err.Error(), 200*time.Millisecond)
	}
	for i, st := range s.Step {
		var next *step
		if i+1 < len(s.Step) {
			next = &s.Step[i+1]
		}
		if err := a.runStep(ctx, st, next); err != nil {
			if errors.Is(err, errStop) {
				return nil
			}
			return err
		}
	}
	return nil
}

// say adds a conversation line.
func (a *app) say(prefix, text string) {
	a.mu.Lock()
	a.conv = append(a.conv, &convLine{prefix: prefix, text: text})
	a.mu.Unlock()
	a.requestRedraw()
}

// assistant records finished assistant text.
func (a *app) assistant(text string) {
	a.mu.Lock()
	a.lastText = text
	a.mu.Unlock()
	a.transcriptAppend(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}},
	}})
}

// stream reveals text over d with the spinner running.
func (a *app) stream(ctx context.Context, text string, d time.Duration) error {
	l := &convLine{prefix: "⏺ ", text: text, start: time.Now(), dur: d}
	a.mu.Lock()
	a.conv = append(a.conv, l)
	a.mu.Unlock()
	a.requestRedraw()
	if err := sleepCtx(ctx, d); err != nil {
		return err
	}
	a.mu.Lock()
	l.dur = 0
	a.mu.Unlock()
	a.requestRedraw()
	a.assistant(text)
	return nil
}

func (a *app) setInTool(v bool) {
	a.mu.Lock()
	a.inTool = v
	a.mu.Unlock()
}

// toolArg is what a tool line shows in parentheses.
func toolArg(input map[string]any) string {
	for _, k := range []string{"command", "file_path", "subject", "description", "prompt"} {
		if s, ok := input[k].(string); ok {
			return s
		}
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return fmt.Sprint(input[keys[0]])
	}
	return ""
}

// permissionTitle is the dialog's first line for a tool.
func permissionTitle(tool string) string {
	switch tool {
	case "Write":
		return "Create file"
	case "Edit", "MultiEdit", "NotebookEdit":
		return "Edit file"
	case "Bash":
		return "Bash command"
	}
	return tool
}

func defaultSubject(tool string, input map[string]any) string {
	if p, ok := input["file_path"].(string); ok && (tool == "Write" || tool == "Edit") {
		if tool == "Write" {
			return "create " + filepath.Base(p)
		}
		return "make this edit to " + filepath.Base(p)
	}
	return "proceed"
}

// askPermission shows the permission dialog unless yolo or the tool was
// allowed for the session. It fires PermissionRequest first. It returns
// whether the tool may run.
func (a *app) askPermission(ctx context.Context, tool, subject string, input map[string]any, id string) (bool, error) {
	if a.cx != nil {
		return a.codexApprove(ctx, tool, subject, input, id)
	}
	a.mu.Lock()
	skip := a.opts.yolo || a.alwaysAllow[tool]
	a.mu.Unlock()
	if skip {
		return true, nil
	}
	if _, err := a.fireHook(ctx, "PermissionRequest", map[string]any{"tool_name": tool, "tool_input": input, "tool_use_id": id}); err != nil {
		return false, err
	}
	if subject == "" {
		subject = defaultSubject(tool, input)
	}
	d := &dialog{kind: "permission", title: permissionTitle(tool), subject: subject,
		options: []string{"Yes", "Yes, and don't ask again this session", "No"}}
	n, err := a.waitDialog(ctx, d)
	if err != nil {
		return false, err
	}
	switch n {
	case 2:
		a.mu.Lock()
		a.alwaysAllow[tool] = true
		a.mu.Unlock()
	case 3:
		a.say("  ⎿  ", "User rejected "+tool)
		a.setInTool(false)
		a.say("⏺ ", "Not doing that.")
		a.assistant("Not doing that.")
		return false, nil
	}
	return true, nil
}

// orEmpty returns m, or an empty map for nil.
func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func notifyPayload(st step) map[string]any {
	typ, msg := st.Type, st.Message
	if typ == "" {
		typ = "permission_prompt"
	}
	if msg == "" {
		msg = "Claude needs your permission"
	}
	return map[string]any{"notification_type": typ, "message": msg}
}

// isDialogStep reports whether st opens a dialog.
func isDialogStep(st *step) bool {
	if st == nil {
		return false
	}
	switch st.Do {
	case "permission", "question", "write":
		return true
	case "tool":
		return st.Ask
	}
	return false
}

// runStep runs one script step. next is the step after it, if any.
func (a *app) runStep(ctx context.Context, st step, next *step) error {
	if ctx.Err() != nil {
		return errCancelled
	}
	ms := time.Duration(st.MS) * time.Millisecond
	switch st.Do {
	case "stream":
		text := st.Text
		if text == "" {
			text = "…"
		}
		return a.stream(ctx, text, ms)
	case "sleep":
		return sleepCtx(ctx, ms)
	case "tool", "permission":
		return a.toolStep(ctx, st)
	case "question":
		return a.questionStep(ctx, st)
	case "notify":
		if isDialogStep(next) {
			a.mu.Lock()
			a.notify = &st
			a.mu.Unlock()
			return nil
		}
		if err := sleepCtx(ctx, time.Duration(st.AfterMS)*time.Millisecond); err != nil {
			return err
		}
		_, err := a.fireHook(ctx, "Notification", notifyPayload(st))
		return err
	case "todo_create":
		return a.todoCreate(ctx, st)
	case "todo_update":
		return a.todoUpdate(ctx, st)
	case "subagent":
		return a.subagent(ctx, st)
	case "suggestion":
		a.mu.Lock()
		a.suggestion = st.Text
		a.mu.Unlock()
		a.requestRedraw()
		_, err := a.fireHook(ctx, "SubagentStop", map[string]any{"agent_id": randID("a", 17), "last_assistant_message": st.Text, "stop_hook_active": false})
		return err
	case "hook":
		_, err := a.fireHook(ctx, st.Event, orEmpty(st.Payload))
		return err
	case "status":
		a.mu.Lock()
		a.overrideStatusLocked(st.Status, st.WaitingFor)
		a.mu.Unlock()
		return nil
	case "await_key":
		ch := make(chan struct{})
		a.mu.Lock()
		a.awaitKey = ch
		a.mu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return errCancelled
		}
	case "cancel_silently":
		a.mu.Lock()
		a.cancelTurnLocked()
		a.mu.Unlock()
		return errCancelled
	case "clear":
		a.doClear()
	case "compact":
		a.doCompact(ctx)
	case "run":
		return a.runCmd(ctx, st)
	case "write":
		return a.writeStep(ctx, st)
	case "stop":
		return errStop
	case "exit":
		a.exit(st.Code, "other")
	case "crash":
		a.crash()
	}
	return nil
}

// toolStep is a tool call: PreToolUse, maybe a dialog, ms of work,
// PostToolUse.
func (a *app) toolStep(ctx context.Context, st step) error {
	input := orEmpty(st.Input)
	id := randID("toolu_", 24)
	a.say("⏺ ", st.Tool+"("+toolArg(input)+")")
	a.setInTool(true)
	if _, err := a.fireHook(ctx, "PreToolUse", map[string]any{"tool_name": st.Tool, "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	if st.Do == "permission" || st.Ask {
		ok, err := a.askPermission(ctx, st.Tool, st.Subject, input, id)
		if err != nil || !ok {
			return err
		}
	}
	if err := sleepCtx(ctx, time.Duration(st.MS)*time.Millisecond); err != nil {
		return err
	}
	resp := orEmpty(st.Response)
	if _, err := a.fireHook(ctx, "PostToolUse", map[string]any{"tool_name": st.Tool, "tool_input": input, "tool_response": resp, "tool_use_id": id}); err != nil {
		return err
	}
	out := "Done"
	if s, ok := resp["stdout"].(string); ok && s != "" {
		out = strings.TrimRight(s, "\n")
	}
	a.say("  ⎿  ", out)
	a.setInTool(false)
	return nil
}

// questionStep is AskUserQuestion.
func (a *app) questionStep(ctx context.Context, st step) error {
	opts := st.Options
	if len(opts) == 0 {
		opts = []string{"Yes", "No"}
	}
	if a.cx != nil {
		return a.codexQuestion(ctx, st, opts)
	}
	var optList []any
	for _, o := range opts {
		optList = append(optList, map[string]any{"label": o, "description": ""})
	}
	input := map[string]any{"questions": []any{map[string]any{
		"question": st.Question, "header": st.Header, "options": optList, "multiSelect": false,
	}}}
	id := randID("toolu_", 24)
	a.say("⏺ ", "AskUserQuestion("+st.Header+")")
	a.setInTool(true)
	if _, err := a.fireHook(ctx, "PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	if _, err := a.fireHook(ctx, "PermissionRequest", map[string]any{"tool_name": "AskUserQuestion", "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	all := append(append([]string(nil), opts...), "Type something.")
	d := &dialog{kind: "question", header: st.Header, question: st.Question, options: all, textOpt: len(all) - 1}
	if st.Style == "codex" {
		all = append(append([]string(nil), opts...), "None of the above")
		d = &dialog{kind: "codexq", question: st.Question, options: all, textOpt: len(all) - 1, at: max(st.At, 1), of: max(st.Of, 1)}
	}
	n, err := a.waitDialog(ctx, d)
	if err != nil {
		return err
	}
	answer := all[n-1]
	if n-1 == d.textOpt {
		a.mu.Lock()
		answer = string(d.text)
		a.mu.Unlock()
	}
	resp := map[string]any{"questions": input["questions"], "answers": map[string]any{st.Question: answer}}
	if _, err := a.fireHook(ctx, "PostToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": input, "tool_response": resp, "tool_use_id": id}); err != nil {
		return err
	}
	a.say("  ⎿  ", "· "+st.Question+" → "+answer)
	a.setInTool(false)
	return nil
}

func (a *app) currentTaskDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return taskDir(a.home, a.sid)
}

// todoCreate is TaskCreate.
func (a *app) todoCreate(ctx context.Context, st step) error {
	input := map[string]any{"subject": st.Subject, "description": st.Description}
	if st.ActiveForm != "" {
		input["activeForm"] = st.ActiveForm
	}
	id := randID("toolu_", 24)
	a.say("⏺ ", "TaskCreate("+st.Subject+")")
	if _, err := a.fireHook(ctx, "PreToolUse", map[string]any{"tool_name": "TaskCreate", "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	dir := a.currentTaskDir()
	tid, err := nextTaskID(dir)
	if err != nil {
		return err
	}
	_ = writeTask(dir, task{ID: tid, Subject: st.Subject, Description: st.Description, ActiveForm: st.ActiveForm, Status: "pending"})
	resp := map[string]any{"task": map[string]any{"id": tid, "subject": st.Subject}}
	if _, err := a.fireHook(ctx, "PostToolUse", map[string]any{"tool_name": "TaskCreate", "tool_input": input, "tool_response": resp, "tool_use_id": id}); err != nil {
		return err
	}
	a.say("  ⎿  ", "Task #"+tid+" created: "+st.Subject)
	_, err = a.fireHook(ctx, "TaskCreated", map[string]any{"task_id": tid, "task_subject": st.Subject, "task_description": st.Description})
	return err
}

// todoUpdate is TaskUpdate.
func (a *app) todoUpdate(ctx context.Context, st step) error {
	tid := fmt.Sprint(st.ID)
	input := map[string]any{"taskId": tid}
	var fields []string
	if st.Status != "" {
		input["status"] = st.Status
		fields = append(fields, "status")
	}
	if st.Subject != "" {
		input["subject"] = st.Subject
		fields = append(fields, "subject")
	}
	if st.ActiveForm != "" {
		input["activeForm"] = st.ActiveForm
		fields = append(fields, "activeForm")
	}
	id := randID("toolu_", 24)
	a.say("⏺ ", "TaskUpdate(#"+tid+" "+st.Status+")")
	if _, err := a.fireHook(ctx, "PreToolUse", map[string]any{"tool_name": "TaskUpdate", "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	dir := a.currentTaskDir()
	t, err := readTask(dir, tid)
	resp := map[string]any{"success": err == nil, "taskId": tid, "updatedFields": fields}
	if err == nil {
		from := t.Status
		if st.Subject != "" {
			t.Subject = st.Subject
		}
		if st.ActiveForm != "" {
			t.ActiveForm = st.ActiveForm
		}
		if st.Status != "" {
			t.Status = st.Status
			resp["statusChange"] = map[string]any{"from": from, "to": st.Status}
		}
		if t.Status == "deleted" {
			os.Remove(filepath.Join(dir, tid+".json"))
		} else {
			_ = writeTask(dir, t)
		}
	}
	if _, err := a.fireHook(ctx, "PostToolUse", map[string]any{"tool_name": "TaskUpdate", "tool_input": input, "tool_response": resp, "tool_use_id": id}); err != nil {
		return err
	}
	a.say("  ⎿  ", "Updated task #"+tid)
	if err == nil && st.Status == "completed" {
		_, err := a.fireHook(ctx, "TaskCompleted", map[string]any{"task_id": tid, "task_subject": t.Subject, "task_description": t.Description})
		return err
	}
	return nil
}

// subagent launches a background agent, as the Agent tool does.
func (a *app) subagent(ctx context.Context, st step) error {
	typ := st.Type
	if typ == "" {
		typ = "general-purpose"
	}
	tools := 1
	if st.Tools != nil {
		tools = *st.Tools
	}
	agentID := randID("a", 17)
	desc := st.Text
	if desc == "" {
		desc = "background work"
	}
	input := map[string]any{"description": desc, "prompt": desc, "subagent_type": typ, "run_in_background": true}
	id := randID("toolu_", 24)
	a.say("⏺ ", "Agent("+desc+")")
	if _, err := a.fireHook(ctx, "PreToolUse", map[string]any{"tool_name": "Agent", "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	resp := map[string]any{"status": "async_launched", "agentId": agentID, "description": desc}
	if _, err := a.fireHook(ctx, "PostToolUse", map[string]any{"tool_name": "Agent", "tool_input": input, "tool_response": resp, "tool_use_id": id}); err != nil {
		return err
	}
	a.say("  ⎿  ", "Backgrounded agent "+agentID)
	a.mu.Lock()
	a.bg++
	a.syncSessionLocked()
	a.mu.Unlock()
	_, _ = a.fireHook(context.Background(), "SubagentStart", map[string]any{"agent_id": agentID, "agent_type": typ})
	go a.runSubagent(agentID, typ, tools, time.Duration(st.MS)*time.Millisecond)
	return nil
}

// runSubagent is the background part: tool calls, SubagentStop, then a
// <task-notification> turn once the main turn is over.
func (a *app) runSubagent(agentID, typ string, tools int, total time.Duration) {
	gap := total / time.Duration(tools+1)
	ids := map[string]any{"agent_id": agentID, "agent_type": typ}
	with := func(m map[string]any) map[string]any {
		for k, v := range ids {
			m[k] = v
		}
		return m
	}
	for i := 0; i < tools; i++ {
		time.Sleep(gap)
		input := map[string]any{"command": fmt.Sprintf("echo sub-%d", i+1), "description": "subagent step"}
		id := randID("toolu_", 24)
		_, _ = a.fireHook(context.Background(), "PreToolUse", with(map[string]any{"tool_name": "Bash", "tool_input": input, "tool_use_id": id}))
		_, _ = a.fireHook(context.Background(), "PostToolUse", with(map[string]any{"tool_name": "Bash", "tool_input": input, "tool_use_id": id,
			"tool_response": map[string]any{"stdout": fmt.Sprintf("sub-%d\n", i+1), "stderr": "", "interrupted": false}}))
	}
	time.Sleep(total - gap*time.Duration(tools))
	_, _ = a.fireHook(context.Background(), "SubagentStop", with(map[string]any{"last_assistant_message": "Subagent done.", "stop_hook_active": false}))
	text := fmt.Sprintf("<task-notification>\n<task-id>%s</task-id>\n<status>completed</status>\n<summary>Agent %q completed</summary>\n</task-notification>", agentID, typ)
	a.mu.Lock()
	a.queue = append(a.queue, job{kind: "notification", text: text, via: "notification"})
	a.bg--
	a.syncSessionLocked()
	a.mu.Unlock()
	a.poke()
	a.requestRedraw()
}

// runCmd runs a shell command as the Bash tool.
func (a *app) runCmd(ctx context.Context, st step) error {
	input := map[string]any{"command": st.Cmd}
	id := randID("toolu_", 24)
	a.say("⏺ ", "Bash("+st.Cmd+")")
	a.setInTool(true)
	if _, err := a.fireHook(ctx, "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", st.Cmd)
	cmd.Dir = a.cwd
	cmd.Env = a.childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return errCancelled
	}
	out := strings.TrimRight(stdout.String()+stderr.String(), "\n")
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		msg := fmt.Sprintf("Exit code %d", code)
		if out != "" {
			msg += "\n" + out
		}
		if _, err := a.fireHook(ctx, "PostToolUseFailure", map[string]any{"tool_name": "Bash", "tool_input": input, "tool_use_id": id, "error": msg, "is_interrupt": false}); err != nil {
			return err
		}
		a.say("  ⎿  ", "Error: "+msg)
	} else {
		resp := map[string]any{"stdout": stdout.String(), "stderr": stderr.String(), "interrupted": false}
		if _, err := a.fireHook(ctx, "PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": input, "tool_response": resp, "tool_use_id": id}); err != nil {
			return err
		}
		if out == "" {
			out = "(No content)"
		}
		a.say("  ⎿  ", out)
	}
	a.setInTool(false)
	return nil
}

// deniedMsg is Claude's refusal for a write the settings deny.
const deniedMsg = "File is in a directory that is denied by your permission settings."

// writeStep is the Write tool, checked against the deny rules.
func (a *app) writeStep(ctx context.Context, st step) error {
	path := os.ExpandEnv(st.Path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.cwd, path)
	}
	input := map[string]any{"file_path": path, "content": st.Text}
	id := randID("toolu_", 24)
	a.say("⏺ ", "Write("+path+")")
	a.setInTool(true)
	if _, err := a.fireHook(ctx, "PreToolUse", map[string]any{"tool_name": "Write", "tool_input": input, "tool_use_id": id}); err != nil {
		return err
	}
	if denied(path, a.deny) {
		a.log("write", map[string]any{"path": path, "allowed": false, "reason": "settings"})
		if _, err := a.fireHook(ctx, "PostToolUseFailure", map[string]any{"tool_name": "Write", "tool_input": input, "tool_use_id": id, "error": deniedMsg, "is_interrupt": false}); err != nil {
			return err
		}
		a.say("  ⎿  ", "Error: "+deniedMsg)
		a.setInTool(false)
		return nil
	}
	subject := st.Subject
	if subject == "" {
		subject = "create " + filepath.Base(path)
	}
	ok, err := a.askPermission(ctx, "Write", subject, input, id)
	if err != nil {
		return err
	}
	if !ok {
		a.log("write", map[string]any{"path": path, "allowed": false, "reason": "user"})
		return nil
	}
	werr := os.MkdirAll(filepath.Dir(path), 0o755)
	if werr == nil {
		werr = os.WriteFile(path, []byte(st.Text), 0o644)
	}
	if werr != nil {
		a.log("write", map[string]any{"path": path, "allowed": true, "error": werr.Error()})
		if _, err := a.fireHook(ctx, "PostToolUseFailure", map[string]any{"tool_name": "Write", "tool_input": input, "tool_use_id": id, "error": werr.Error(), "is_interrupt": false}); err != nil {
			return err
		}
		a.say("  ⎿  ", "Error: "+werr.Error())
		a.setInTool(false)
		return nil
	}
	a.log("write", map[string]any{"path": path, "allowed": true})
	resp := map[string]any{"type": "create", "filePath": path, "content": st.Text, "structuredPatch": []any{}}
	if _, err := a.fireHook(ctx, "PostToolUse", map[string]any{"tool_name": "Write", "tool_input": input, "tool_response": resp, "tool_use_id": id}); err != nil {
		return err
	}
	a.say("  ⎿  ", fmt.Sprintf("Wrote %d lines to %s", strings.Count(st.Text, "\n")+1, path))
	a.setInTool(false)
	return nil
}
