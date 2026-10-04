// Package version holds build information set by the linker.
package version

// Version is overridden at build time:
//
//	go build -ldflags "-X github.com/theclifmeister/termalator/internal/version.Version=v0.1.0"
var Version = "dev"
