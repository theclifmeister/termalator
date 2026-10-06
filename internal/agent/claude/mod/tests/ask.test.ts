import { expect, mock, test } from 'claude-code/testing'

import { answers, question } from '../hooks/ask'

const menu = [
  { question: 'Which colour?', header: 'Colour', multiSelect: false, options: [{ label: 'Red', description: 'r' }, { label: 'Blue', description: 'b' }] },
  { question: 'Which fruits?', header: 'Fruit', multiSelect: true, options: [{ label: 'Apple', description: 'a' }, { label: 'Pear', description: 'p' }] },
]

test('question is the menu as tm session ask reads it', () => {
  expect(JSON.parse(question('AskUserQuestion', menu))).toEqual({
    tool: 'AskUserQuestion',
    questions: [
      { question: 'Which colour?', header: 'Colour', multiSelect: false, options: [{ label: 'Red', description: 'r' }, { label: 'Blue', description: 'b' }] },
      { question: 'Which fruits?', header: 'Fruit', multiSelect: true, options: [{ label: 'Apple', description: 'a' }, { label: 'Pear', description: 'p' }] },
    ],
  })
})

test('answers reads what tm session ask printed', () => {
  expect(answers('{"Which colour?":"Blue","Which fruits?":"Apple, Pear"}\n')).toEqual({ 'Which colour?': 'Blue', 'Which fruits?': 'Apple, Pear' })
  expect(answers('')).toBe(null)
  expect(answers('not json')).toBe(null)
  expect(answers('{}')).toBe(null)
})

test('tm answers the menu: the call returns its answers', async ($, on) => {
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7' })
  let argv: readonly string[] = []
  let input = ''
  on('process.spawn', async function* (_$, e) {
    argv = e.argv
    input = e.input ?? ''
    yield { stream: 'stdout' as const, text: '{"Which colour?":"Blue",' }
    yield { stream: 'stdout' as const, text: '"Which fruits?":"Pear"}\n' }
    return { value: { code: 0, signal: null } }
  })
  // The menu: open until the call is answered some other way.
  on('tool.call', { tool: 'AskUserQuestion' }, () => new Promise(() => {}))

  const r = await $.tool.call({ tool: 'AskUserQuestion', questions: menu })
  expect(argv).toEqual(['/opt/tm', 'session', 'ask', 's-7'])
  expect(JSON.parse(input).questions[1].multiSelect).toBe(true)
  expect((r as { result: { answers: unknown } }).result.answers).toEqual({ 'Which colour?': 'Blue', 'Which fruits?': 'Pear' })
})

test('the user answers in the pane first: their answer stands', async ($, on) => {
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7' })
  on('process.spawn', async function* () {
    await new Promise(() => {})
    return { value: { code: 0, signal: null } }
  })
  const mine = { result: { questions: menu, answers: { 'Which colour?': 'Red', 'Which fruits?': 'Apple' } } }
  on('tool.call', { tool: 'AskUserQuestion' }, async () => mine)

  const r = await $.tool.call({ tool: 'AskUserQuestion', questions: menu })
  expect((r as { result: { answers: unknown } }).result.answers).toEqual(mine.result.answers)
})

test('tm closes the question unanswered: the menu stays the user\'s', async ($, on) => {
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7' })
  let closed = () => {}
  const isClosed = new Promise<void>(res => (closed = res))
  on('process.spawn', async function* () {
    closed()
    return { value: { code: 1, signal: null } }
  })
  // The user answers once tm has given up.
  const mine = { result: { questions: menu, answers: { 'Which colour?': 'teal' } } }
  on('tool.call', { tool: 'AskUserQuestion' }, async () => {
    await isClosed
    return mine
  })

  const r = await $.tool.call({ tool: 'AskUserQuestion', questions: menu })
  expect((r as { result: { answers: unknown } }).result.answers).toEqual(mine.result.answers)
})

test('outside a terminatr session the menu is left alone', async ($, on) => {
  mock.env(on, {})
  let spawned = false
  on('process.spawn', async function* () {
    spawned = true
    return { value: { code: 0, signal: null } }
  })
  on('tool.call', { tool: 'AskUserQuestion' }, async () => ({ result: { questions: menu, answers: { 'Which colour?': 'Red' } } }))
  const r = await $.tool.call({ tool: 'AskUserQuestion', questions: menu })
  expect((r as { result: { answers: unknown } }).result.answers).toEqual({ 'Which colour?': 'Red' })
  expect(spawned).toBe(false)
})
