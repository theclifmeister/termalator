import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import { commands, judge, rulesOf } from '../hooks/guard'
import type { Rules } from '../hooks/guard'
import vectors from './guard-vectors'

type Vectors = {
  tools: Rules['tools']
  rules: Record<string, Rules>
  cases: { rules: string; tool: string; input: Record<string, unknown>; rule: string | null; summary?: string }[]
  messages: Record<string, Record<string, string>>
}

const SOCKET = '/run/s/s-7/mod.sock'
const WT = '/h/.terminatr/worktrees/demo/t-0001-fix'

const TOOLS = (vectors as unknown as Vectors).tools
const all = ['force-push', 'push-default', 'worktree-only', 'delete-branch', 'merge', 'credentials']
const thread: Rules = {
  on: true, role: 'thread', rules: all, home: '/h', cwd: WT,
  writable: [WT, '/tmp', '/private/tmp'], worktrees: '/h/.terminatr/worktrees',
  protected: ['main', 'master'], secrets: ['/h/.ssh', '/h/.aws', '/h/.config/gh'], tools: TOOLS,
}

test('commands splits a command line shell-style', () => {
  expect(commands(`cd x && git push -f origin 'my branch'; echo "a $(gh pr merge 3) b" | cat`)).toEqual([
    ['cd', 'x'], ['git', 'push', '-f', 'origin', 'my branch'], ['gh', 'pr', 'merge', '3'], ['echo', 'a $(gh pr merge 3) b'], ['cat'],
  ])
  expect(commands('cat < ~/.ssh/id_rsa')).toEqual([['cat', '<', '~/.ssh/id_rsa']])
})

// The shared vectors (guard-vectors.ts): the Go port in the server
// (internal/server/guardmatch_test.go) passes the same cases.
test('the shared vectors: rule and summary of each call', () => {
  const v = vectors as unknown as Vectors
  for (const c of v.cases) {
    const d = judge({ ...v.rules[c.rules], tools: v.tools }, c.tool, c.input)
    expect([c.rules, c.tool, c.input, d?.rule ?? null, d?.summary]).toEqual([c.rules, c.tool, c.input, c.rule, c.summary])
  }
  for (const [role, m] of Object.entries(v.messages)) {
    const r = { ...v.rules.thread, role, tools: v.tools }
    const probe: Record<string, [string, Record<string, unknown>]> = {
      'force-push': ['Bash', { command: 'git push -f' }], 'push-default': ['Bash', { command: 'git push origin master' }],
      'worktree-only': ['Write', { file_path: '/etc/x' }], 'delete-branch': ['Bash', { command: 'git branch -D x' }],
      merge: ['Bash', { command: 'gh pr merge 1' }], credentials: ['Bash', { command: 'gh auth token' }],
    }
    for (const [rule, [tool, input]] of Object.entries(probe)) expect([role, rule, judge(r, tool, input)?.message]).toEqual([role, rule, m[rule]])
  }
})

test('a refusal names the push target and az says what gh says', () => {
  expect(judge(thread, 'Bash', { command: 'git push origin main' })?.message).toContain('nothing is pushed straight to main')
  expect(judge(thread, 'Bash', { command: 'az repos pr update --id 7 --auto-complete true' })?.message).toBe(judge(thread, 'Bash', { command: 'gh pr merge 42' })?.message)
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
