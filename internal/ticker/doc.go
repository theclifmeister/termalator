// Package ticker is the server's event loop and sweep (docs/SPEC.md
// §7.5, M7). It turns thread state, report and PR changes into inbox
// items, nudges an idle coordinator about new items, follows up on a
// thread's failing PR checks, and resolves a thread once its PR merged.
//
// Inbox items and prompts are built from fixed text and ids only: report
// bodies, PR titles and comments never reach an item or an injected
// prompt, because data is not instructions (§11.2).
package ticker
