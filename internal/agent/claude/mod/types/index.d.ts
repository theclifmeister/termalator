// The terminatr mod's state contract: one line of `tm watch --json`
// (proto.Watch, docs/SPEC.md §3.3), kept as the session's latest,
// whether the band and status entry show ([mods] band), the turn it
// reports to the server, the id of the last prompt from the server it
// handed to Claude, and which load of the module takes those prompts.
// In a coordinator, also the project's line of `tm watch --project
// --json` (proto.ProjectWatch) for the /tm pane, what its buttons asked,
// the task being sent back, and the thread report it shows.

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
  // A coordinator's open questions (tm ask), which its band offers to
  // open in the question dialog; absent for none.
  questions?: number
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

// One line of `tm watch --project --json` (proto.ProjectWatch).
export type TerminatrProject = {
  project: string
  needs_you: TerminatrNeed[]
  inbox: TerminatrItem[]
  threads: TerminatrThread[]
  ready: TerminatrTodo[]
  context?: TerminatrContext
  ticker?: TerminatrTicker
}

// The ticker's timers: ISO times, so a line only changes when the ticker
// acts; the pane works out the ages (proto.WatchTicker).
export type TerminatrTicker = {
  pr_checked?: string
  synced?: string
  pr_poll_seconds: number
  gh_failing?: boolean
}

// How full the coordinator's context window is; hint says it reached
// the threshold (percent, 0 for never) and /clear is worth considering.
export type TerminatrContext = {
  tokens: number
  window: number
  percent: number
  threshold: number
  hint?: boolean
}

// What waits for the user, most pressing first; 'queue' is a session
// whose queued prompts are held (session names it).
export type TerminatrNeed = {
  why: 'queue' | 'review' | 'question' | 'ci' | 'blocked'
  task?: string
  title: string
  status?: string
  thread?: string
  session?: string
  question?: string
  pr?: string
  pr_url?: string
  pr_number?: number
  mergeable?: boolean
  asked?: string
}

export type TerminatrItem = {
  id: string
  kind: string
  subject: string
  summary: string
  needs_user?: boolean
  count: number
  task?: string
  what: string
  title?: string
}

export type TerminatrThread = {
  id: string
  title: string
  session?: string
  state?: string
  reason?: string
  needs_you?: string
  task?: { id: string; title: string; status: string; steps_done: number; steps_total: number; current?: string }
  pr?: string
  pr_url?: string
  pr_bad?: boolean
  pr_checked?: string
  reports?: number
  done?: boolean
}

export type TerminatrTodo = {
  task: string
  title: string
  status: string
  asked?: string
}

// An ask a /tm button sent the coordinator: the task, the ask, and the
// task's status then; it shows as sent while the status holds.
export type TerminatrAsk = { task: string; kind: string; status: string }

// The thread report the /tm-report pane shows.
export type TerminatrReport = { thread: string; title: string; text: string }

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
      project: TerminatrProject | null
      asks: TerminatrAsk[]
      sendBack: string
      report: TerminatrReport | null
    }
  }
}
