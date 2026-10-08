package server

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/theclifmeister/terminatr/internal/plat/proc"
)

// Test hooks (docs/OPERATIONS.md, Test servers). Tests set them in the
// environment of the tm that starts a server; nothing else should.
const (
	// testHelloEnv makes the server act like another build at the
	// handshake: "protocol=N" claims protocol N (an older server), "deaf"
	// reads each hello and hangs up without answering (a server tm can't
	// talk to at all).
	testHelloEnv = "TERMINATR_TEST_HELLO"
	// testOwnerEnv names the test process that owns the server: when that
	// pid is gone, the server stops itself, so a test killed before its
	// cleanup (a timeout, ^C, SIGKILL) leaves no server behind.
	testOwnerEnv = "TERMINATR_TEST_OWNER"
)

// testHello parses testHelloEnv: the protocol to claim (0: the real one)
// and whether to hang up on every hello.
func testHello() (protocol int, deaf bool) {
	v := os.Getenv(testHelloEnv)
	if v == "deaf" {
		return 0, true
	}
	if n, ok := strings.CutPrefix(v, "protocol="); ok {
		protocol, _ = strconv.Atoi(n)
	}
	return protocol, false
}

// watchOwner stops the server once the pid in testOwnerEnv has exited.
func (s *Server) watchOwner() {
	owner, _ := strconv.Atoi(os.Getenv(testOwnerEnv))
	if owner <= 0 {
		return
	}
	go func() {
		for proc.Alive(owner) {
			select {
			case <-s.stopReq:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		s.log.Printf("stopping: test owner pid %d is gone", owner)
		s.requestStop()
	}()
}
