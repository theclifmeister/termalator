import type { TerminatrAsk, TerminatrNeed, TerminatrProject, TerminatrThread, TerminatrTodo } from '../types'

// The coordinator's /tm pane (docs/SPEC.md §8.6, Mods): what it says
// about a line of `tm watch --project --json`, and what its buttons ask.
// Plain functions of the line, so the tests can hold them to it; the
// hooks that draw and press live in pane.tsx.
//
// A button never acts itself: it sends the coordinator a fixed line in
// the user's own words ($.prompt.submit, asUser), and the coordinator
// does the rest, as if the user had typed it.

// An ask a button sends.
export type AskKind = 'accept' | 'send-back' | 'merge' | 'delegate'

// askLine is the line a button sends the coordinator: "accept T63",
// "send T63 back: <note>", "merge PR #120 for T63", "delegate T70".
export function askLine(kind: AskKind, task: string, extra: { note?: string; pr?: number } = {}): string {
  switch (kind) {
    case 'accept':
      return `accept ${task}`
    case 'send-back':
      return `send ${task} back: ${oneLine(extra.note ?? '')}`
    case 'merge':
      return extra.pr ? `merge PR #${extra.pr} for ${task}` : `merge the PR for ${task}`
    case 'delegate':
      return `delegate ${task}`
  }
}

// MAX_NOTE is the longest send-back note, in characters, as the Go
// dashboard's (project.MaxSendBackNote).
export const MAX_NOTE = 200

// oneLine folds a note to one line of at most MAX_NOTE characters.
export function oneLine(note: string): string {
  const s = note.replace(/\s+/g, ' ').trim()
  return s.length > MAX_NOTE ? s.slice(0, MAX_NOTE) : s
}

// asked is what the coordinator was already asked about a task: the
// inbox's ask (the Go dashboard's keys), or a button pressed here while
// the task's status still holds. "" when nothing.
export function asked(task: string | undefined, status: string | undefined, fromInbox: string | undefined, asks: readonly TerminatrAsk[]): string {
  if (!task) return ''
  if (fromInbox) return fromInbox
  const a = asks.find(a => a.task === task && a.status === (status ?? ''))
  return a ? a.kind : ''
}

// The buttons a need gets, in order: what can be asked of its task and
// whether its thread has a report to open.
export function needButtons(n: TerminatrNeed, sent: string): (AskKind | 'report')[] {
  const out: (AskKind | 'report')[] = []
  if (n.task && !sent) {
    if (n.why === 'review' || n.status === 'review') {
      out.push('accept', 'send-back')
      if (n.mergeable && n.pr_number) out.push('merge')
    } else if (n.status === 'blocked' && !n.thread) {
      out.push('delegate')
    }
  }
  if (n.thread) out.push('report')
  return out
}

// BUTTON_LABELS are the buttons' labels.
export const BUTTON_LABELS: Record<AskKind | 'report', string> = {
  accept: 'Accept',
  'send-back': 'Send back',
  merge: 'Merge',
  delegate: 'Delegate',
  report: 'Report',
}

// WHY_WORDS say why a need waits, short.
const WHY_WORDS: Record<TerminatrNeed['why'], string> = {
  review: 'review',
  question: 'asks',
  ci: 'red CI',
  blocked: 'blocked',
}

// needHead is a need's first row: "T63 review · /tm dashboard pane".
export function needHead(n: TerminatrNeed): { ref: string; why: string; title: string } {
  return { ref: n.task ?? n.thread ?? '', why: WHY_WORDS[n.why], title: n.title }
}

// needDetail is a need's second row: the thread's question, else its
// PR in the ticker's words; "" for neither.
export function needDetail(n: TerminatrNeed): { text: string; tone: 'question' | 'bad' | 'ok' | '' } {
  if (n.question) return { text: '“' + n.question + '”', tone: 'question' }
  if (n.pr) return { text: n.pr, tone: n.why === 'ci' || isBad(n.pr) ? 'bad' : 'ok' }
  return { text: '', tone: '' }
}

// isBad reports a PR the thread has to act on, in the ticker's words.
export function isBad(pr: string): boolean {
  return /failed|conflicts|changes requested|behind/.test(pr)
}

// sentWords say what was asked: "asked: accept".
export function sentWords(kind: string): string {
  return 'asked the coordinator: ' + kind.replace('-', ' ')
}

// threadLine is a running thread in one compact row: "t-0059 T64
// working 3/5 ▸ mod tools · #121 checks pending".
export function threadLine(t: TerminatrThread): string {
  const parts = [t.id]
  if (t.task) parts.push(t.task.id)
  parts.push(threadState(t))
  if (t.task && t.task.steps_total > 0) parts.push(`${t.task.steps_done}/${t.task.steps_total}`)
  let line = parts.join(' ')
  if (t.task?.current) line += ' ▸ ' + t.task.current
  if (t.pr) line += ' · ' + t.pr
  return line
}

// threadState is a thread's state in a word: the session's, "done" for a
// thread that called tm done, "stopped" for one with no session.
export function threadState(t: TerminatrThread): string {
  if (t.done) return 'done'
  if (!t.session) return 'stopped'
  if (t.state === 'blocked') return t.reason === 'question' ? 'asks' : 'blocked'
  return t.state || 'running'
}

// todoLine is a task on deck: "T70 ready · title".
export function todoLine(t: TerminatrTodo): string {
  return `${t.task} ${t.status} · ${t.title}`
}

// MAX_READY bounds the tasks on deck the pane lists.
export const MAX_READY = 5

// isNarrow says the pane is too narrow for a row's buttons beside its
// head: they go on a row of their own (a phone, an inline pane).
export function isNarrow(bodyColumns: number): boolean {
  return bodyColumns < 60
}

// summary is the pane's title line: "3 need you · 1 in inbox · 5 threads".
export function summary(p: TerminatrProject): string {
  const parts = [`${p.needs_you.length} need${p.needs_you.length === 1 ? 's' : ''} you`]
  if (p.inbox.length > 0) parts.push(`${p.inbox.length} in inbox`)
  parts.push(`${p.threads.length} thread${p.threads.length === 1 ? '' : 's'}`)
  return parts.join(' · ')
}

// projectFeed reads `tm watch --project --json` output: what is left of
// the last piece and a new piece, answering the whole lines that parse,
// in order, and the rest.
export function projectFeed(rest: string, text: string): { projects: TerminatrProject[]; rest: string } {
  const lines = (rest + text).split('\n')
  const tail = lines.pop() ?? ''
  const projects: TerminatrProject[] = []
  for (const l of lines) {
    if (l.trim() === '') continue
    try {
      const v = JSON.parse(l) as TerminatrProject
      if (v && typeof v.project === 'string' && Array.isArray(v.needs_you)) projects.push(v)
    } catch {
      // not a line of the feed: skip it
    }
  }
  return { projects, rest: tail }
}

// reportOf reads `tm thread show <id> --json`: its report's text, "" for
// none.
export function reportOf(out: string): string {
  try {
    const v = JSON.parse(out) as { report_text?: unknown }
    return typeof v.report_text === 'string' ? v.report_text : ''
  } catch {
    return ''
  }
}
