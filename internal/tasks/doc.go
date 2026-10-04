// Package tasks is the task board: the Task model, the TASKS.md and
// tasks/ARCHIVE.md format, and Store, which applies the tm task commands to
// a project folder under the caller rules (docs/SPEC.md §6).
//
// Store works on the files directly, under mdfile's lock + atomic rename.
// It is the seam where the server takes over writes later: the server
// calls the same Store methods for its task.* control methods, so the CLI
// only swaps a file-backed Store for a socket client with the same API.
package tasks
