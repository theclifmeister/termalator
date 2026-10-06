import { expect, test } from 'claude-code/testing'

import { reportOf } from '../hooks/usage'

const turn = { model: 'm', input_tokens: 10, output_tokens: 20, cache_read_input_tokens: 30, cache_creation_input_tokens: 40 }

test('a turn reports its counts and the rise of the ledger', () => {
  const { report, now } = reportOf('t1', turn, 0.75, { usd: 0.5 })
  expect(report).toEqual({ turn: 't1', model: 'm', input: 10, output: 20, cache_read: 30, cache_creation: 40, cost_usd: 0.25, context: 0, context_window: 0 })
  expect(now).toEqual({ usd: 0.75 })
})

test('no usage, no report; a ledger that fell or is missing costs nothing', () => {
  expect(reportOf('t', undefined, 1, { usd: 0 })).toEqual({ report: null, now: { usd: 1 } })
  expect(reportOf('t', turn, 0.1, { usd: 0.5 }).report?.cost_usd).toBe(0)
  expect(reportOf('t', turn, undefined, { usd: 0.5 }).report?.cost_usd).toBe(0)
})

test('only numbers go: junk counts read as zero, the model is clipped', () => {
  const r = reportOf('t', { ...turn, input_tokens: -3, output_tokens: NaN, model: 'x'.repeat(100) }, 0, { usd: 0 }).report
  expect(r?.input).toBe(0)
  expect(r?.output).toBe(0)
  expect(r?.model.length).toBe(64)
})

test('the context is the session\'s last request, never the turn\'s summed counts', () => {
  // A turn of many requests sums far past its context: the context comes
  // from $.session.usage().context alone.
  const many = { ...turn, input_tokens: 900_000 }
  const r = reportOf('t', many, 0, { usd: 0 }, { tokens: 84_000, window: 200_000 }).report
  expect(r?.context).toBe(84_000)
  expect(r?.context_window).toBe(200_000)
  expect(reportOf('t', many, 0, { usd: 0 }, {}).report?.context).toBe(0)
})
