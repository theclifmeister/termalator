package pty

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The test binary is its own child: PTY_TEST_CHILD names what it does.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PTY_TEST_CHILD"); mode != "" {
		child(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func child(mode string) {
	switch mode {
	case "size": // the console's size, then exit 3
		fmt.Printf("size %s env %s\r\n", consoleSize(), os.Getenv("PTY_TEST_VAR"))
		os.Exit(3)
	case "resize": // the size once a line is typed
		fmt.Print("ready\r\n")
		bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Printf("size %s\r\n", consoleSize())
		time.Sleep(30 * time.Second)
	case "sleep":
		fmt.Print("ready\r\n")
		time.Sleep(30 * time.Second)
	case "stubborn": // takes CTRL_CLOSE_EVENT and never returns from it
		setCtrlHandler.Call(windows.NewCallback(func(uint32) uintptr {
			time.Sleep(time.Minute)
			return 1
		}), 1)
		fmt.Print("ready\r\n")
		time.Sleep(time.Minute)
	case "tree": // a child of its own, then wait
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "PTY_TEST_CHILD=sleep")
		if err := cmd.Start(); err != nil {
			fmt.Printf("start: %v\r\n", err)
			os.Exit(1)
		}
		fmt.Printf("child %d\r\n", cmd.Process.Pid)
		time.Sleep(time.Minute)
	case "title": // a title of its own
		fmt.Print("\x1b]0;mine\x07titled\r\n")
		time.Sleep(time.Second)
	}
}

var setCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")

func consoleSize() string {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info); err != nil {
		return err.Error()
	}
	w := info.Window
	return fmt.Sprintf("%dx%d", w.Right-w.Left+1, w.Bottom-w.Top+1)
}

// apis are the ConPTYs to test: the system's, and the shipped one when
// conpty.dll and OpenConsole.exe are next to the test binary.
func apis(t *testing.T) map[string]*conptyAPI {
	m := map[string]*conptyAPI{"system": systemConpty()}
	exe, _ := os.Executable()
	if api := shippedConpty(filepath.Dir(exe)); api != nil {
		m["shipped"] = api
	} else {
		t.Log("no conpty.dll + OpenConsole.exe next to the test binary: system ConPTY only")
	}
	return m
}

func startChild(t *testing.T, api *conptyAPI, mode string, cols, rows uint16) *console {
	t.Helper()
	exe, _ := os.Executable()
	env := append(os.Environ(), "PTY_TEST_CHILD="+mode, "PTY_TEST_VAR=v1")
	c, err := start(api, []string{exe}, os.TempDir(), env, cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// waitFor reads c until its output contains s; it returns the output.
func waitFor(t *testing.T, c Console, s string) string {
	t.Helper()
	var out []byte
	buf := make([]byte, 1024)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	for !strings.Contains(string(out), s) {
		n, err := c.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			t.Fatalf("waiting for %q: %v (output %q)", s, err, out)
		}
	}
	return string(out)
}

var childRE = regexp.MustCompile(`child (\d+)\r`)

// waitChild reads c until the "tree" child has said its child's pid.
func waitChild(t *testing.T, c Console) int {
	t.Helper()
	out := waitFor(t, c, "child ")
	for !childRE.MatchString(out) {
		out += waitFor(t, c, "\r")
	}
	kid, _ := strconv.Atoi(childRE.FindStringSubmatch(out)[1])
	return kid
}

func TestStart(t *testing.T) {
	for name, api := range apis(t) {
		t.Run(name, func(t *testing.T) {
			c := startChild(t, api, "size", 77, 13)
			defer c.Close()
			out, _ := io.ReadAll(c) // ends once the console has no process left (ReleasePseudoConsole)
			err := c.Wait()
			if !strings.Contains(string(out), "size 77x13 env v1") {
				t.Fatalf("output %q", out)
			}
			if err == nil || err.Error() != "exit status 3" {
				t.Fatalf("Wait = %v, want exit status 3", err)
			}
			exe, _ := os.Executable()
			if strings.Contains(strings.ToLower(string(out)), strings.ToLower(exe)) {
				t.Fatalf("ConPTY's exe-path title got through: %q", out)
			}
		})
	}
	if _, err := Start(nil, "", nil, 80, 24); err == nil {
		t.Fatal("Start(nil) succeeded")
	}
	if _, err := Start([]string{`C:\no\such.exe`}, "", nil, 80, 24); err == nil {
		t.Fatal("Start(no such exe) succeeded")
	}
}

func TestTitle(t *testing.T) {
	for name, api := range apis(t) {
		t.Run(name, func(t *testing.T) {
			c := startChild(t, api, "title", 80, 24)
			defer c.Close()
			out := waitFor(t, c, "titled")
			if !strings.Contains(out, "\x1b]0;mine\x07") && !strings.Contains(out, "\x1b]2;mine\x07") {
				t.Fatalf("the program's title is missing: %q", out)
			}
			go io.Copy(io.Discard, c)
			c.Wait()
		})
	}
}

func TestResizeAndDeadline(t *testing.T) {
	for name, api := range apis(t) {
		t.Run(name, func(t *testing.T) {
			c := startChild(t, api, "resize", 80, 24)
			defer func() { c.Stop(0); c.Close(); c.Wait() }()
			waitFor(t, c, "ready")
			c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			if _, err := c.Read(make([]byte, 64)); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("Read past the deadline = %v", err)
			}
			if err := c.Resize(100, 30); err != nil {
				t.Fatal(err)
			}
			c.Write([]byte("\r"))
			waitFor(t, c, "size 100x30")
		})
	}
}

func TestForeground(t *testing.T) {
	c := startChild(t, conpty(), "tree", 80, 24)
	waited := make(chan struct{})
	go func() { c.Wait(); close(waited) }()
	defer func() { c.Stop(0); c.Close(); <-waited }()
	kid := waitChild(t, c)
	if fg, err := c.Foreground(); err != nil || fg != kid {
		t.Fatalf("Foreground = %d, %v; want the child %d (shell %d)", fg, err, kid, c.PID())
	}

	// Stop and Close, as a session does them, end the whole tree: the
	// console's close may not reach a child in time (seen under x64
	// emulation), the job's does.
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(kid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	go io.Copy(io.Discard, c)
	c.Stop(5 * time.Second)
	c.Close()
	if ev, _ := windows.WaitForSingleObject(h, 5000); ev != windows.WAIT_OBJECT_0 {
		t.Fatal("the child outlived Stop and Close")
	}

	// A shell without a command is its own foreground.
	c2 := startChild(t, conpty(), "sleep", 80, 24)
	defer func() { c2.Stop(0); c2.Close(); c2.Wait() }()
	waitFor(t, c2, "ready")
	if fg, err := c2.Foreground(); err != nil || fg != c2.PID() {
		t.Fatalf("Foreground = %d, %v; want the shell %d", fg, err, c2.PID())
	}
}

func TestStop(t *testing.T) {
	for name, api := range apis(t) {
		t.Run(name, func(t *testing.T) {
			// Closing the console ends a plain program.
			c := startChild(t, api, "sleep", 80, 24)
			waitFor(t, c, "ready")
			go c.Wait()
			go io.Copy(io.Discard, c)
			if err := c.Stop(5 * time.Second); err != nil {
				t.Fatalf("Stop = %v, want a clean close", err)
			}
			c.Close()

			// One that holds on is terminated after grace.
			c = startChild(t, api, "stubborn", 80, 24)
			waitFor(t, c, "ready")
			waited := make(chan error, 1)
			go func() { waited <- c.Wait() }()
			go io.Copy(io.Discard, c)
			start := time.Now()
			if err := c.Stop(200 * time.Millisecond); err == nil {
				t.Fatal("Stop = nil, want the termination reported")
			}
			if d := time.Since(start); d < 200*time.Millisecond {
				t.Fatalf("terminated after %v, before grace", d)
			}
			select {
			case err := <-waited:
				if err == nil {
					t.Fatal("Wait = nil, want an exit status")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still running after TerminateJobObject")
			}
			c.Close()
		})
	}
}

func TestCloseEndsTree(t *testing.T) {
	c := startChild(t, conpty(), "tree", 80, 24)
	kid := waitChild(t, c)
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(kid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	read := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 1024))
		for err == nil {
			_, err = c.Read(make([]byte, 1024))
		}
		read <- err
	}()
	c.Close()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not interrupt Read")
	}
	if ev, _ := windows.WaitForSingleObject(h, 5000); ev != windows.WAIT_OBJECT_0 {
		t.Fatal("the child outlived Close")
	}
	c.Wait()
}

func TestFilter(t *testing.T) {
	exe := `C:\Program Files\PowerShell\7\pwsh.exe`
	in := "\x1b[?9001h\x1b[?1004h\x1b]0;c:\\program files\\powershell\\7\\PWSH.EXE\x07PS> " +
		"\x1b]0;mine\x07\x1b]2;" + exe + "\x1b\\\x1b[1mbold\x1b[0m\x1b]0;" + exe + "x\x07end\x1b"
	want := "\x1b[?9001h\x1b[?1004hPS> \x1b]0;mine\x07\x1b[1mbold\x1b[0m\x1b]0;" + exe + "x\x07end\x1b"
	// Whole, and split at every byte.
	for _, step := range []int{len(in), 1, 3} {
		answered := 0
		f := newFilter(exe, func() { answered++ })
		var out []byte
		for i := 0; i < len(in); i += step {
			out = f.feed(out, []byte(in[i:min(i+step, len(in))]))
		}
		out = f.flush(out)
		if string(out) != want {
			t.Errorf("step %d: got  %q\nwant %q", step, out, want)
		}
		if answered != 1 {
			t.Errorf("step %d: focus answered %d times, want once", step, answered)
		}
	}
}

func TestEnvBlock(t *testing.T) {
	b, err := envBlock([]string{"A=1", "=C:=C:\\x", "Path=a", "PATH=b", "SYSTEMROOT=r"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(string(utf16ToRunes(b)), "\x00")
	want := []string{"A=1", "=C:=C:\\x", "PATH=b", "SYSTEMROOT=r", "", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("block %q, want %q", got, want)
	}
	if _, err := envBlock([]string{"A=\x00"}); err == nil {
		t.Fatal("NUL accepted")
	}
}

func utf16ToRunes(b []uint16) []rune {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return r
}
