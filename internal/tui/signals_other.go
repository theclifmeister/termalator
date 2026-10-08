//go:build !unix

package tui

import "os"

// Not yet ported: only an interrupt detaches; there is no resize or
// digest signal (plat/term will deliver both as events).

type noSignal string

func (s noSignal) Signal()        {}
func (s noSignal) String() string { return string(s) }

var (
	sigResize     os.Signal = noSignal("resize")
	sigDigest     os.Signal = noSignal("digest")
	attachSignals           = []os.Signal{os.Interrupt}
)
