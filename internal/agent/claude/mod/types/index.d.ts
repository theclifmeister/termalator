// The terminatr mod's state contract: one line of `tm watch --json`
// (proto.Watch, docs/SPEC.md §3.3), kept as the session's latest, and
// whether the band and status entry show ([mods] band).

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

declare module 'claude-code' {
  interface PluginState {
    terminatr: { watch: TerminatrWatch | null; band: boolean }
  }
}
