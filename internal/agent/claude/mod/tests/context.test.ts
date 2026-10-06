import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'

import { withBlock } from '../hooks/context'

const SOCKET = '/run/s/s-7/mod.sock'
const RULES = 'tm skill thread v0.9.0\n\nYou are one thread.\n'
const FRESH = RULES + '\nYou are thread t-1 of project demo.\nPR: #7 open, checks pass\n'

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

test('a SessionStart brings no copy: the block is the server\'s alone', async ($, on) => {
  serve(on, { status: 200, text: FRESH })
  on('classic.SessionStart', async () => ({ additionalContext: ['terminatr: see the context block'] }))
  const started = await $.classic.SessionStart({ source: 'clear' })
  expect(started.additionalContext).toEqual(['terminatr: see the context block'])
  const r = await $.prompt.context({ blocks: [] })
  expect(r.blocks).toEqual([{ name: 'currentDate', text: 'today' }, { name: 'terminatr', text: FRESH }])
})

test('a server that does not answer leaves no block', async ($, on) => {
  serve(on, 'down')
  const r = await $.prompt.context({ blocks: [] })
  expect(r.blocks).toEqual([{ name: 'currentDate', text: 'today' }])
})
