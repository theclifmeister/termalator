package codehost

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// azLogLines is how many lines of a task's log are asked for: the last
// ones, where the error is.
const azLogLines = 2000

// FailedLog is the first failing task of a failed build of the PR's
// validation and an excerpt of its log (azureExcerpt), "" when there is
// none or Azure DevOps can't give it: no PR build, no failed task, the
// call failed. Builds are the PR's failed builds of its merge ref,
// newest first, at most logRuns of them.
func (a Azure) FailedLog(repo string, pr PR) (job, text string) {
	if pr.Number <= 0 || a.Target.OrgURL == "" || a.Target.Project == "" {
		return "", ""
	}
	for _, id := range a.failedBuilds(repo, pr.Number) {
		if job, ex := a.buildLog(repo, id); ex != "" {
			return job, ex
		}
	}
	return "", ""
}

// failedBuilds are the ids of the PR's failed builds: those of
// refs/pull/<n>/merge, where Azure DevOps builds a PR (a build
// validation policy's and any other), newest first, at most logRuns.
func (a Azure) failedBuilds(repo string, n int) []int {
	out, err := a.get(repo, fmt.Sprintf("%s/_apis/build/builds?branchName=refs/pull/%d/merge&resultFilter=failed&queryOrder=queueTimeDescending&$top=%d&api-version=7.1",
		a.projectURL(), n, logRuns))
	if err != nil {
		return nil
	}
	var ids []int
	for _, raw := range azValues(out) {
		var b struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(raw, &b) == nil && b.ID > 0 && len(ids) < logRuns {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

// azRecord is one record of a build's timeline.
type azRecord struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Order    int    `json:"order"`
	Result   string `json:"result"`
	Log      *struct {
		ID int `json:"id"`
	} `json:"log"`
}

// firstFailedTask picks the failed Task record with a log that comes
// first (the stage, phase and job rollups fail too but have no log of
// their own), and answers its name, with its job's in front when it has
// one, and its log id.
func firstFailedTask(timeline []byte) (name string, logID int) {
	var tl struct {
		Records []azRecord `json:"records"`
	}
	if json.Unmarshal(timeline, &tl) != nil {
		return "", 0
	}
	byID := map[string]azRecord{}
	for _, r := range tl.Records {
		byID[r.ID] = r
	}
	var best *azRecord
	for i, r := range tl.Records {
		if r.Type != "Task" || r.Result != "failed" || r.Log == nil || r.Log.ID <= 0 {
			continue
		}
		if best == nil || r.Order < best.Order {
			best = &tl.Records[i]
		}
	}
	if best == nil {
		return "", 0
	}
	name = best.Name
	for p, ok := byID[best.ParentID]; ok; p, ok = byID[p.ParentID] {
		if p.Type == "Job" {
			name = p.Name + " / " + best.Name
			break
		}
	}
	return name, best.Log.ID
}

// buildURL is the REST URL of build id's resource ("timeline", "logs",
// "logs/7").
func (a Azure) buildURL(id int, resource string) string {
	return fmt.Sprintf("%s/_apis/build/builds/%d/%s", a.projectURL(), id, resource)
}

// buildLog is the excerpt of the first failed task of build id.
func (a Azure) buildLog(repo string, id int) (job, text string) {
	tl, err := a.get(repo, a.buildURL(id, "timeline")+"?api-version=7.1")
	if err != nil {
		return "", ""
	}
	name, logID := firstFailedTask(tl)
	if logID <= 0 {
		return "", ""
	}
	// the last azLogLines lines: the log's line count is on its entry
	start := 1
	if out, err := a.get(repo, a.buildURL(id, "logs")+"?api-version=7.1"); err == nil {
		var logs struct {
			Value []struct {
				ID        int `json:"id"`
				LineCount int `json:"lineCount"`
			} `json:"value"`
		}
		json.Unmarshal(out, &logs)
		for _, e := range logs.Value {
			if e.ID == logID && e.LineCount > azLogLines {
				start = e.LineCount - azLogLines + 1
			}
		}
	}
	out, err := a.get(repo, fmt.Sprintf("%s?startLine=%d&endLine=%d&api-version=7.1", a.buildURL(id, "logs/"+strconv.Itoa(logID)), start, start+azLogLines-1))
	if err != nil {
		return "", ""
	}
	return azureExcerpt(name, azLogText(out))
}

// azLogText is the log text of a log's answer: JSON
// {"count":n,"value":[lines]} as asked for, a JSON string, or the raw
// text.
func azLogText(out []byte) string {
	var obj struct {
		Value []string `json:"value"`
	}
	if json.Unmarshal(out, &obj) == nil && obj.Value != nil {
		return strings.Join(obj.Value, "\n")
	}
	var s string
	if json.Unmarshal(out, &s) == nil {
		return s
	}
	return string(out)
}
