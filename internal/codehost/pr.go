package codehost

import (
	"strconv"
	"strings"
	"time"
)

// PR is the fixed set of fields tm keeps of a pull request
// (docs/SPEC.md §7.5): never its title, body or comments. Each host maps
// its own answer onto these words; the JSON names are the ticker's
// state file's.
type PR struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state,omitempty"`  // OPEN, CLOSED, MERGED
	Checks string `json:"checks,omitempty"` // pass, fail, pending, or "" with no checks
	Failed int    `json:"failed,omitempty"` // failing checks
	Review string `json:"review,omitempty"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	// MergedAt is when it merged (when the ticker first saw it merged,
	// if the host doesn't say); Head is its head commit, Merge the
	// commit its merge made on the base branch.
	MergedAt time.Time `json:"merged_at,omitzero"`
	Head     string    `json:"head,omitempty"`
	Merge    string    `json:"merge,omitempty"`
	// Base is the branch it merges into; Mergeable (MERGEABLE,
	// CONFLICTING, UNKNOWN) and MergeState (BEHIND, DIRTY, CLEAN, …) are
	// the host's word on it against that branch.
	Base       string `json:"base,omitempty"`
	Mergeable  string `json:"mergeable,omitempty"`
	MergeState string `json:"merge_state,omitempty"`
}

// Ref is how items and prompts name a PR: "PR #12".
func (pr PR) Ref() string {
	if pr.Number > 0 {
		return "PR #" + strconv.Itoa(pr.Number)
	}
	return "its PR"
}

// Summary is the PR's state in one line of fixed words, as tm thread
// list and tm context show it: "#12 open, checks pass, approved",
// "#12 merged". It is "" before the ticker has seen the PR.
func (pr PR) Summary() string {
	if pr.Number <= 0 {
		return ""
	}
	num := "#" + strconv.Itoa(pr.Number)
	var parts []string
	switch pr.State {
	case "OPEN":
		parts = append(parts, num+" open")
		switch pr.Checks {
		case "pass":
			parts = append(parts, "checks pass")
		case "pending":
			parts = append(parts, "checks pending")
		case "fail":
			if pr.Failed == 1 {
				parts = append(parts, "1 check failed")
			} else {
				parts = append(parts, strconv.Itoa(pr.Failed)+" checks failed")
			}
		}
		if pr.Mergeable == "CONFLICTING" {
			parts = append(parts, "conflicts")
		}
		switch pr.Review {
		case "APPROVED":
			parts = append(parts, "approved")
		case "CHANGES_REQUESTED":
			parts = append(parts, "changes requested")
		case "REVIEW_REQUIRED":
			parts = append(parts, "review required")
		}
	case "MERGED":
		parts = append(parts, num+" merged")
	case "CLOSED":
		parts = append(parts, num+" closed")
	default:
		parts = append(parts, num)
	}
	return strings.Join(parts, ", ")
}
