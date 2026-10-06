import type { TerminatrUsage } from '../types'

// What a turn cost, as numbers only (docs/SPEC.md §8.6, Mods): the four
// token counts of turn.complete's usage, and the dollars the session's
// own ledger rose by since the last turn. No text, no ids beyond the
// turn's own.

// What turn.complete carries, as far as it is used.
export type TurnUsageIn = {
  model: string
  input_tokens: number
  output_tokens: number
  cache_read_input_tokens: number
  cache_creation_input_tokens: number
}

export type UsageReport = {
  turn: string
  model: string
  input: number
  output: number
  cache_read: number
  cache_creation: number
  cost_usd: number
}

export const initialUsage: TerminatrUsage = { usd: 0 }

const count = (n: unknown): number => (typeof n === 'number' && Number.isFinite(n) && n > 0 ? Math.floor(n) : 0)

// reportOf is the report for a turn that cost something, or null; and
// the ledger as it stands now, to carry to the next turn. A ledger that
// went down (a new session under the same state) costs nothing.
export function reportOf(
  turn: string, u: TurnUsageIn | undefined, ledger: number | undefined, before: TerminatrUsage,
): { report: UsageReport | null; now: TerminatrUsage } {
  const usd = typeof ledger === 'number' && Number.isFinite(ledger) && ledger >= 0 ? ledger : before.usd
  const now = { usd }
  if (!u) return { report: null, now }
  const report: UsageReport = {
    turn,
    model: String(u.model ?? '').slice(0, 64),
    input: count(u.input_tokens),
    output: count(u.output_tokens),
    cache_read: count(u.cache_read_input_tokens),
    cache_creation: count(u.cache_creation_input_tokens),
    cost_usd: Math.max(0, Math.round((usd - before.usd) * 1e6) / 1e6),
  }
  return { report, now }
}
