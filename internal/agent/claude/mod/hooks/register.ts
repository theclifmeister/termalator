// The terminatr mod (docs/SPEC.md §8.6, Mods): it follows the session's
// `tm watch --json` feed for the session's life and keeps the latest
// line in $.state. From it, unless [mods] band is off
// (TERMINATR_BAND=off), it draws the band above the prompt and keeps a
// status entry under it: the thread's task, steps, current item, PR and
// what waits for the user; and it toasts when the PR's CI run finishes.
// It sends each AskUserQuestion menu to the server (`tm session ask`) and
// answers it with what `tm thread answer` gave, unless the user answers
// in the pane first. The command hooks beside it in hooks.json stay the
// source of the session's state.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { TerminatrWatch } from '../types'
import { answers, question } from './ask'
import { drawBand } from './band'
import { feed } from './feed'
import { ciToast, shows, statusText } from './view'

const watch = atom({ plugin: 'terminatr', key: 'watch' } as const, null)
const band = atom({ plugin: 'terminatr', key: 'band' } as const, true)

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const started = await next(e)
    const bin = await $.env.get('TERMINATR_BIN')
    const id = await $.env.get('TERMINATR_SESSION')
    const isBand = (await $.env.get('TERMINATR_BAND')) !== 'off'
    await update($, band, () => isBand)
    if (bin && id) void follow($, bin, id, isBand)
    return started
  })

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
