// The role's context (docs/SPEC.md §7.8): what a thread or coordinator
// gets back after /clear or compaction. The mod adds it as a context
// block of the conversation's first message, which Claude re-reads after
// /clear and compaction; the SessionStart command hook brings the same
// text, which the mod keeps as a fallback (and leaves in SessionStart's
// answer for now). Plain functions; the hooks live in register.ts.

import type { PromptContextBlock } from 'claude-code'

// BLOCK is the block's name: it renders under `# terminatr`.
export const BLOCK = 'terminatr'

// The server's context starts with the role's rules, whose first line is
// `tm skill <role> v<version>`.
const OURS = /^tm skill (thread|coordinator) v/

// splitContext parts a SessionStart answer's additionalContext into ours
// (the last, when several) and the rest, as given.
export function splitContext(all: readonly string[] | undefined): { ours: string; rest: string[] } {
  let ours = ''
  const rest: string[] = []
  for (const c of all ?? []) {
    if (OURS.test(c)) ours = c
    else rest.push(c)
  }
  return { ours, rest }
}

// withBlock is blocks with ours last, in place of an earlier one; none
// for empty text.
export function withBlock(blocks: readonly PromptContextBlock[], text: string): PromptContextBlock[] {
  const out = blocks.filter(b => b.name !== BLOCK)
  if (text) out.push({ name: BLOCK, text })
  return out
}
