// The guard's hooks (hooks/guard.ts judges): every tool call is judged
// against the session's rules before it runs, in every permission mode,
// so a refusal comes ahead of any dialog. A refused call returns the
// rule's sentence as the tool's error, and the mod reports it to the
// server (POST /v1/denied), which journals it and tells the coordinator.
// A call no rule matches goes on to Claude's own checks.
//
// The rules come from the server (GET /v1/rules), fetched at the first
// call after a load and again once they are a minute old. Without the mod
// socket, or when the server can't say, the guard is off: the session
// keeps exactly the permission rules it has without the mod.

import type { EngineInterface, On } from 'claude-code'

import { judge, off, rulesOf } from './guard'
import type { Denial, Rules } from './guard'

// RULES_TTL_MS is how long fetched rules hold; FETCH_MS bounds a fetch.
const RULES_TTL_MS = 60_000
const FETCH_MS = 2_000

let cached: { at: number; rules: Promise<Rules> } | null = null

export function guard(on: On) {
  on('tool.call', async ($, e, next) => {
    const d = judge(await rulesNow($), e.tool, e as unknown as Record<string, unknown>)
    if (!d) return next(e)
    void reportDenial($, e.tool, d)
    return { deny: d.message }
  }).catch(($, e, next) =>
    next.called ? next(e) : { deny: 'terminatr guard: the check of this call failed, so it was refused. Try again; if it keeps failing, say so.' },
  )

  // What $.tool.check asks gets the same answer. A real call the guard
  // refuses never gets here: tool.call refused it first.
  on('tool.check', async ($, e, next) => {
    const input = e.input && typeof e.input === 'object' ? (e.input as Record<string, unknown>) : {}
    const d = judge(await rulesNow($), e.tool, input)
    return d ? { decision: 'deny' as const, reason: d.message } : next(e)
  }).catch(() => ({ decision: 'deny' as const }))
}

// rulesNow is the session's rules, fetched when there are none or they
// are older than RULES_TTL_MS. Rules it can't get are the guard off,
// never a refusal of every call.
async function rulesNow($: EngineInterface): Promise<Rules> {
  try {
    const socket = (await $.env.get('TERMINATR_MOD_SOCKET')) ?? ''
    if (!socket) return off
    const now = (await $.clock.now()) * 1000
    if (!cached || now - cached.at > RULES_TTL_MS) cached = { at: now, rules: fetchRules($, socket) }
    return await cached.rules
  } catch {
    cached = null
    return off
  }
}

async function fetchRules($: EngineInterface, socket: string): Promise<Rules> {
  const got = await Promise.race([
    $.http.fetch('http://terminatr/v1/rules', { socketPath: socket }),
    $.clock.sleep(FETCH_MS).then(() => null),
  ])
  if (got && got.ok) return rulesOf(got.text)
  // Asked again at the next call, not after the TTL.
  cached = null
  log($, `terminatr: guard rules: ${got ? `${got.status} ${got.text}` : 'no answer'}`)
  return off
}

async function reportDenial($: EngineInterface, tool: string, d: Denial) {
  const socket = (await $.env.get('TERMINATR_MOD_SOCKET')) ?? ''
  if (!socket) return
  try {
    const r = await $.http.fetch('http://terminatr/v1/denied', {
      method: 'POST', socketPath: socket, headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ rule: d.rule, tool, summary: d.summary }),
    })
    if (!r.ok) log($, `terminatr: guard report: ${r.status} ${r.text}`)
  } catch (err) {
    log($, `terminatr: guard report: ${String(err)}`)
  }
}

function log($: EngineInterface, text: string) {
  try {
    $.ui.log(text, { to: 'debug' })
  } catch {
    // Nowhere to say it.
  }
}
