Azure DevOps answers as `az` prints them (`--output json`), for the
codehost tests. Shapes follow the REST reference (GitPullRequest,
PolicyEvaluationRecord, GitPullRequestStatus, api-version 7.1) and the
azure-devops extension's msrest serialisation (dates with +00:00, nulls
kept). Names and ids are made up. They are assumed, not captured: the
live-org task replaces them with sanitised captures.

- pr-active.json: `az repos pr show --id 12`, active, one required
  reviewer who hasn't voted, merge status succeeded.
- pr-list.json: `az repos pr list --source-branch refs/heads/feature/login
  --status all`: an abandoned PR 9, the active 12 (as above, shortened)
  and a completed 3.
- pr-completed.json: `az repos pr show --id 12` after a squash merge.
- policies.json: `az repos pr policy list --id 12`: a blocking build
  rejected, an optional build queued, a minimum-reviewers policy
  queued, a comment requirement, a blocking status policy approved.
- statuses.json: `az devops invoke --area git --resource
  pullRequestStatuses ...`: two statuses of one context (the later one
  wins), one covered by the status policy, one pending.
