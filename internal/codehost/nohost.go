package codehost

// NoHost is the Host of a repo with no PR host (NoKind): it has nothing
// to ask over the network, so the ticker's polls skip it (PR gives
// ErrNoRef, as for a thread with nothing to look up) and no call of it
// counts as a failed one. MergeCommit and MergedPR read git alone, which
// works without a remote.
type NoHost struct{}

func (NoHost) Kind() string                                { return NoKind }
func (NoHost) PR(string, Ref) (PR, error)                  { return PR{}, ErrNoRef }
func (NoHost) FailedLog(string, PR) (job, log string)      { return "", "" }
func (NoHost) PRState(string, string) string               { return "" }
func (NoHost) PRHead(string, string) (string, int, string) { return "", 0, "" }
func (NoHost) MergeCommit(repo string, n int) string       { return GitHub{}.MergeCommit(repo, n) }
func (NoHost) MergedPR(repo, commit string) int            { return GitHub{}.MergedPR(repo, commit) }
func (NoHost) Hints(int) Hints                             { return Hints{} }
func (NoHost) Doctor(DoctorDeps) []Check                   { return nil }
func (NoHost) ParsePRURL(string) (int, bool)               { return 0, false }
