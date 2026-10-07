import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import { commands, judge, rulesOf } from '../hooks/guard'
import type { Rules } from '../hooks/guard'

const SOCKET = '/run/s/s-7/mod.sock'
const WT = '/h/.terminatr/worktrees/demo/t-0001-fix'

const all = ['force-push', 'push-default', 'worktree-only', 'delete-branch', 'merge', 'credentials']
const thread: Rules = {
  on: true, role: 'thread', rules: all, home: '/h', cwd: WT,
  writable: [WT, '/tmp', '/private/tmp'], worktrees: '/h/.terminatr/worktrees',
  protected: ['main', 'master'], secrets: ['/h/.ssh', '/h/.aws', '/h/.config/gh'],
}
const coordinator: Rules = { ...thread, role: 'coordinator', cwd: '/h/.terminatr/projects/demo', rules: ['force-push', 'push-default', 'delete-branch', 'credentials'], writable: undefined }

const bash = (r: Rules, command: string) => judge(r, 'Bash', { command })?.rule ?? null

test('commands splits a command line shell-style', () => {
  expect(commands(`cd x && git push -f origin 'my branch'; echo "a $(gh pr merge 3) b" | cat`)).toEqual([
    ['cd', 'x'], ['git', 'push', '-f', 'origin', 'my branch'], ['gh', 'pr', 'merge', '3'], ['echo', 'a $(gh pr merge 3) b'], ['cat'],
  ])
  expect(commands('cat < ~/.ssh/id_rsa')).toEqual([['cat', '<', '~/.ssh/id_rsa']])
})

test('pushes: force and the default branch are refused, the thread\'s own branch passes', () => {
  for (const c of ['git push -f', 'git push --force origin tm/x', 'git push --force-with-lease', 'git push -uf origin tm/x', 'git push origin +tm/x', 'git -C /w push --force', 'FOO=1 git push --force', 'sudo git push --mirror']) {
    expect([c, bash(thread, c)]).toEqual([c, 'force-push'])
  }
  for (const c of ['git push origin main', 'git push origin HEAD:main', 'git push origin HEAD:refs/heads/master', 'git push --all origin']) {
    expect([c, bash(thread, c)]).toEqual([c, 'push-default'])
  }
  for (const c of ['git push', 'git push -u origin tm/x', 'git push origin HEAD', 'git push origin main:tm/x', 'git status && git push origin tm/x']) {
    expect([c, bash(thread, c)]).toEqual([c, null])
  }
  expect(judge(thread, 'Bash', { command: 'git push origin main' })?.message).toContain('nothing is pushed straight to main')
})

test('deleting branches and worktrees is refused', () => {
  for (const c of ['git branch -D tm/x', 'git branch -d tm/x', 'git branch --delete tm/x', 'git push origin --delete tm/x', 'git push origin :tm/x', 'git worktree remove ../x', 'git worktree prune', 'gh repo delete o/r', 'rm -rf .', `rm -rf ${WT}`, 'rm -r /h/.terminatr/worktrees/demo', 'gh api -X DELETE repos/o/r/git/refs/heads/x']) {
    expect([c, bash(thread, c)]).toEqual([c, 'delete-branch'])
  }
  for (const c of ['git branch -a', 'git branch tm/new', 'rm -rf build', 'rm -f /h/.terminatr/worktrees/demo/t-0001-fix/x.txt', 'git worktree list']) {
    expect([c, bash(thread, c)]).toEqual([c, null])
  }
})

test('gh pr merge: refused for a thread under merge = "coordinator", the coordinator may', () => {
  expect(bash(thread, 'gh pr merge 42 --merge')).toBe('merge')
  expect(bash(thread, 'gh api -X PUT repos/o/r/pulls/42/merge')).toBe('merge')
  expect(judge(thread, 'Bash', { command: 'gh pr merge 42' })?.message).toBe(
    'terminatr guard (merge): merging is the coordinator\'s job in this project (merge = "coordinator"). Leave the PR open with CI green and say so in your report.',
  )
  expect(bash(coordinator, 'gh pr merge 42 --merge')).toBe(null)
  expect(bash({ ...thread, rules: all.filter(r => r !== 'merge') }, 'gh pr merge 42')).toBe(null)
  expect(bash(thread, 'gh pr view 42 && gh pr checks 42')).toBe(null)
})

test('az: completing a PR, deleting branches and reading tokens are refused', () => {
  const org = '--org=https://dev.azure.com/o'
  for (const c of [
    'az repos pr update --id 7 --status completed', 'az repos pr update --id 7 --status=completed', 'az repos pr update --id 7 --auto-complete true',
    'az repos pr update --id 7 --auto-complete', 'az repos pr update --id 7 --bypass-policy true', `az repos pr update ${org} --id 7 --status completed`,
    'az repos pr create --auto-complete true', 'az --only-show-errors repos pr update --id 7 --status completed', 'az -o json repos pr update --id 7 --status Completed',
    'az rest --method patch --url https://dev.azure.com/o/p/_apis/git/repositories/r/pullrequests/7?api-version=7.1 --body @b.json',
    'az rest -m PATCH -u https://dev.azure.com/o/p/_apis/git/repositories/r/pullRequests/7', 'az rest --method post --url https://dev.azure.com/o/p/_apis/git/repositories/r/merges',
    'az devops invoke --area git --resource pullRequests --http-method PATCH --route-parameters project=p repositoryId=r pullRequestId=7',
    'curl -X PATCH https://dev.azure.com/o/p/_apis/git/repositories/r/pullrequests/7 -d @b.json',
  ]) {
    expect([c, bash(thread, c)]).toEqual([c, 'merge'])
  }
  for (const c of [
    'az repos delete --id r --yes', 'az repos ref delete --name refs/heads/x --object-id abc', 'az repos pr update --id 7 --delete-source-branch true', 'az repos pr create --delete-source-branch true',
    'az rest --method delete --url https://dev.azure.com/o/p/_apis/git/repositories/r', 'az rest --method post --url https://dev.azure.com/o/p/_apis/git/repositories/r/refs --body @zero.json',
    'az devops invoke --area git --resource refs --http-method POST --route-parameters project=p repositoryId=r --in-file z.json',
    'curl --request DELETE https://dev.azure.com/o/p/_apis/git/repositories/r', 'curl -XPOST https://dev.azure.com/o/p/_apis/git/repositories/r/refs',
  ]) {
    expect([c, bash(thread, c)]).toEqual([c, 'delete-branch'])
  }
  for (const c of [
    'az account get-access-token', 'az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798', 'az devops login --organization https://dev.azure.com/o',
    'echo $AZURE_DEVOPS_EXT_PAT', 'echo "pat: ${AZURE_DEVOPS_EXT_PAT}"', 'printenv AZURE_DEVOPS_EXT_PAT', 'curl -u :$AZURE_DEVOPS_EXT_PAT https://dev.azure.com/o/_apis/projects',
    'curl -H "Authorization: Basic abc" https://dev.azure.com/o/_apis/projects', 'curl --user me:pat https://o.visualstudio.com/_apis/projects',
  ]) {
    expect([c, bash(thread, c)]).toEqual([c, 'credentials'])
  }
  for (const c of [
    'az repos pr list --status completed', 'az repos pr show --id 7', 'az repos pr create --title t --source-branch tm/x --target-branch main', 'az repos pr create --auto-complete false --delete-source-branch false',
    'az repos pr update --id 7 --description d', 'az repos pr update --id 7 --status abandoned', 'az repos pr set-vote --id 7 --vote approve', 'az repos pr list --status active', 'az repos list', 'az repos ref list --filter heads/tm',
    'az account show', 'az devops configure --defaults organization=https://dev.azure.com/o', 'az pipelines runs list',
    'az rest --method get --url https://dev.azure.com/o/p/_apis/git/repositories/r/pullrequests/7',
    'az rest --method post --url https://dev.azure.com/o/p/_apis/git/repositories/r/pullrequests --body @pr.json',
    'az rest --method post --url https://dev.azure.com/o/p/_apis/git/repositories/r/pullrequests/7/threads --body @c.json',
    'az devops invoke --area git --resource pullRequests --route-parameters project=p repositoryId=r pullRequestId=7',
    'az devops invoke --area git --resource pullRequestThreads --http-method POST --route-parameters pullRequestId=7',
    'curl https://dev.azure.com/o/p/_apis/git/repositories/r/pullrequests/7', 'printenv AZURE_CONFIG_DIR', 'echo $HOME',
  ]) {
    expect([c, bash(thread, c)]).toEqual([c, null])
  }
  expect(bash(coordinator, 'az repos pr update --id 7 --status completed')).toBe(null)
  expect(bash(coordinator, 'az repos delete --id r')).toBe('delete-branch')
  expect(judge(thread, 'Bash', { command: 'az repos pr update --id 7 --auto-complete true' })?.message).toBe(judge(thread, 'Bash', { command: 'gh pr merge 42' })?.message)
})

test('a thread writes only in its worktree and the temporary folders', () => {
  expect(judge(thread, 'Edit', { file_path: `${WT}/main.go` })).toBe(null)
  expect(judge(thread, 'Write', { file_path: '/tmp/claude-501/x/notes.md' })).toBe(null)
  expect(judge(thread, 'Write', { file_path: 'docs/a.md' })).toBe(null)
  expect(judge(thread, 'Edit', { file_path: '/h/.terminatr/projects/demo/PROJECT.md' })?.rule).toBe('worktree-only')
  expect(judge(thread, 'Write', { file_path: `${WT}/../other/x` })?.rule).toBe('worktree-only')
  expect(judge(thread, 'NotebookEdit', { notebook_path: '/h/n.ipynb' })?.rule).toBe('worktree-only')
  expect(judge(coordinator, 'Edit', { file_path: '/h/.terminatr/projects/demo/TASKS.md' })).toBe(null)
})

test('credentials are not read', () => {
  expect(judge(thread, 'Read', { file_path: '/h/.ssh/id_ed25519' })?.rule).toBe('credentials')
  expect(judge(coordinator, 'Grep', { pattern: 'token', path: '/h/.config/gh' })?.rule).toBe('credentials')
  expect(judge(thread, 'Glob', { pattern: '/h/.aws/*' })?.rule).toBe('credentials')
  expect(judge(thread, 'Read', { file_path: `${WT}/README.md` })).toBe(null)
  for (const c of ['cat ~/.ssh/id_rsa', 'base64 < $HOME/.aws/credentials', 'gh auth token', 'gh auth status --show-token', 'security find-generic-password -s x -w', 'printenv', 'printenv -0', 'printenv PATH GITHUB_TOKEN', 'printenv aws_secret_access_key', 'printenv MY_PASSWORD', 'env', 'env | grep TOKEN', 'git credential fill']) {
    expect([c, bash(thread, c)]).toEqual([c, 'credentials'])
  }
  for (const c of ['gh auth status', 'cat README.md', 'env GOOS=linux go build ./...', 'printenv HOME', 'printenv HOME PATH TERM', 'printenv -0 SHELL', 'ssh -T git@github.com']) {
    expect([c, bash(thread, c)]).toEqual([c, null])
  }
})

test('the guard off refuses nothing', () => {
  expect(judge(rulesOf('{"on":false}'), 'Bash', { command: 'git push -f origin main' })).toBe(null)
  expect(rulesOf('not json').on).toBe(false)
  expect(rulesOf(JSON.stringify(thread)).rules).toEqual(all)
})

// start runs session.start in a terminatr session whose server answers
// the rules with rules, and keeps what the mod reports refused.
async function start($: Engine, on: On, rules: Rules | null, env: Record<string, string> = { TERMINATR_MOD_SOCKET: SOCKET }) {
  const clock = mock.clock(on)
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7', ...env })
  const denied: { rule: string; tool: string; summary: string }[] = []
  let fetches = 0
  on('http.fetch', async (_$, e) => {
    const ok = (status: number, text = '') => ({ value: { status, ok: status < 300, headers: {}, text } })
    if (e.url.endsWith('/v1/rules')) {
      fetches++
      return rules ? ok(200, JSON.stringify(rules)) : ok(500, 'down')
    }
    if (e.url.endsWith('/v1/denied')) {
      denied.push(JSON.parse(e.init?.body ?? '{}'))
      return ok(204)
    }
    return ok(204)
  })
  on('process.spawn', async function* () {
    return { value: { code: 0, signal: null } }
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))
  on('tool.call', { tool: 'Bash' }, async () => ({ result: { stdout: 'ran', stderr: '', interrupted: false } }))
  on('tool.check', async () => ({ decision: 'allow' as const }))
  await $.session.start({ cwd: WT, surface: 'terminal', isInteractive: true })
  await clock.settle()
  return { clock, denied, fetches: () => fetches }
}

test('a refused call returns the rule\'s sentence and is reported; others run', async ($, on) => {
  const { clock, denied } = await start($, on, thread)
  const r = await $.tool.call({ tool: 'Bash', command: 'git push --force origin tm/x' })
  expect((r as { deny?: string }).deny).toContain('terminatr guard (force-push)')
  await clock.settle()
  expect(denied).toEqual([{ rule: 'force-push', tool: 'Bash', summary: 'git push with force' }])
  const ran = await $.tool.call({ tool: 'Bash', command: 'git push -u origin tm/x' })
  expect((ran as { deny?: string }).deny).toBe(undefined)
  expect((ran as { result: { stdout: string } }).result.stdout).toBe('ran')
})

test('$.tool.check gets the same answer', async ($, on) => {
  await start($, on, thread)
  expect((await $.tool.check({ tool: 'Bash', input: { command: 'gh pr merge 1' } })).decision).toBe('deny')
  expect((await $.tool.check({ tool: 'Bash', input: { command: 'gh pr view 1' } })).decision).toBe('allow')
})

test('without the mod socket, or when the server can\'t say, the guard is off', async ($, on) => {
  await start($, on, thread, {})
  const r = await $.tool.call({ tool: 'Bash', command: 'git push --force' })
  expect((r as { deny?: string }).deny).toBe(undefined)
})

test('rules the server couldn\'t give are asked for again at the next call', async ($, on) => {
  const { fetches } = await start($, on, null)
  for (let i = 0; i < 2; i++) {
    const r = await $.tool.call({ tool: 'Bash', command: 'git push --force' })
    expect((r as { deny?: string }).deny).toBe(undefined)
  }
  expect(fetches()).toBe(2)
})
