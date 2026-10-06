import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'

import { splitContext, withBlock } from '../hooks/context'

const SOCKET = '/run/s/s-7/mod.sock'
const RULES = 'tm skill thread v0.9.0\n\nYou are one thread.\n'
const FRESH = RULES + '\nYou are thread t-1 of project demo.\nPR: #7 open, checks pass\n'

test('splitContext takes ours out, keeps the rest in order', () => {
  expect(splitContext(undefined)).toEqual({ ours: '', rest: [] })
  expect(splitContext(['user hook', RULES, 'other'])).toEqual({ ours: RULES, rest: ['user hook', 'other'] })
  expect(splitContext(['tm skill coordinator v1.0.0\n'])).toEqual({ ours: 'tm skill coordinator v1.0.0\n', rest: [] })
  expect(splitContext(['says tm skill thread v1 later'])).toEqual({ ours: '', rest: ['says tm skill thread v1 later'] })
})

test('withBlock puts ours last, once', () => {
  const date = { name: 'currentDate', text: 'today' }
  expect(withBlock([date], 'x')).toEqual([date, { name: 'terminatr', text: 'x' }])
  expect(withBlock([{ name: 'terminatr', text: 'old' }, date], 'new')).toEqual([date, { name: 'terminatr', text: 'new' }])
  expect(withBlock([date], '')).toEqual([date])
})

// serve answers GET /v1/context with status and text, or fails.
function serve(on: On, ctx: { status: number; text?: string } | 'down') {
  mock.clock(on)
  mock.env(on, { TERMINATR_MOD_SOCKET: SOCKET })
  on('http.fetch', async (_$, e) => {
    expect(e.init?.socketPath).toBe(SOCKET)
    if (!e.url.endsWith('/v1/context') || ctx === 'down') throw new Error('connection refused')
    return { value: { status: ctx.status, ok: ctx.status < 300, headers: {}, text: ctx.text ?? '' } }
  })
  on('prompt.context', async () => ({ blocks: [{ name: 'currentDate', text: 'today' }] }))
}

test('the context block is the server\'s, fresh', async ($, on) => {
  serve(on, { status: 200, text: FRESH })
  const r = await $.prompt.context({ blocks: [] })
  expect(r.blocks).toEqual([{ name: 'currentDate', text: 'today' }, { name: 'terminatr', text: FRESH }])
})

test('outside a project there is no block', async ($, on) => {
  serve(on, { status: 204 })
  const r = await $.prompt.context({ blocks: [] })
  expect(r.blocks).toEqual([{ name: 'currentDate', text: 'today' }])
})

test('SessionStart\'s copy is the block\'s fallback, and stays in its answer', async ($, on) => {
  serve(on, 'down')
  on('classic.SessionStart', async () => ({ additionalContext: ['user hook', RULES] }))
  const started = await $.classic.SessionStart({ source: 'clear' })
  expect(started.additionalContext).toEqual(['user hook', RULES])
  const r = await $.prompt.context({ blocks: [] })
  expect(r.blocks).toEqual([{ name: 'currentDate', text: 'today' }, { name: 'terminatr', text: RULES }])
})

test('a SessionStart without ours is left alone', async ($, on) => {
  serve(on, 'down')
  on('classic.SessionStart', async () => ({ additionalContext: ['user hook'] }))
  expect((await $.classic.SessionStart({ source: 'startup' })).additionalContext).toEqual(['user hook'])
  const r = await $.prompt.context({ blocks: [] })
  expect(r.blocks).toEqual([{ name: 'currentDate', text: 'today' }])
})

test('a server that does not answer falls back to the copy', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, { TERMINATR_MOD_SOCKET: SOCKET })
  on('http.fetch', () => new Promise(() => {}))
  on('prompt.context', async () => ({ blocks: [] }))
  on('classic.SessionStart', async () => ({ additionalContext: [RULES] }))
  await $.classic.SessionStart({ source: 'compact' })
  const r = $.prompt.context({ blocks: [] })
  await clock.advance(2_000)
  expect((await r).blocks).toEqual([{ name: 'terminatr', text: RULES }])
})
