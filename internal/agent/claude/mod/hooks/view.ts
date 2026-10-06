import type { TerminatrWatch } from '../types'

// What the band, the status entry and the CI toast say about a watch
// line. Plain functions of the line, so the tests can hold them to it.

// shows reports whether the session has anything to show: a thread with
// a task, or a session whose project has work waiting for the user. A
// thread without a task, and a quiet project, show nothing.
export function shows(w: TerminatrWatch | null): w is TerminatrWatch {
  if (!w || w.session.state === 'exited') return false
  return w.task !== null || waiting(w) > 0
}

// bandShows reports whether the band above the prompt has anything to
// draw: only a thread's task. The counts stay in the status entry, so a
// coordinator, which has no task, gets no band.
export function bandShows(w: TerminatrWatch | null): w is TerminatrWatch {
  return shows(w) && w.task !== null
}

// waiting counts what waits for the user: the board's Needs you tasks
// and the unhandled inbox items.
export function waiting(w: TerminatrWatch): number {
  return w.needs_you + w.inbox
}

// steps is "steps 2/4", or "" for a task without steps.
export function steps(w: TerminatrWatch): string {
  const t = w.task
  return t && t.steps_total > 0 ? `steps ${t.steps_done}/${t.steps_total}` : ''
}

// isPRBad reports a PR the thread has to act on, in the ticker's words.
export function isPRBad(pr: string): boolean {
  return /failed|conflicts|changes requested|behind/.test(pr)
}

// counts is "2 need you · 1 in inbox", leaving out what is zero.
export function counts(w: TerminatrWatch): string {
  const parts: string[] = []
  if (w.needs_you > 0) parts.push(`${w.needs_you} need${w.needs_you === 1 ? 's' : ''} you`)
  if (w.inbox > 0) parts.push(`${w.inbox} in inbox`)
  return parts.join(' · ')
}

// statusText is the status entry under the prompt, short enough for a
// phone: "T50 steps 2/4 · #12 open, checks pass · 1 needs you". undefined
// clears it.
export function statusText(w: TerminatrWatch | null): string | undefined {
  if (!shows(w)) return undefined
  const parts: string[] = []
  if (w.task) {
    const t = w.task
    parts.push(t.steps_total > 0 ? `${t.id} steps ${t.steps_done}/${t.steps_total}` : t.id)
  }
  if (w.pr) parts.push(w.pr)
  const c = counts(w)
  if (c) parts.push(c)
  return parts.join(' · ')
}

// checks is a PR's checks in the ticker's words: "pending", "pass",
// "failed" (with how many), or "" when it says nothing of them.
function checks(pr: string): string {
  if (/checks pending/.test(pr)) return 'pending'
  if (/checks pass/.test(pr)) return 'pass'
  const m = /(\d+) checks? failed/.exec(pr)
  return m ? `failed ${m[1]}` : ''
}

// ciToast is the toast for a CI run that finished between two lines of
// the feed: the PR's checks went from pending to pass or failed.
// undefined when none did.
export function ciToast(prev: TerminatrWatch | null, next: TerminatrWatch): string | undefined {
  if (!prev || checks(prev.pr) !== 'pending') return undefined
  const now = checks(next.pr)
  if (now === '' || now === 'pending') return undefined
  const num = /#\d+/.exec(next.pr)?.[0] ?? 'PR'
  const task = next.task ? `${next.task.id} ` : ''
  if (now === 'pass') return `${task}${num}: checks passed`
  const n = now.slice('failed '.length)
  return `${task}${num}: ${n} check${n === '1' ? '' : 's'} failed`
}
