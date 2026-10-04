// Package proto defines the wire protocol between the server, the TUI
// client and the CLI: the hello handshake, control NDJSON requests and the
// attach stream framing (docs/SPEC.md §3.3).
//
// Every connection starts with one Hello line from each side; both sides
// run Check. A control connection then exchanges Request/Response lines.
// An attach connection sends one AttachRequest line, gets one AttachReply
// line, and from then on both sides speak binary frames.
package proto
