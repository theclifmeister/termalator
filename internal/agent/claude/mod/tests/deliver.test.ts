import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import { handled, offerOf } from '../hooks/deliver'

const SOCKET = '/run/s/s-7/mod.sock'

type Offer = { id: string; kind: 'prompt' | 'command'; text: string; command?: string; args?: string }
type Ack = { id: string; result: string; error?: string }

// What stands beneath: the server's queue (the head is offered until it
// is acked submitted or refused; a "taken" may be turned down) and
// Claude (a refused prompt, a command that throws).
type Server = {
  queue: Offer[]
  refuseTaken?: boolean
  gone?: boolean
  drop?: string
  commandFails?: boolean
  failSubmitted?: number // answer this many "submitted" acks 500
  hold?: Promise<void> // Claude takes the prompt only once this resolves
}

// start runs session.start in a terminatr session with the mod's socket,
// serving GET /v1/prompts and the acks from srv, and records what the
// mod hands Claude.
async function start($: Engine, on: On, srv: Server) {
  const clock = mock.clock(on)
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7', TERMINATR_MOD_SOCKET: SOCKET })
  const acks: Ack[] = []
  const submitted: { text: string; asUser?: boolean }[] = []
  const commands: string[] = []
  let fills = 0
  let polls = 0
  const ok = (status: number, text = '') => ({ value: { status, ok: status < 300, headers: {}, text } })
  on('http.fetch', async (_$, e) => {
    expect(e.init?.socketPath).toBe(SOCKET)
    if (e.url.endsWith('/v1/state')) return ok(204)
    if (e.url.includes('/v1/prompts?')) {
      polls++
      if (srv.gone) return ok(410, 'gone')
      const head = srv.queue[0]
      return head ? ok(200, JSON.stringify(head)) : ok(204)
    }
    const m = /\/v1\/prompts\/([^/]+)\/ack$/.exec(e.url)
    if (!m) return ok(404, 'no route')
    const a = JSON.parse(e.init?.body ?? '{}') as { result: string; error?: string }
    const id = decodeURIComponent(m[1])
    acks.push({ id, ...a })
    if (srv.queue[0]?.id !== id) return ok(404, 'not the head')
    if (a.result === 'taken' && srv.refuseTaken) return ok(404, 'pasted meanwhile')
    if (a.result === 'submitted' && srv.failSubmitted) {
      srv.failSubmitted--
      return ok(500, 'lost')
    }
    if (a.result === 'submitted' || a.result === 'refused') srv.queue.shift()
    return ok(204)
  })
  on('process.spawn', async function* () {
    return { value: { code: 0, signal: null } }
  })
  on('prompt.submit', async (_$, e) => {
    if (srv.drop) return { drop: srv.drop }
    if (srv.hold) await srv.hold
    submitted.push({ text: e.text, asUser: e.origin.kind === 'plugin' ? e.origin.asUser : undefined })
    return { text: e.text }
  })
  on('command.run', async (_$, e) => {
    if (srv.commandFails) throw new Error(`unknown command ${e.command}`)
    commands.push(`/${e.command} ${e.args}`.trim())
    return { text: '' }
  })
  on('prompt.fill', async () => {
    fills++
    return { isFilled: true }
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))
  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  await clock.settle()
  return { clock, acks, submitted, commands, fills: () => fills, polls: () => polls }
}

const acked = (acks: Ack[]) => acks.map(a => `${a.id}:${a.result}`)

test('prompts and a slash command go to Claude in order, each acked; the box is left alone', async ($, on) => {
  const srv: Server = {
    queue: [
      { id: 'g-1', kind: 'prompt', text: 'Continue with step 2' },
      { id: 'g-2', kind: 'command', text: '/remote-control tm-x', command: 'remote-control', args: 'tm-x' },
      { id: 'g-3', kind: 'prompt', text: '[tm] PR checks failed' },
    ],
  }
  const { clock, acks, submitted, commands, fills } = await start($, on, srv)
  await clock.advance(1_000)
  expect(submitted).toEqual([{ text: 'Continue with step 2', asUser: true }, { text: '[tm] PR checks failed', asUser: true }])
  expect(commands).toEqual(['/remote-control tm-x'])
  expect(acked(acks)).toEqual(['g-1:taken', 'g-1:submitted', 'g-2:taken', 'g-2:submitted', 'g-3:taken', 'g-3:submitted'])
  expect(fills()).toBe(0)
  expect(srv.queue).toEqual([])
})

test('a lost ack: offered again, the prompt it handed on is acked, not handed on again', async ($, on) => {
  const srv: Server = { queue: [{ id: 'g-1', kind: 'prompt', text: 'once' }, { id: 'g-2', kind: 'prompt', text: 'next' }], failSubmitted: 1 }
  const { clock, acks, submitted } = await start($, on, srv)
  await clock.advance(1_000)
  expect(submitted.map(s => s.text)).toEqual(['once', 'next'])
  expect(acked(acks)).toEqual(['g-1:taken', 'g-1:submitted', 'g-1:submitted', 'g-2:taken', 'g-2:submitted'])
})

test('restarted mid-queue: the new loop acks what the old one handed on, and carries on', async ($, on) => {
  let release = () => {}
  const srv: Server = {
    queue: [{ id: 'g-1', kind: 'prompt', text: 'once' }, { id: 'g-2', kind: 'prompt', text: 'next' }],
    hold: new Promise<void>(r => (release = r)),
  }
  const { clock, acks, submitted } = await start($, on, srv)
  expect(acked(acks)).toEqual(['g-1:taken'])
  // session.start again (a reload) while Claude still holds g-1.
  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  srv.hold = undefined
  await clock.advance(1_000)
  release()
  await clock.advance(1_000)
  expect(submitted.map(s => s.text)).toEqual(['next', 'once'])
  // The old loop's late ack finds g-1 gone, and the old loop stops.
  expect(acked(acks)).toEqual(['g-1:taken', 'g-1:submitted', 'g-2:taken', 'g-2:submitted', 'g-1:submitted'])
})

test("a prompt Claude drops, or a command it can't run, is acked refused for the server to paste", async ($, on) => {
  const srv: Server = { queue: [{ id: 'g-1', kind: 'prompt', text: 'hi' }], drop: 'blocked by a hook' }
  const { clock, acks } = await start($, on, srv)
  await clock.advance(1_000)
  expect(acks.slice(0, 2)).toEqual([{ id: 'g-1', result: 'taken' }, { id: 'g-1', result: 'refused', error: 'blocked by a hook' }])
  // Offered again (the paste injector had it back, then the mod), it is
  // handed on again: a refusal isn't kept as handed on.
  srv.drop = undefined
  srv.commandFails = true
  srv.queue.push({ id: 'g-1', kind: 'prompt', text: 'hi' }, { id: 'g-2', kind: 'command', text: '/nope', command: 'nope', args: '' })
  await clock.advance(1_000)
  expect(acked(acks.slice(2))).toEqual(['g-1:taken', 'g-1:submitted', 'g-2:taken', 'g-2:refused'])
  expect(acks.at(-1)?.error).toBeTruthy()
})

test('an offer the server no longer lets it take is not handed on', async ($, on) => {
  const srv: Server = { queue: [{ id: 'g-1', kind: 'prompt', text: 'late' }], refuseTaken: true }
  const { clock, submitted, acks } = await start($, on, srv)
  await clock.advance(300)
  expect(submitted).toEqual([])
  expect(acks[0]).toEqual({ id: 'g-1', result: 'taken' })
  srv.queue = []
})

test('the loop ends when the session is gone', async ($, on) => {
  const srv: Server = { queue: [], gone: true }
  const { clock, polls } = await start($, on, srv)
  await clock.advance(10_000)
  expect(polls()).toBe(1)
})

test('an offer: read from the body, and kept by its id', () => {
  expect(offerOf('{"id":"a-1","kind":"prompt","text":"hi"}')).toEqual({ id: 'a-1', kind: 'prompt', text: 'hi' })
  expect(offerOf('{"id":"a-2","kind":"command","text":"/compact","command":"compact"}')).toEqual({
    id: 'a-2', kind: 'command', text: '/compact', command: 'compact', args: '',
  })
  expect(offerOf('{"id":"a-3","kind":"command","text":"/x"}')).toBe(null)
  expect(offerOf('not json')).toBe(null)
  expect(offerOf('{"kind":"prompt","text":"no id"}')).toBe(null)
  const o = offerOf('{"id":"a-1","kind":"prompt","text":"hi"}')!
  expect(handled(o, 'a-1')).toBe(true)
  expect(handled(o, '')).toBe(false)
})
