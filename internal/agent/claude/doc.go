// Package claude holds the Go parts of the Claude Code reference agent that
// its manifest (../manifests/claude.toml) cannot express: the structured
// prompt channel over Claude's undocumented uds-messaging socket
// (docs/SPEC.md §8.6). Everything else about Claude is data.
//
// Import it for its side effect (agent.RegisterGo); only cmd/tm does.
package claude
