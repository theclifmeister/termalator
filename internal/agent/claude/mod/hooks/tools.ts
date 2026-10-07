// A thread's tools (docs/SPEC.md §8.6, Mods): in a thread session the mod
// registers typed tools for what a thread otherwise runs through the
// shell (tm report, tm status, tm task steps, tm done), and serves each
// call with POST /v1/tools/<name> on the session's mod socket, where the
// server checks the input and runs the command as this thread. Plain
// data and functions, for the tests.

// A tool as $.tool.register takes it; the model calls it as
// mcp__terminatr__<name>.
export type ThreadTool = {
  name: string
  description: string
  inputSchema: { type: 'object'; properties: Record<string, unknown>; required?: string[]; additionalProperties: false }
}

const line = (description: string, maxLength?: number) => ({ type: 'string', minLength: 1, ...(maxLength ? { maxLength } : {}), description })
const lines = (description: string, maxLength?: number) => ({ type: 'array', items: line('One line.', maxLength), description })

export const TOOLS: readonly ThreadTool[] = [
  {
    name: 'report',
    description:
      'Hand in your report to the coordinator (what `tm report` does): whenever you finish or stop to wait. ' +
      'Each field is a section of the report; the server checks the format and answers with the report number, or why it refused.',
    inputSchema: {
      type: 'object',
      properties: {
        pr: { type: 'string', pattern: '^https://', description: 'The PR URL, if you opened one: https://github.com/<owner>/<repo>/pull/<n> or https://dev.azure.com/<org>/<project>/_git/<repo>/pullrequest/<n>.' },
        report: { type: 'string', minLength: 1, description: 'The report itself, in Markdown: what you did, what you assumed, any repository outside the project you used. Subheadings are ### (## is reserved for the sections).' },
        next: lines('What happens next: one imperative action per item, at most 100 characters each.', 100),
        check: lines('Optional: how the user can see the change working (what to run, where to look), a few lines.'),
        remember: lines('Optional: lessons for the project, one per item.'),
        attach: lines('Optional: files meant for the user, by path (relative to your worktree, or absolute).'),
      },
      required: ['report', 'next'],
      additionalProperties: false,
    },
  },
  {
    name: 'status',
    description:
      'Set your thread status (what `tm status` does). Use needs_you when you are blocked on the human; ' +
      'percent and activity only when you have neither task steps nor a todo list.',
    inputSchema: {
      type: 'object',
      properties: {
        needs_you: line('The question or decision you are blocked on, for the human; one line, at most 300 characters.', 300),
        percent: { type: 'integer', minimum: 0, maximum: 100, description: 'How far along you are, 0-100.' },
        activity: line('What you are doing now, one line, at most 100 characters.', 100),
      },
      additionalProperties: false,
    },
  },
  {
    name: 'steps',
    description:
      "Work your task's steps (what `tm task steps` does): check the ones you finished, uncheck one done too early, " +
      'or add your plan as steps when the task has none. Steps are numbered from 1; the server answers what changed.',
    inputSchema: {
      type: 'object',
      properties: {
        check: { type: 'array', items: { type: 'integer', minimum: 1 }, description: 'Step numbers to check, in order.' },
        uncheck: { type: 'array', items: { type: 'integer', minimum: 1 }, description: 'Step numbers to uncheck.' },
        add: lines('Steps to add at the end, one per item, in order.'),
      },
      additionalProperties: false,
    },
  },
  {
    name: 'done',
    description: 'Mark your task finished (what `tm done` does), once your report is in. It refuses when no report came since your last prompt.',
    inputSchema: {
      type: 'object',
      properties: { summary: line('Optional: a one-line summary, at most 80 characters.', 80) },
      additionalProperties: false,
    },
  },
]

export const toolName = (t: ThreadTool) => `mcp__terminatr__${t.name}` as const

// body is the call's arguments as the server takes them: the tool's own
// fields alone (a tool.call input carries the envelope beside them).
export function body(t: ThreadTool, e: Record<string, unknown>): string {
  const out: Record<string, unknown> = {}
  for (const k of Object.keys(t.inputSchema.properties)) if (e[k] !== undefined) out[k] = e[k]
  return JSON.stringify(out)
}

// answer is the call's result from the server's reply: its text, or a
// deny with why it refused, which the model reads as the tool's error.
export function answer(status: number, text: string): { result: string } | { deny: string } {
  let got: { text?: unknown; error?: unknown } = {}
  try {
    const v = JSON.parse(text) as unknown
    if (v && typeof v === 'object') got = v as typeof got
  } catch {
    // Not the server's JSON: say what came.
  }
  if (status >= 200 && status < 300) return { result: typeof got.text === 'string' && got.text ? got.text : 'done' }
  const why = typeof got.error === 'string' && got.error ? got.error : `${status} ${text}`.trim()
  return { deny: why }
}
