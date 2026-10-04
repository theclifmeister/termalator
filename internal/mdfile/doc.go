// Package mdfile reads and writes markdown files with TOML front matter
// (between +++ lines), using a lock file and atomic rename for every write
// (docs/SPEC.md §5.1).
//
// Locks are advisory flock(2) locks on a hidden sibling ".<file>.lock",
// so every writer of a file must go through Lock, Update, Write or Append.
// Readers don't lock: a rename is atomic, so a reader sees the old or the
// new file.
package mdfile
