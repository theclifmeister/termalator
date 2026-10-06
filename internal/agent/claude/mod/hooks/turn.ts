import type { TerminatrTurn } from '../types'

// The session's state as the mod sees it from its own events (docs/SPEC.md
// §8.6, Mods): plain functions of what happened, so the tests can hold
// them to it. The server takes the state the mod reports over the
// session's own channel (register.ts) in place of the command hooks.

export type State = 'idle' | 'working' | 'blocked' | 'exited'

// What the mod saw. A wait is a dialog the user has to answer: a
// permission prompt or an AskUserQuestion menu, keyed by the tool and
// the loop that asked (waitKey).
export type Seen =
  | { kind: 'turn.start' }
  | { kind: 'turn.complete'; reason: 'answer' | 'aborted' | 'refusal' | 'error' }
  | { kind: 'wait'; key: string; reason: 'permission' | 'question' }
  | { kind: 'unwait'; key: string }
  | { kind: 'compact'; on: boolean }
  | { kind: 'clear' }
  | { kind: 'end' }

export const initialTurn: TerminatrTurn = { turn: false, wait: '', waitReason: '', compacting: false, idleReason: '', exited: false }

// waitKey names a dialog by its tool and the loop that asked: the
// permission prompt and the tool's end carry both, nothing else in common.
export function waitKey(tool: string, agentId?: string): string {
  return `${tool}/${agentId ?? ''}`
}

// step is the turn after what the mod saw.
export function step(t: TerminatrTurn, s: Seen): TerminatrTurn {
  switch (s.kind) {
    case 'turn.start':
      return { ...t, turn: true, idleReason: '' }
    case 'turn.complete': {
      // A dialog doesn't outlive its turn: Esc on one ends the turn.
      const idleReason = s.reason === 'aborted' ? 'interrupted' : s.reason === 'error' ? 'error' : ''
      return { ...t, turn: false, wait: '', waitReason: '', idleReason }
    }
    case 'wait':
      return { ...t, wait: s.key, waitReason: s.reason }
    case 'unwait':
      return t.wait === s.key ? { ...t, wait: '', waitReason: '' } : t
    case 'compact':
      return { ...t, compacting: s.on }
    case 'clear':
      return { ...initialTurn }
    case 'end':
      return { ...t, exited: true }
  }
}

// stateOf is the state a turn reports, with its reason.
export function stateOf(t: TerminatrTurn): { state: State; reason: string } {
  if (t.exited) return { state: 'exited', reason: '' }
  if (t.wait) return { state: 'blocked', reason: t.waitReason }
  if (t.turn || t.compacting) return { state: 'working', reason: '' }
  return { state: 'idle', reason: t.idleReason }
}
