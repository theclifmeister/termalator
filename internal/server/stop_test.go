package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/proto"
)

// TestStopOlderProtocol: a server that claims an older protocol refuses
// this tm's hello, and Stop still stops it by redialling at its protocol.
func TestStopOlderProtocol(t *testing.T) {
	t.Setenv(testHelloEnv, "protocol=1")
	p := testPaths(t)
	r := startServer(t, p)
	var mm *proto.MismatchError
	if _, err := Dial(p, proto.KindControl); !errors.As(err, &mm) || mm.Server.Protocol != 1 || !strings.Contains(err.Error(), "tm server restart") {
		t.Fatalf("Dial: %v", err)
	}
	st, err := Stop(p, true)
	if err != nil || st.Signalled || st.Protocol != 1 {
		t.Fatalf("Stop: %+v %v", st, err)
	}
	select {
	case <-r.done:
		r.done <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not stop the server")
	}
	if _, err := Stop(p, true); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestStopNeverSignalsAnUnrelatedProcess: a server that can't be asked
// (deaf) gets SIGTERM only when its pid runs `tm server run`; here the
// pid is the test binary, so Stop refuses.
func TestStopNeverSignalsAnUnrelatedProcess(t *testing.T) {
	t.Setenv(testHelloEnv, "deaf")
	p := testPaths(t)
	startServer(t, p)
	_, err := Stop(p, true)
	if err == nil || !strings.Contains(err.Error(), "not a tm server") {
		t.Fatalf("Stop: %v", err)
	}
}

func TestIsServerArgv(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want bool
	}{
		{[]string{"/opt/homebrew/bin/tm", "server", "run", "--detached"}, true},
		{[]string{"/Users/x/.terminatr/bin/tm-abc", "server", "run"}, true},
		{[]string{"tm", "server", "stop"}, false},
		{[]string{"server", "run"}, false}, // argv[0] is the program
		{[]string{"vim", "notes", "server", "run"}, false},
		{nil, false},
	} {
		if got := isServerArgv(c.argv); got != c.want {
			t.Errorf("%q: %v, want %v", c.argv, got, c.want)
		}
	}
}
