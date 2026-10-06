// Prompts from the server (docs/SPEC.md §8.6, Mods): the head of the
// session's prompt queue, offered to the mod over its socket, and what
// the mod does with it. Plain functions; the loop that calls them lives
// in register.ts.

// An offer: a prompt, or a slash command split into its name (no slash)
// and the rest of the line.
export type Offer =
  | { id: string; kind: 'prompt'; text: string }
  | { id: string; kind: 'command'; text: string; command: string; args: string }

// An ack's body.
export type Ack = { result: 'taken' | 'submitted' | 'refused'; error?: string }

// offerOf reads GET /v1/prompts's body, or null when it isn't an offer.
export function offerOf(text: string): Offer | null {
  let v: unknown
  try {
    v = JSON.parse(text)
  } catch {
    return null
  }
  if (typeof v !== 'object' || v === null) return null
  const o = v as Record<string, unknown>
  if (typeof o.id !== 'string' || !o.id || typeof o.text !== 'string') return null
  if (o.kind === 'prompt') return { id: o.id, kind: 'prompt', text: o.text }
  if (o.kind === 'command' && typeof o.command === 'string' && o.command) {
    return { id: o.id, kind: 'command', text: o.text, command: o.command, args: typeof o.args === 'string' ? o.args : '' }
  }
  return null
}

// handled says whether the offer is the one this session's mod already
// handed on (its id kept in $.state across reloads): then it is only
// acked again, never handed on twice.
export function handled(offer: Offer, delivering: string): boolean {
  return offer.id === delivering
}

// errorText bounds an error for an ack.
export function errorText(err: unknown): string {
  const s = err instanceof Error ? err.message : String(err)
  return s.length > 200 ? s.slice(0, 200) : s
}
