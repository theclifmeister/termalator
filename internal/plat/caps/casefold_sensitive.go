//go:build !darwin && !windows

package caps

// CaseFold: the file system ignores case in names by default (APFS,
// NTFS), so ~/.Terminatr and ~/.terminatr are the same folder and paths
// are compared with case folded.
const CaseFold = false
