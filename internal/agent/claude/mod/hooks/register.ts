// The terminatr mod (docs/SPEC.md §8.6, Mods): it follows the session's
// `tm watch --json` feed for the session's life and keeps the latest
// line in $.state, where the band and status entry read it. It draws
// nothing itself yet. The command hooks beside it in hooks.json stay
// the source of the session's state; the mod only reads.

import { atom, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import { feed } from './feed'

const watch = atom({ plugin: 'terminatr', key: 'watch' } as const, null)

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const started = await next(e)
    const bin = await $.env.get('TERMINATR_BIN')
    const id = await $.env.get('TERMINATR_SESSION')
    if (bin && id) void follow($, bin, id)
    return started
  })
}

// follow keeps the latest line of the feed. The loop ends with the
// session (tm watch exits after the "exited" line) or when the module
// unloads.
async function follow($: EngineInterface, bin: string, id: string) {
  let rest = ''
  try {
    for await (const { stream, text } of $.process.spawn({ argv: [bin, 'watch', '--session', id, '--json'] })) {
      if (stream !== 'stdout') continue
      const got = feed(rest, text)
      rest = got.rest
      const w = got.watches.at(-1)
      if (w) await update($, watch, () => w)
    }
  } catch (err) {
    $.ui.log(`terminatr: tm watch: ${String(err)}`, { to: 'debug' })
  }
}
