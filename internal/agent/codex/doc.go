// Package codex holds the Go part of the Codex agent that its manifest
// (../manifests/codex.toml) cannot express: the session's command hooks
// and the trust hashes that let Codex run them without asking, added to
// every launch (docs/SPEC.md §8.6). Everything else about Codex is data.
//
// Import it for its side effect (agent.RegisterGo); only cmd/tm does.
package codex
