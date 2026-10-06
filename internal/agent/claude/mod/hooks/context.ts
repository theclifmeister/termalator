// The role's context (docs/SPEC.md §7.8): what a thread or coordinator
// gets back after /clear or compaction. The mod adds it as a context
// block of the conversation's first message, which Claude re-reads after
// /clear and compaction; the SessionStart command hook says only where
// it is. Plain functions; the hooks live in register.ts.

import type { PromptContextBlock } from 'claude-code'

// BLOCK is the block's name: it renders under `# terminatr`.
export const BLOCK = 'terminatr'

// withBlock is blocks with ours last, in place of an earlier one; none
// for empty text.
export function withBlock(blocks: readonly PromptContextBlock[], text: string): PromptContextBlock[] {
  const out = blocks.filter(b => b.name !== BLOCK)
  if (text) out.push({ name: BLOCK, text })
  return out
}
