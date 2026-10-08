import { expect, test } from 'claude-code/testing'

import { tickerRows } from '../hooks/dashboard'

const now = Date.parse('2026-10-07T12:00:00Z')
const ago = (s: number) => new Date(now - s * 1000).toISOString()

test('the ticker is a row per timer, the PR host red only when it fails', () => {
  const t = { pr_checked: ago(40), synced: ago(60), pr_poll_seconds: 120, gh_failing: false }
  expect(tickerRows(t, now)).toEqual([
    { label: 'PRs', value: '40s ago · next 1m20s', bad: false },
    { label: 'synced', value: '1m ago', bad: false },
    { label: 'PR host', value: 'ok', bad: false },
  ])
  const f = tickerRows({ ...t, pr_checked: ago(180), gh_failing: true }, now)
  expect(f.map(r => [r.label, r.value, r.bad])).toEqual([
    ['PRs', '3m ago · due', false],
    ['synced', '1m ago', false],
    ['PR host', 'failing', true],
  ])
})

test('a timer not known yet has no row', () => {
  expect(tickerRows({ synced: ago(60), pr_poll_seconds: 120 } as never, now).map(r => r.label)).toEqual(['synced'])
})
