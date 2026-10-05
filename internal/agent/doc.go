// Package agent is the plug-in point for coding agents (Claude Code, Codex,
// pi, ...). The server, dashboard and coordinator logic only ever see the
// Agent interface and the harness-neutral types in this package; nothing
// outside internal/agent/<name> may mention a specific agent.
//
// Most of an agent is data: a manifest (manifests/<name>.toml, or a user
// file in ~/.termilator/agents/) declares how to recognise it, launch it,
// map its hook events to states, which screen rules cross-check those
// states, and how prompts and context get in. A manifest alone yields a
// working Agent (FromManifest). Go code is only needed for what data cannot
// express, such as a live protocol client; such agents wrap the manifest
// agent and register themselves. See docs/SPEC.md §8.
package agent
