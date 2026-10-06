// The terminatr mod's state contract: one line of `tm watch --json`
// (proto.Watch, docs/SPEC.md §3.3), kept as the session's latest,
// whether the band and status entry show ([mods] band), the turn it
// reports to the server, the id of the last prompt from the server it
// handed to Claude, and which load of the module takes those prompts.

export type TerminatrWatch = {
  session: {
    id: string
    role: string
    agent?: string
    project?: string
    thread?: string
    state?: string
    reason?: string
    needs_you?: string
  }
  task: {
    id: string
    title: string
    status: string
    steps_done: number
    steps_total: number
    current?: string
  } | null
  pr: string
  pr_url?: string
  needs_you: number
  inbox: number
  queued_prompts: number
}

// The session's turn as the mod's own events left it (hooks/turn.ts),
// kept across reloads: a turn running, the dialog waiting on the user
// (its tool and loop, and why), a compaction, why it last went idle,
// and whether the session ended.
export type TerminatrTurn = {
  turn: boolean
  wait: string
  waitReason: string
  compacting: boolean
  idleReason: string
  exited: boolean
}

// The session's cost ledger as the last turn left it (hooks/usage.ts):
// the dollars /cost showed then, so the next turn reports the rise.
export type TerminatrUsage = {
  usd: number
}

declare module 'claude-code' {
  interface PluginState {
    terminatr: {
      watch: TerminatrWatch | null
      band: boolean
      turn: TerminatrTurn
      usage: TerminatrUsage
      delivering: string
      deliverer: number
    }
  }
}
