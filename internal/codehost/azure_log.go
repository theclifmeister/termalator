package codehost

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// azLogLines is how many lines of a task's log are asked for: the last
// ones, where the error is.
const azLogLines = 2000

// FailedLog is the first failing task of a failed build of the PR's
// validation and an excerpt of its log (azureExcerpt), "" when there is
// none or az can't give it: no PR build, no failed task, az failed.
// Builds are the rejected build policies' (their context names the
// build), else the PR's failed runs by its merge ref, newest first, at
// most logRuns of them.
func (a Azure) FailedLog(repo string, pr PR) (job, text string) {
	if pr.Number <= 0 || a.Target.OrgURL == "" || a.Target.Project == "" {
		return "", ""
	}
	n := 0
	for _, id := range a.failedBuilds(repo, pr.Number) {
		if n++; n > logRuns {
			break
		}
		if job, ex := a.buildLog(repo, id); ex != "" {
			return job, ex
		}
	}
	return "", ""
}

// failedBuilds are the ids of the PR's failed builds: the context's
// buildId of each rejected or broken build policy, else (a policy
// without one, or no policy) the failed pull request runs of
// refs/pull/<n>/merge.
func (a Azure) failedBuilds(repo string, n int) []int {
	var ids []int
	if out, err := a.az(repo, append([]string{"repos", "pr", "policy", "list", "--id", strconv.Itoa(n)}, a.org()...)...); err == nil {
		var pols []struct {
			Status  string `json:"status"`
			Context *struct {
				BuildID int `json:"buildId"`
			} `json:"context"`
		}
		json.Unmarshal(out, &pols)
		for _, p := range pols {
			if (p.Status == "rejected" || p.Status == "broken") && p.Context != nil && p.Context.BuildID > 0 &&
				!slices.Contains(ids, p.Context.BuildID) {
				ids = append(ids, p.Context.BuildID)
			}
		}
	}
	if len(ids) > 0 {
		return ids
	}
	out, err := a.az(repo, append([]string{"pipelines", "runs", "list", "--project", a.Target.Project,
		"--branch", "refs/pull/" + strconv.Itoa(n) + "/merge", "--reason", "pullRequest", "--result", "failed", "--top", strconv.Itoa(logRuns)}, a.org()...)...)
	if err != nil {
		return nil
	}
	var runs []struct {
		ID int `json:"id"`
	}
	json.Unmarshal(out, &runs)
	for _, r := range runs {
		if r.ID > 0 {
			ids = append(ids, r.ID)
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

// invoke runs az devops invoke on the build area for one resource.
func (a Azure) invoke(repo, resource string, route map[string]string, query ...string) ([]byte, error) {
	args := []string{"devops", "invoke", "--area", "build", "--resource", resource, "--api-version", "7.1", "--http-method", "GET", "--route-parameters", "project=" + a.Target.Project}
	for _, k := range []string{"buildId", "logId"} {
		if v, ok := route[k]; ok {
			args = append(args, k+"="+v)
		}
	}
	if len(query) > 0 {
		args = append(args, "--query-parameters")
		args = append(args, query...)
	}
	return a.az(repo, append(args, a.org()...)...)
}

// buildLog is the excerpt of the first failed task of build id.
func (a Azure) buildLog(repo string, id int) (job, text string) {
	b := strconv.Itoa(id)
	tl, err := a.invoke(repo, "timeline", map[string]string{"buildId": b})
	if err != nil {
		return "", ""
	}
	name, logID := firstFailedTask(tl)
	if logID <= 0 {
		return "", ""
	}
	l := strconv.Itoa(logID)
	// the last azLogLines lines: the log's line count is on its entry
	start := 1
	if out, err := a.invoke(repo, "logs", map[string]string{"buildId": b}); err == nil {
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
	out, err := a.invoke(repo, "logs", map[string]string{"buildId": b, "logId": l},
		"startLine="+strconv.Itoa(start), "endLine="+strconv.Itoa(start+azLogLines-1))
	if err != nil {
		return "", ""
	}
	return azureExcerpt(name, azLogText(out))
}

// azLogText is the log text of az devops invoke's answer: a JSON
// {"count":n,"value":[lines]}, a JSON string, or the raw text.
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
