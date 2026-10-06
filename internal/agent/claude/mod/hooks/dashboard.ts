import type { TerminatrAsk, TerminatrContext, TerminatrItem, TerminatrNeed, TerminatrProject, TerminatrThread, TerminatrTodo } from '../types'

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
  if (n.why === 'queue') return out
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

// WHY_WORDS say why a need waits, short, in the TUI's words
// (docs/STYLE.md T2).
const WHY_WORDS: Record<TerminatrNeed['why'], string> = {
  queue: 'prompts held',
  review: 'in review',
  question: 'asks you',
  ci: 'checks failed',
  blocked: 'blocked',
}

// KIND_WORDS are the inbox kinds in words, where the kind's own name is a
// code, as the TUI shows them (kindWord).
const KIND_WORDS: Record<string, string> = {
  'thread-resolved': 'resolved',
  takeover: 'you typed',
  'server-restart': 'server restart',
  'pr-opened': 'PR opened',
  'pr-checks-failed': 'checks failed',
  'pr-review': 'PR review',
  'pr-merged': 'PR merged',
  'pr-closed': 'PR closed',
  'pr-conflict': 'PR conflict',
  'close-held': 'kept open',
  'gh-failing': 'gh failing',
  guard: 'guard refused',
}

// kindWord is an inbox kind (or an ask, "send-back") in words.
export function kindWord(kind: string): string {
  return KIND_WORDS[kind] ?? kind.replace(/-/g, ' ')
}

// needHead is a need's first row: "T63 in review · /tm dashboard pane".
export function needHead(n: TerminatrNeed): { ref: string; why: string; title: string } {
  return { ref: n.task ?? n.thread ?? n.session ?? '', why: WHY_WORDS[n.why], title: n.title }
}

// needKey tells needs apart in the pane: a held queue by its session, the
// rest by task, thread or title.
export function needKey(n: TerminatrNeed): string {
  return n.why === 'queue' ? 'queue-' + (n.session ?? '') : n.task ?? n.thread ?? n.title
}

// needDetail is a need's second row: what a held queue keeps back, the
// thread's question, else its PR in the ticker's words; "" for none.
export function needDetail(n: TerminatrNeed): { text: string; tone: 'question' | 'bad' | 'ok' | '' } {
  if (n.why === 'queue') {
    const what = n.thread ? 'the thread gets nothing' : 'the coordinator hears of nothing'
    return { text: `${what} until it clears; tm agent explain ${n.session ?? ''} says why`, tone: 'bad' }
  }
  if (n.question) return { text: '“' + n.question + '”', tone: 'question' }
  if (n.pr) return { text: n.pr, tone: n.why === 'ci' || isBad(n.pr) ? 'bad' : 'ok' }
  return { text: '', tone: '' }
}

// isBad reports a PR the thread has to act on, in the ticker's words.
export function isBad(pr: string): boolean {
  return /failed|conflicts|changes requested|behind/.test(pr)
}

// sentWords say what was asked: "asked the coordinator: accept".
export function sentWords(kind: string): string {
  return 'asked the coordinator: ' + kindWord(kind)
}

// threadLine is a running thread in one compact row: "t-0059 T64
// working · steps 3/5 · now: mod tools · #121 checks pending".
export function threadLine(t: TerminatrThread): string {
  const parts = [t.id]
  if (t.task) parts.push(t.task.id)
  parts.push(threadState(t))
  let line = parts.join(' ')
  if (t.task && t.task.steps_total > 0) line += ` · steps ${t.task.steps_done}/${t.task.steps_total}`
  if (t.task?.current) line += ' · now: ' + t.task.current
  if (t.pr) line += ' · ' + t.pr
  return line
}

// threadState is a thread's state in a word: the session's, "done" for a
// thread that called tm done, "stopped" for one with no session.
export function threadState(t: TerminatrThread): string {
  if (t.done) return 'done'
  if (!t.session) return 'stopped'
  if (t.state === 'blocked') return t.reason === 'question' ? 'asks you' : 'blocked'
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

// contextLine is the coordinator's context use for the pane: "context
// 84k / 200k · 42%", with the hint past the threshold. tone is the
// colour's name: ok below, warning from the threshold, error from 80%.
export function contextLine(c: TerminatrContext): { text: string; tone: 'ok' | 'warning' | 'error'; hint: string } {
  const k = (n: number) => (n >= 1_000_000 ? `${(n / 1e6).toFixed(1)}M` : n >= 1_000 ? `${Math.round(n / 1e3)}k` : String(n))
  const tone = c.percent >= 80 ? 'error' : c.hint ? 'warning' : 'ok'
  return {
    text: `context ${k(c.tokens)} / ${k(c.window)} · ${c.percent}%`,
    tone,
    hint: c.hint ? 'consider /clear: the context lives in files (tm context)' : '',
  }
}

// contextToast is the toast for a context that reached its threshold
// since the line before, once per crossing; undefined otherwise.
export function contextToast(was: boolean, c: TerminatrContext | undefined): string | undefined {
  if (!c?.hint || was) return undefined
  return `Context ${c.percent}% full: consider /clear; the context lives in files (tm context)`
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

// reportTextOf reads `tm thread show <id> --json`: its report's text, "" for
// none.
export function reportTextOf(out: string): string {
  try {
    const v = JSON.parse(out) as { report_text?: unknown }
    return typeof v.report_text === 'string' ? v.report_text : ''
  } catch {
    return ''
  }
}

// itemTone is the color an inbox item's kind gets, as the Go dashboard's
// (kindStyle): red for what went wrong, green for what finished, yellow
// for the rest.
export function itemTone(kind: string): 'error' | 'success' | 'warning' {
  if (['pr-checks-failed', 'pr-conflict', 'exited', 'gh-failing', 'blocked', 'needs-you', 'guard'].includes(kind)) return 'error'
  if (['report', 'pr-merged', 'pr-opened', 'task-done', 'thread-resolved'].includes(kind)) return 'success'
  return 'warning'
}

// itemParts are an inbox row's text after its kind, in the order it
// reads: "×3" when it stands for several items, the task, what happened.
// The task's title is apart, drawn last, so it is what a narrow row cuts.
export function itemParts(it: TerminatrItem): { head: string; title: string } {
  const head = [it.count > 1 ? `×${it.count}` : '', it.task ?? '', it.what ?? it.summary].filter(Boolean).join(' ')
  return { head, title: it.title ?? '' }
}
