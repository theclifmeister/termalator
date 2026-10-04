package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"go.mitchellh.com/libghostty"
)

const streamLines = 300

func claudeScenario(inline bool) {
	name := "claude"
	if inline {
		name = "claude-inline"
		os.Setenv("CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN", "1")
	}
	sockDir, _ := os.MkdirTemp("", "gvt")
	sock := filepath.Join(sockDir, "claude.sock")
	os.Setenv("GVT_RAWLOG", filepath.Join(*outDir, "claude-pty-raw.bin"))
	defer func() {
		b, _ := os.ReadFile(sock + ".log")
		os.WriteFile(filepath.Join(*outDir, "claude-server.log"), b, 0o644)
	}()
	logf := filepath.Join(*outDir, "claude-client.log")
	stats := func(n string) string { return filepath.Join(*outDir, "claude-"+n+"-stats.json") }

	w := openWindow("A", 120, 40, "new", "-s", sock, "-log", logf, "-stats", stats("A"),
		"--", "claude", "--model", *model, "--tools", "", "--strict-mcp-config")
	time.Sleep(time.Second)
	sp, cp := pids(sock)
	if sp == 0 {
		w.save("claude-startup-failed")
		b, _ := os.ReadFile(sock + ".log")
		fmt.Printf("server did not start:\n%s", b)
		os.Exit(1)
	}
	note("server pid=%d claude pid=%d", sp, cp)

	// Startup: maybe a folder-trust dialog first.
	start := time.Now()
	for time.Since(start) < 30*time.Second {
		s := w.screen()
		if strings.Contains(s, "trust") && strings.Contains(s, "Yes") {
			w.save("claude-0-trust")
			w.key(libghostty.KeyEnter, 0, "", 0)
			time.Sleep(time.Second)
			continue
		}
		if strings.Contains(s, "for shortcuts") || strings.Contains(s, "> ") || strings.Contains(s, "❯") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	w.quiet(time.Second, 10*time.Second)
	if !compare("claude-1-startup", w, sock) && strings.TrimSpace(w.screen()) == "" {
		fmt.Println("nothing on screen; aborting")
		os.Exit(1)
	}

	// Shift+Enter and Ctrl+Enter in the prompt box.
	w.typeText("first line")
	b := w.key(libghostty.KeyEnter, libghostty.ModShift, "", 0)
	note("outer terminal sent Shift+Enter as %q", b)
	w.typeText("second line")
	w.quiet(500*time.Millisecond, 5*time.Second)
	s := w.save("claude-2-shift-enter")
	res("Shift+Enter inserts a newline (not submit)", rowsOf(s, "first line") != rowsOf(s, "second line") && rowsOf(s, "second line") >= 0, "")
	b = w.key(libghostty.KeyEnter, libghostty.ModCtrl, "", 0)
	note("outer terminal sent Ctrl+Enter as %q", b)
	w.typeText("third line")
	w.quiet(500*time.Millisecond, 5*time.Second)
	s = w.save("claude-3-ctrl-enter")
	submitted := !strings.Contains(s, "third line") || strings.Contains(s, "⏺")
	note("Ctrl+Enter: Claude %s", map[bool]string{true: "SUBMITTED the prompt", false: "inserted a newline"}[submitted])
	note("inner encodings are in claude-client.log (key ... -> ...)")
	if submitted {
		w.quiet(2*time.Second, 60*time.Second)
	} else {
		clearPrompt(w)
	}

	// Bracketed paste of multi-line text must not submit.
	w.paste("paste-alpha\npaste-beta\npaste-gamma")
	w.quiet(700*time.Millisecond, 5*time.Second)
	s = w.save("claude-4-paste")
	res("multi-line bracketed paste lands in the prompt without submitting",
		strings.Contains(s, "paste-alpha") || strings.Contains(s, "Pasted text"), firstMatch(s, `.*(paste-|Pasted).*`))
	clearPrompt(w)

	// Wide characters / emoji typed into the prompt.
	w.typeText("漢字テスト 👋🏽 🇳🇱 👨‍👩‍👧 café")
	w.quiet(700*time.Millisecond, 5*time.Second)
	ok := compare("claude-5-wide", w, sock)
	s = w.screen()
	res("wide chars/emoji visible in prompt", strings.Contains(s, "漢字テスト") && strings.Contains(s, "café"), "")
	_ = ok
	clearPrompt(w)

	// Streaming: ask for a long list, then detach / reattach mid-stream.
	w.typeText(fmt.Sprintf("Print the numbers 1 to %d, one per line, each line exactly 'row N ok' (e.g. row 7 ok). No tools, no other text.", streamLines))
	w.key(libghostty.KeyEnter, 0, "", 0)
	cpu0 := map[string]time.Duration{"server": cpuTime(sp), "claude": treeCPU(cp)}
	t0 := time.Now()
	clientCPU := cpuTime(w.cmd.Process.Pid)
	if !w.waitFor("row 20 ok", 90*time.Second) {
		res("Claude started streaming", false, "")
	}
	clientCPU = cpuTime(w.cmd.Process.Pid) - clientCPU
	w.save("claude-6-streaming-A")
	w.key(libghostty.KeyBackslash, libghostty.ModCtrl, "", '\\')
	<-w.exit
	note("detached A mid-stream at %v", time.Since(t0).Round(time.Millisecond))
	res("server + claude alive after mid-stream detach", alive(sp) && alive(cp), "")
	before := lastRow(dump(sock, "-all"))
	time.Sleep(2 * time.Second)
	after := lastRow(dump(sock, "-all"))
	res("claude kept producing output while detached", after > before, fmt.Sprintf("last row %d -> %d", before, after))

	// Reattach in a different-sized window while it streams.
	bc, br := uint16(100), uint16(32)
	if *same {
		bc, br = 120, 40
	}
	w2 := openWindow("B", bc, br, "attach", "-s", sock, "-log", logf, "-stats", stats("B"))
	w2.quiet(300*time.Millisecond, 3*time.Second)
	for i := 0; i < 5; i++ {
		w2.cmd.Process.Signal(syscall.SIGUSR1) // in-stream digest: mirror == server?
		time.Sleep(400 * time.Millisecond)
	}
	w2.save("claude-7-streaming-B")
	err := w2.closeWindow()
	res("window B closed mid-stream; only the client died", err == nil && alive(sp) && alive(cp), fmt.Sprint(err))

	time.Sleep(2 * time.Second)
	w3 := openWindow("C", 120, 40, "attach", "-s", sock, "-log", logf, "-stats", stats("C"))
	// Wait for completion.
	end := time.Now().Add(3 * time.Minute)
	for time.Now().Before(end) && lastRow(dump(sock, "-all")) < streamLines {
		time.Sleep(500 * time.Millisecond)
	}
	w3.quiet(2*time.Second, 30*time.Second)
	wall := time.Since(t0)
	note("stream finished after %v", wall.Round(time.Millisecond))
	note("CPU during stream: server %v, claude tree %v (wall %v)", cpuTime(sp)-cpu0["server"], treeCPU(cp)-cpu0["claude"], wall.Round(time.Millisecond))
	note("client A CPU while streaming to it: %v", clientCPU)
	w3.cmd.Process.Signal(syscall.SIGUSR1)
	time.Sleep(300 * time.Millisecond)
	compare("claude-8-after-reattach", w3, sock)

	// Ghost / duplicate line check.
	all := dump(sock, "-all")
	os.WriteFile(filepath.Join(*outDir, "claude-9-server-all.txt"), []byte(all), 0o644)
	if inline {
		// Inline mode: finished output lives in the emulator's scrollback.
		dup, missing := rowCounts(all)
		res("inline: every streamed row appears exactly once in screen+scrollback (no ghost/duplicate lines)",
			len(dup) == 0 && len(missing) == 0, fmt.Sprintf("dup=%v missing=%v", short(dup), short(missing)))
	}
	for _, f := range []string{"claude-6-streaming-A", "claude-7-streaming-B", "claude-8-after-reattach-outer"} {
		b, _ := os.ReadFile(filepath.Join(*outDir, f+".txt"))
		ok, detail := consecutive(string(b))
		res("no ghost/duplicate rows on screen "+f, ok, detail)
	}

	// Scrollback: inline -> client-local Shift+PgUp; fullscreen -> mouse
	// wheel forwarded to Claude, which scrolls its own transcript.
	top := firstLine(w3.screen())
	if inline {
		w3.key(libghostty.KeyPageUp, libghostty.ModShift, "", 0)
	} else {
		b := w3.wheel(true, 20, 10, 10)
		note("wheel up sent by outer terminal as %q", b)
	}
	w3.quiet(500*time.Millisecond, 3*time.Second)
	s = w3.save("claude-12-scrolled")
	res("scrolling back shows earlier output", firstLine(s) != top, fmt.Sprintf("top %q -> %q", top, firstLine(s)))
	if ok, detail := consecutive(s); !ok {
		res("no ghost/duplicate rows on scrolled screen", ok, detail)
	} else {
		res("no ghost/duplicate rows on scrolled screen", ok, detail)
	}
	if !inline {
		w3.wheel(false, 20, 10, 10)
	} else {
		w3.typeText("x") // any key snaps the client viewport back to the bottom
		clearPrompt(w3)
	}
	w3.quiet(500*time.Millisecond, 3*time.Second)
	// Resize while idle.
	w3.resize(90, 30)
	w3.quiet(time.Second, 5*time.Second)
	compare("claude-10-resize-90x30", w3, sock)
	w3.resize(140, 45)
	w3.quiet(time.Second, 5*time.Second)
	compare("claude-11-resize-140x45", w3, sock)

	w3.typeText("x")
	w3.quiet(500*time.Millisecond, 3*time.Second)
	clearPrompt(w3)
	w3.cmd.Process.Signal(syscall.SIGUSR1)
	time.Sleep(300 * time.Millisecond)
	compare("claude-13-back-at-bottom", w3, sock)

	// Detach cleanly so C writes its stats, then stop claude.
	w3.key(libghostty.KeyBackslash, libghostty.ModCtrl, "", '\\')
	<-w3.exit
	syscall.Kill(cp, syscall.SIGTERM)
	time.Sleep(2 * time.Second)
	res("server exits after claude exits", !alive(sp), "")
	for _, n := range []string{"A", "B", "C"} {
		b, _ := os.ReadFile(stats(n))
		var st struct {
			Frames, FrameBytes, OutputMsgs, OutputBytes, Holds, HoldTimeouts, DigestOK, DigestMismatch, SnapshotBytes int
			SnapshotDecode                                                                                            string
			Summary                                                                                                   map[string]string
		}
		json.Unmarshal(b, &st)
		note("client %s: frames=%d frameBytes=%d outMsgs=%d outBytes=%d 2026holds=%d holdTimeouts=%d digestOK=%d mismatch=%d snapshot=%dB decode=%s",
			n, st.Frames, st.FrameBytes, st.OutputMsgs, st.OutputBytes, st.Holds, st.HoldTimeouts, st.DigestOK, st.DigestMismatch, st.SnapshotBytes, st.SnapshotDecode)
		for _, k := range []string{"output_to_frame", "key_to_echo", "render_time", "client_cpu", "client_maxrss"} {
			note("  %s: %s", k, st.Summary[k])
		}
	}
	_ = name
}

func clearPrompt(w *window) {
	w.key(libghostty.KeyC, libghostty.ModCtrl, "", 'c')
	w.quiet(500*time.Millisecond, 3*time.Second)
}

func rowsOf(s, sub string) int {
	for i, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func firstMatch(s, re string) string {
	return strings.TrimSpace(regexp.MustCompile(re).FindString(s))
}

func lastRow(s string) int {
	last := 0
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:⏺\s*)?row (\d+) ok\s*$`).FindAllStringSubmatch(s, -1) {
		var n int
		fmt.Sscan(m[1], &n)
		if n > last {
			last = n
		}
	}
	return last
}

func short(xs []int) string {
	if len(xs) > 10 {
		return fmt.Sprintf("%v... (%d)", xs[:10], len(xs))
	}
	return fmt.Sprint(xs)
}

func rowCounts(s string) (dup, missing []int) {
	counts := map[int]int{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:⏺\s*)?row (\d+) ok\s*$`).FindAllStringSubmatch(s, -1) {
		var n int
		fmt.Sscan(m[1], &n)
		counts[n]++
	}
	for i := 1; i <= streamLines; i++ {
		switch {
		case counts[i] == 0:
			missing = append(missing, i)
		case counts[i] > 1:
			dup = append(dup, i)
		}
	}
	return
}

// consecutive checks that "row N ok" lines on one screen run N, N+1, ...
// with no repeats or gaps (a ghost/duplicated line would break this).
func consecutive(s string) (bool, string) {
	var nums []int
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:⏺\s*)?row (\d+) ok\s*$`).FindAllStringSubmatch(s, -1) {
		var n int
		fmt.Sscan(m[1], &n)
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		return true, "no rows on screen"
	}
	for i := 1; i < len(nums); i++ {
		if nums[i] != nums[i-1]+1 {
			return false, fmt.Sprintf("row %d followed by %d", nums[i-1], nums[i])
		}
	}
	return true, fmt.Sprintf("rows %d..%d", nums[0], nums[len(nums)-1])
}
