// Package version holds build information set by the linker.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

// Version and LibGhostty are overridden at build time by the Makefile:
//
//	go build -ldflags "-X github.com/theclifmeister/termalator/internal/version.Version=v0.1.0"
var (
	Version    = "dev"
	LibGhostty = "unknown" // the Ghostty commit libghostty-vt was built from
)

var (
	buildOnce sync.Once
	buildID   string
)

// BuildID identifies this exact binary: version, libghostty commit and a
// hash of the executable. Attach requires client and server to have the
// same BuildID, because libghostty's snapshot format carries no
// compatibility guarantee between builds (docs/SPEC.md §3.3).
func BuildID() string {
	buildOnce.Do(func() {
		buildID = Version + "+" + LibGhostty + "+" + exeHash()
	})
	return buildID
}

func exeHash() string {
	path, err := os.Executable()
	if err != nil {
		return "nohash"
	}
	f, err := os.Open(path)
	if err != nil {
		return "nohash"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "nohash"
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
