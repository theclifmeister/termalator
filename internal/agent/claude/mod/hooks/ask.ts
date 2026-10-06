// Question menus through tm (docs/SPEC.md §3.3, Ask): the mod sends the
// AskUserQuestion menu it sees open to the server with `tm session ask`,
// so `tm thread show`, `tm context` and the coordinator see its options,
// and `tm thread answer` can answer it by label or with free text. Plain
// functions of the tool's input and tm's output, for the tests.

// A question of the AskUserQuestion tool, as far as tm reads it.
export type AskQuestion = {
  question: string
  header?: string
  multiSelect?: boolean
  options?: readonly { label: string; description?: string }[]
}

// question is the menu as `tm session ask` reads it on stdin
// (proto.Question).
export function question(tool: string, questions: readonly AskQuestion[]): string {
  return JSON.stringify({
    tool,
    questions: questions.map(q => ({
      question: q.question,
      header: q.header ?? '',
      multiSelect: q.multiSelect === true,
      options: (q.options ?? []).map(o => ({ label: o.label, description: o.description ?? '' })),
    })),
  })
}

// answers reads what `tm session ask` printed: the answers by question
// text, or null when it printed none (the question closed unanswered).
export function answers(out: string): Record<string, string> | null {
  const text = out.trim()
  if (!text) return null
  try {
    const got = JSON.parse(text) as unknown
    if (!got || typeof got !== 'object' || Array.isArray(got)) return null
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(got)) if (typeof v === 'string') out[k] = v
    return Object.keys(out).length > 0 ? out : null
  } catch {
    return null
  }
}
