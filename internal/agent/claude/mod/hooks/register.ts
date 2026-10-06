// The terminatr mod (docs/SPEC.md §8.6, Mods): it follows the session's
// `tm watch --json` feed for the session's life and keeps the latest
// line in $.state. From it, unless [mods] band is off
// (TERMINATR_BAND=off), it draws the band above the prompt and keeps a
// status entry under it: the thread's task, steps, current item, PR and
// what waits for the user; and it toasts when the PR's CI run finishes.
// The command hooks beside it in hooks.json stay the source of the
// session's state; the mod only reads.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { TerminatrWatch } from '../types'
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
