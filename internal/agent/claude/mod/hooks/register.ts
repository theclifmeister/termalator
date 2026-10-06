// The terminatr mod (docs/SPEC.md §8.6, Mods). It reports the session's
// state to the server from its own events, in place of most command
// hooks: each transition, over the session's mod socket
// (TERMINATR_MOD_SOCKET, a Unix socket that speaks HTTP), and a beat
// every BEAT_MS with the state it holds; the server believes it while
// the beats come and falls back to its other sources when they stop.
// The turn lives in $.state, so a reload carries on (hooks/turn.ts).
//
// Over the same socket it takes the prompts the server queued for the
// session, one at a time and in order (hooks/deliver.ts): it long-polls
// GET /v1/prompts, acks the offer "taken", keeps its id in $.state, hands
// it to Claude ($.prompt.submit as the user's words, $.command.run for
// a slash command; Claude runs it once idle, leaving the prompt box
// alone), then acks "submitted", or "refused" so the server pastes it.
// An offer whose id is the one kept was handed on before a reload: it is
// only acked again.
//
// It also follows the session's `tm watch --json` feed for the session's
// life and keeps the latest line in $.state. From it, unless [mods] band
// is off (TERMINATR_BAND=off), it draws the band above the prompt and
// keeps a status entry under it: the thread's task, steps, current item,
// PR and what waits for the user; and it toasts when the PR's CI run
// finishes.
//
// In a thread session it gives the model the thread's tools,
// mcp__terminatr__report, __status, __steps and __done, served over the
// same socket (hooks/tools.ts): typed, checked by the server, run as the
// thread, and let through without a permission prompt.
//
// It sends each AskUserQuestion menu to the server (`tm session ask`) and
// answers it with what `tm thread answer` gave, unless the user answers
// in the pane first.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { TerminatrTurn, TerminatrWatch } from '../types'
import { answers, question } from './ask'
import { drawBand } from './band'
import { errorText, handled, offerOf } from './deliver'
import type { Ack, Offer } from './deliver'
import { feed } from './feed'
import { answer, body, toolName, TOOLS } from './tools'
import { initialTurn, stateOf, step, waitKey } from './turn'
import { initialUsage, reportOf } from './usage'
import type { TurnUsageIn, UsageReport } from './usage'
import type { Seen } from './turn'
import { ciToast, shows, statusText } from './view'

const watch = atom({ plugin: 'terminatr', key: 'watch' } as const, null)
const band = atom({ plugin: 'terminatr', key: 'band' } as const, true)
const turn = atom({ plugin: 'terminatr', key: 'turn' } as const, initialTurn)
const usage = atom({ plugin: 'terminatr', key: 'usage' } as const, initialUsage)
const delivering = atom({ plugin: 'terminatr', key: 'delivering' } as const, '')
const deliverer = atom({ plugin: 'terminatr', key: 'deliverer' } as const, 0)

// BEAT_MS is the heartbeat: the server's ModBeat (internal/agent).
const BEAT_MS = 10_000

// END_WAIT_MS bounds the wait for the exited report at session.end.
const END_WAIT_MS = 500

// POLL_S is how long the server holds a poll for a prompt; POLL_GAP_MS
// spaces polls that came back empty, RETRY_MS failed ones.
const POLL_S = 20
const POLL_GAP_MS = 250
const RETRY_MS = 5_000

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const started = await next(e)
    const bin = await $.env.get('TERMINATR_BIN')
    const id = await $.env.get('TERMINATR_SESSION')
    const isBand = (await $.env.get('TERMINATR_BAND')) !== 'off'
    await update($, band, () => isBand)
    await startReports($)
    if (socket && (await $.env.get('TERMINATR_ROLE')) === 'thread') await registerTools($)
    if (bin && id) void follow($, bin, id, isBand)
    return started
  })

  // The thread's tools: each call goes to the server, which answers what
  // the command printed or why it refused. A deny beneath (a rule of the
  // user's) stands; otherwise they run unasked, being the thread's own
  // reporting. They are listed up front, not behind ToolSearch.
  for (const t of TOOLS) {
    const tool = toolName(t)
    on('tool.call', { tool }, async ($, e) => {
      if (!socket) return { deny: `terminatr: this session has no mod socket; run tm ${t.name} in the shell` }
      try {
        const r = await $.http.fetch(`http://terminatr/v1/tools/${t.name}`, {
          method: 'POST', socketPath: socket, headers: { 'content-type': 'application/json' }, body: body(t, e),
        })
        return answer(r.status, r.text)
      } catch (err) {
        return { deny: `terminatr: the server didn't answer (${String(err)}); run tm ${t.name} in the shell` }
      }
    })
    on('tool.check', { tool }, async ($, e, next) => {
      const v = await next(e)
      return v.decision === 'deny' ? v : { decision: 'allow' }
    })
    on('tool.describe', { tool }, async ($, e, next) => ({ ...(await next(e)), isDeferred: false }))
  }

  // The menu is open while next(e) is pending; whichever answers first,
  // the user in the pane or tm, answers the call. Returning with next(e)
  // pending takes the menu down; ending the loop kills `tm session ask`,
  // which takes the question off the server.
  on('tool.call', { tool: 'AskUserQuestion' }, async ($, e, next) => {
    const bin = await $.env.get('TERMINATR_BIN')
    const id = await $.env.get('TERMINATR_SESSION')
    if (!bin || !id) return next(e)
    const menu = next(e)
    menu.catch(() => undefined)
    const ask = $.process.spawn({ argv: [bin, 'session', 'ask', id], input: question(e.tool, e.questions) })[Symbol.asyncIterator]()
    const fromTm = (async () => {
      let out = ''
      for (;;) {
        const r = await ask.next()
        if (r.done) return answers(out)
        if (r.value.stream === 'stdout') out += r.value.text
      }
    })().catch(() => null)
    const first = await Promise.race([menu.then(r => ({ r })), fromTm.then(a => ({ a }))])
    if ('r' in first) {
      void ask.return?.({ code: null, signal: null })
      return first.r
    }
    if (!first.a) return menu
    return { result: { questions: e.questions, answers: first.a } }
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const w = await read($, watch)
    if (e.props.hasSurvey || !(await read($, band)) || !shows(w)) return next(e)
    return drawBand($.ui.resolve(e), e.props.bodyColumns, w)
  })

  on('turn.start', async ($, e, next) => {
    const r = await next(e)
    await saw($, { kind: 'turn.start' }, 'turn.start')
    return r
  })

  on('turn.complete', async ($, e, next) => {
    // A subagent's run is part of its spawner's turn.
    if (!e.agentId) await saw($, { kind: 'turn.complete', reason: e.reason }, 'turn.complete')
    void spent($, e.turnId, e.usage)
    return next(e)
  })

  // An AskUserQuestion menu is open for as long as its call runs.
  on('tool.call', { tool: 'AskUserQuestion' }, async ($, e, next) => {
    const key = waitKey(e.tool, e.agentId)
    await saw($, { kind: 'wait', key, reason: 'question' }, 'tool.call')
    try {
      return await next(e)
    } finally {
      await saw($, { kind: 'unwait', key }, 'tool.call')
    }
  })

  // A permission prompt opens unless a hook beneath decided; it closes
  // when the tool ends or is denied.
  on('classic.PermissionRequest', async ($, e, next) => {
    const r = await next(e)
    if (!r.decision) {
      const reason = e.tool_name === 'AskUserQuestion' ? 'question' : 'permission'
      await saw($, { kind: 'wait', key: waitKey(e.tool_name, e.agent_id), reason }, 'PermissionRequest')
    }
    return r
  })
  on('classic.PostToolUse', async ($, e, next) => {
    await saw($, { kind: 'unwait', key: waitKey(e.tool_name, e.agent_id) }, 'PostToolUse')
    return next(e)
  })
  on('classic.PostToolUseFailure', async ($, e, next) => {
    await saw($, { kind: 'unwait', key: waitKey(e.tool_name, e.agent_id) }, 'PostToolUseFailure')
    return next(e)
  })
  on('classic.PermissionDenied', async ($, e, next) => {
    await saw($, { kind: 'unwait', key: waitKey(e.tool_name, e.agent_id) }, 'PermissionDenied')
    return next(e)
  })

  // /compact runs no turn; ahead-of-time compaction doesn't hold the
  // session up.
  on('session.compact', async ($, e, next) => {
    if (e.trigger === 'precompute') return next(e)
    await saw($, { kind: 'compact', on: true }, 'session.compact')
    try {
      return await next(e)
    } finally {
      await saw($, { kind: 'compact', on: false }, 'session.compact')
    }
  })

  // The last report goes before the process does, or after END_WAIT_MS
  // at most.
  on('session.end', async ($, e, next) => {
    const { sent } = await saw($, e.reason === 'clear' ? { kind: 'clear' } : { kind: 'end' }, 'session.end')
    await Promise.race([sent, $.clock.sleep(END_WAIT_MS)])
    return next(e)
  })
}

// spent sends what the turn cost, numbers only, a subagent's turns
// included: they are spend too. Failures are logged and dropped.
async function spent($: EngineInterface, turnId: string, u: TurnUsageIn | undefined) {
  if (!socket || !u) return
  try {
    const ledger = (await $.session.usage()).cost?.usd
    let out: UsageReport | null = null
    await update($, usage, before => {
      const r = reportOf(turnId, u, ledger, before)
      out = r.report
      return r.now
    })
    if (!out) return
    const r = await $.http.fetch('http://terminatr/v1/usage', {
      method: 'POST', socketPath: socket, headers: { 'content-type': 'application/json' }, body: JSON.stringify(out),
    })
    if (!r.ok) $.ui.log(`terminatr: usage report: ${r.status} ${r.text}`, { to: 'debug' })
  } catch (err) {
    $.ui.log(`terminatr: usage report: ${String(err)}`, { to: 'debug' })
  }
}

// The channel: the socket, and one report in flight at a time, the
// latest waiting behind it (a report is the whole state, so the ones
// between can go).
let socket = ''
let sending: Promise<void> | null = null
let pending: string | null = null
let last = ''
let loops = 0

// startReports opens the channel, in a terminatr session with the mod's
// socket: it reports the state the turn is in (a reload carries on),
// then beats.
async function startReports($: EngineInterface) {
  socket = (await $.env.get('TERMINATR_MOD_SOCKET')) ?? ''
  if (!socket) return
  last = ''
  let now = initialTurn
  await update($, turn, t => (now = t.exited ? { ...initialTurn } : t))
  void report($, now, 'session.start')
  $.clock.every(BEAT_MS, () => {
    void read($, turn).then(t => report($, t, 'beat'))
  })
  // An unload ends the loop mid-call: nothing to report.
  deliver($).catch(() => {})
}

// registerTools lists the thread's tools for the model; one that fails
// leaves its shell command.
async function registerTools($: EngineInterface) {
  for (const t of TOOLS) {
    try {
      await $.tool.register(t)
    } catch (err) {
      $.ui.log(`terminatr: tool ${t.name}: ${String(err)}`, { to: 'debug' })
    }
  }
}

// deliver takes the server's prompts until the session is gone. A
// reload starts a new loop; the old one stops at its next turn round.
async function deliver($: EngineInterface) {
  const me = (await $.clock.now()) * 1000 + (++loops % 1000)
  await update($, deliverer, () => me)
  for (;;) {
    if ((await read($, deliverer)) !== me) return
    let r
    try {
      r = await $.http.fetch(`http://terminatr/v1/prompts?wait=${POLL_S}`, { socketPath: socket })
    } catch (err) {
      $.ui.log(`terminatr: prompts: ${String(err)}`, { to: 'debug' })
      await $.clock.sleep(RETRY_MS)
      continue
    }
    if (r.status === 410) return
    if (r.status === 204) {
      await $.clock.sleep(POLL_GAP_MS)
      continue
    }
    const offer = r.ok ? offerOf(r.text) : null
    if (!offer) {
      $.ui.log(`terminatr: prompts: ${r.status} ${r.text}`, { to: 'debug' })
      await $.clock.sleep(RETRY_MS)
      continue
    }
    if ((await read($, deliverer)) !== me) return
    if (handled(offer, await read($, delivering))) {
      if (!(await ack($, offer.id, { result: 'submitted' }))) await $.clock.sleep(POLL_GAP_MS)
      continue
    }
    // Hand on only what the server let us take, and keep its id first,
    // so a reload from here on doesn't hand it on again.
    if (!(await ack($, offer.id, { result: 'taken' }))) {
      await $.clock.sleep(POLL_GAP_MS)
      continue
    }
    await update($, delivering, () => offer.id)
    const done = await handOn($, offer)
    // Claude didn't take it: offered again, it is handed on again.
    if (done.result === 'refused') await update($, delivering, () => '')
    await ack($, offer.id, done)
  }
}

// handOn gives Claude the offer: queued, it runs once the session is
// idle; the prompt box keeps whatever is typed in it.
async function handOn($: EngineInterface, offer: Offer): Promise<Ack> {
  try {
    if (offer.kind === 'command') {
      await $.command.run({ command: offer.command, args: offer.args })
      return { result: 'submitted' }
    }
    const r = await $.prompt.submit({ text: offer.text, asUser: true })
    return r.drop === undefined ? { result: 'submitted' } : { result: 'refused', error: r.drop }
  } catch (err) {
    return { result: 'refused', error: errorText(err) }
  }
}

// ack tells the server what became of prompt id; true when it took it.
async function ack($: EngineInterface, id: string, a: Ack): Promise<boolean> {
  try {
    const r = await $.http.fetch(`http://terminatr/v1/prompts/${encodeURIComponent(id)}/ack`, {
      method: 'POST', socketPath: socket, headers: { 'content-type': 'application/json' }, body: JSON.stringify(a),
    })
    if (!r.ok) $.ui.log(`terminatr: prompt ${id} ${a.result}: ${r.status} ${r.text}`, { to: 'debug' })
    return r.ok
  } catch (err) {
    $.ui.log(`terminatr: prompt ${id} ${a.result}: ${String(err)}`, { to: 'debug' })
    return false
  }
}

// saw moves the turn on and reports where it got to, without waiting
// for the report: a hook never holds the session up on the server. It
// answers the report, for session.end.
async function saw($: EngineInterface, s: Seen, event: string): Promise<{ sent: Promise<void> }> {
  let now = initialTurn
  await update($, turn, t => (now = step(t, s)))
  return { sent: report($, now, event) }
}

// report sends the state t is in, unless it is the one sent last (a
// beat always goes), and resolves once it went.
function report($: EngineInterface, t: TerminatrTurn, event: string): Promise<void> {
  if (!socket) return Promise.resolve()
  const s = stateOf(t)
  const key = `${s.state}/${s.reason}`
  if (key === last && event !== 'beat') return sending ?? Promise.resolve()
  last = key
  pending = JSON.stringify({ ...s, event })
  if (!sending) {
    sending = (async () => {
      while (pending) {
        const body = pending
        pending = null
        await post($, body)
      }
      sending = null
    })()
  }
  return sending
}

async function post($: EngineInterface, body: string) {
  try {
    const r = await $.http.fetch('http://terminatr/v1/state', {
      method: 'POST', socketPath: socket, headers: { 'content-type': 'application/json' }, body,
    })
    if (!r.ok) $.ui.log(`terminatr: state report: ${r.status} ${r.text}`, { to: 'debug' })
  } catch (err) {
    $.ui.log(`terminatr: state report: ${String(err)}`, { to: 'debug' })
  }
}

// follow keeps the latest line of the feed, and the status entry and CI
// toast with it when the band is on. The loop ends with the session (tm
// watch exits after the "exited" line) or when the module unloads.
async function follow($: EngineInterface, bin: string, id: string, isBand: boolean) {
  let rest = ''
  let last: TerminatrWatch | null = null
  try {
    for await (const { stream, text } of $.process.spawn({ argv: [bin, 'watch', '--session', id, '--json'] })) {
      if (stream !== 'stdout') continue
      const got = feed(rest, text)
      rest = got.rest
      if (got.watches.length === 0) continue
      for (const w of got.watches) {
        const toast = isBand ? ciToast(last, w) : undefined
        if (toast) $.ui.toast(toast)
        last = w
      }
      const w = last
      await update($, watch, () => w)
      if (isBand) $.ui.status(statusText(w))
    }
  } catch (err) {
    $.ui.log(`terminatr: tm watch: ${String(err)}`, { to: 'debug' })
  }
}
