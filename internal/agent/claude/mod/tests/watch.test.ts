import { expect, mock, test } from 'claude-code/testing'

import { feed } from '../hooks/feed'

const line = (state: string, done: number) =>
  JSON.stringify({
    session: { id: 's-7', role: 'thread', state },
    task: { id: 'T49', title: 'Ship the mod', status: 'started', steps_done: done, steps_total: 4 },
    pr: '',
    needs_you: 1,
    inbox: 0,
    queued_prompts: 0,
  }) + '\n'

test('feed joins split lines, skips what is not a watch', () => {
  const a = line('working', 1)
  let got = feed('', a.slice(0, 20))
  expect(got.watches).toEqual([])
  got = feed(got.rest, a.slice(20) + 'not json\n\n' + line('idle', 2) + '{"ses')
  expect(got.watches.map(w => w.session.state)).toEqual(['working', 'idle'])
  expect(got.watches[1]?.task?.steps_done).toBe(2)
  expect(got.rest).toBe('{"ses')
})

test('session.start follows tm watch for the session', async ($, on) => {
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7' })
  let argv: readonly string[] = []
  on('process.spawn', async function* (_$, e) {
    argv = e.argv
    return { value: { code: 0, signal: null } }
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))

  expect(await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })).toEqual({ cwd: '/w' })
  for (let i = 0; i < 100 && argv.length === 0; i++) await Promise.resolve()
  expect(argv).toEqual(['/opt/tm', 'watch', '--session', 's-7', '--json'])
})

test('outside a terminatr session it starts nothing', async ($, on) => {
  mock.env(on, {})
  let spawned = false
  on('process.spawn', async function* () {
    spawned = true
    return { value: { code: 0, signal: null } }
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))

  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  for (let i = 0; i < 100; i++) await Promise.resolve()
  expect(spawned).toBe(false)
})
