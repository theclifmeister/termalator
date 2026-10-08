// The guard (docs/SPEC.md §8.6, Guard): the standing rules as checks on
// each tool call. The server sends the rules in force for the session
// (GET /v1/rules on the mod socket, internal/server/guard.go); judge
// matches one call against them and names the rule it breaks, with the
// sentence the model reads. It only ever refuses: a call no rule matches
// goes on to Claude's own permission check, as without the mod.
//
// Bash commands are split into simple commands on ; & | && || newlines,
// $( ) and backticks, and read shell-style (quotes, env assignments,
// sudo, git -C). That catches what agents type; the sandbox and the
// permission rules stay the backstop for what is written to hide.
// Plain functions of the rules and the call, for the tests.
//
// Agents without a mod get the same guard from its Go port
// (internal/guard), judged by the server on their PreToolUse hook. Both
// pass the shared vectors in tests/guard-vectors.ts: change them together.

// The rules as the server sends them (server.GuardRules).
export type Rules = {
  on: boolean
  role?: string
  rules?: readonly string[]
  home?: string
  cwd?: string
  writable?: readonly string[]
  worktrees?: string
  protected?: readonly string[]
  secrets?: readonly string[]
  // The agent's tools the guard judges, by name (its manifest's
  // [guard.tools]); a tool not listed is not judged.
  tools?: Readonly<Record<string, Tool>>
}

// What the guard knows of a tool: its kind (shell, write, patch, read,
// glob) and the fields of its input that kind reads.
export type Tool = { kind: string; fields: readonly string[] }

// A refusal: the rule, what the model reads, and a short account for
// the journal that carries no text of the call beyond a path or branch.
export type Denial = { rule: string; message: string; summary: string }

export const off: Rules = { on: false }

// rulesOf reads GET /v1/rules' body; anything else is the guard off.
export function rulesOf(text: string): Rules {
  try {
    const r = JSON.parse(text) as Rules
    return r && typeof r === 'object' && r.on === true && Array.isArray(r.rules) ? r : off
  } catch {
    return off
  }
}

// judge is the rule the call of tool with input breaks, or null. The
// tool is judged by its kind in r.tools, from the fields listed there.
export function judge(r: Rules, tool: string, input: Record<string, unknown>): Denial | null {
  if (!r.on || !r.rules?.length) return null
  const t = r.tools && Object.hasOwn(r.tools, tool) ? r.tools[tool] : undefined
  if (!t) return null
  const has = (id: string) => r.rules!.includes(id)
  const values = (t.fields ?? []).map(f => input[f]).filter((v): v is string => typeof v === 'string' && v !== '')
  const writable = (p: string) => (r.writable ?? []).some(w => under(abs(r, p), w))
  switch (t.kind) {
    case 'shell':
      for (const c of values) {
        const d = judgeBash(r, has, c)
        if (d) return d
      }
      return null
    case 'write':
      if (!has('worktree-only')) return null
      for (const p of values) if (!writable(p)) return deny(r, 'worktree-only', `${tool} of ${safe(abs(r, p))}`)
      return null
    case 'patch':
      // Each file the patch adds, updates, deletes or moves to is a write.
      if (!has('worktree-only')) return null
      for (const patch of values) {
        for (const p of patchPaths(patch)) if (!writable(p)) return deny(r, 'worktree-only', `${tool} of ${safe(abs(r, p))}`)
      }
      return null
    case 'read':
    case 'glob':
      // A glob's fields may be patterns: judged by the part before the
      // first wildcard.
      if (!has('credentials')) return null
      for (const p of values) if (isSecret(r, abs(r, p))) return deny(r, 'credentials', `${tool} of ${safe(abs(r, p))}`)
      return null
  }
  return null
}

// patchPaths are the files a Codex patch (*** Begin Patch …) writes.
export function patchPaths(patch: string): string[] {
  return [...patch.matchAll(/^\*\*\* (?:(?:Add|Update|Delete) File|Move to): (.+?)\s*$/gm)].map(m => m[1])
}

// The sentence the model reads for each rule; the end says what to do,
// which differs for a thread (its report) and the coordinator (the user).
function deny(r: Rules, rule: string, summary: string, target = ''): Denial {
  const tell = r.role === 'thread' ? 'say so in your report' : 'ask the user'
  const m: Record<string, string> = {
    'force-push': `force-pushing is not allowed here. Push without force; if the branch has diverged, merge instead of rewriting it, or ${tell}.`,
    'push-default': `nothing is pushed straight to ${target || 'the default branch'}: work lands through a pull request. Push your own branch and open a PR.`,
    'worktree-only': `a thread writes only inside its worktree (${r.writable?.[0] ?? ''}). Write the file there, or ${tell} what needs changing elsewhere.`,
    'delete-branch': `agents don't delete branches or worktrees; tm cleans them up once the work has merged. Leave them, and ${tell} if one is in the way.`,
    merge: `merging is the coordinator's job in this project (merge = "coordinator"). Leave the PR open with CI green and say so in your report.`,
    credentials: `credential files and tokens are not read here. If the task needs access you don't have, ${tell} exactly what is missing.`,
  }
  return { rule, message: `terminatr guard (${rule}): ${m[rule]}`, summary }
}

// judgeBash checks each simple command of a Bash command line.
function judgeBash(r: Rules, has: (id: string) => boolean, command: string): Denial | null {
  for (const words of commands(command)) {
    const argv = strip(words)
    if (argv.length === 0) continue
    const cmd = base(argv[0])
    const args = argv.slice(1)
    if (cmd === 'git') {
      const d = judgeGit(r, has, args)
      if (d) return d
    } else if (cmd === 'gh') {
      const d = judgeGh(r, has, args)
      if (d) return d
    } else if (cmd === 'az') {
      const d = judgeAz(r, has, args)
      if (d) return d
    } else if (HTTP_CLIENTS.has(cmd)) {
      const d = judgeHttp(r, has, args)
      if (d) return d
    } else if (cmd === 'rm' && has('delete-branch') && args.some(a => /^-[a-zA-Z]*[rR]/.test(a) || a === '--recursive')) {
      for (const a of args) {
        if (a.startsWith('-')) continue
        if (isWorktreeRoot(r, abs(r, a))) return deny(r, 'delete-branch', `rm -r of ${safe(abs(r, a))}`)
      }
    }
    if (has('credentials')) {
      const d = judgeSecret(r, cmd, args, words)
      if (d) return d
    }
  }
  return null
}

function judgeGit(r: Rules, has: (id: string) => boolean, args: string[]): Denial | null {
  // Global options before the subcommand.
  let i = 0
  while (i < args.length && args[i].startsWith('-')) {
    if (args[i] === '-C' || args[i] === '-c' || args[i] === '--git-dir' || args[i] === '--work-tree' || args[i] === '--namespace') i++
    i++
  }
  const sub = args[i]
  const rest = args.slice(i + 1)
  // long: one of the long options, with or without a value; flag: that
  // or the short one, alone or among others (-fu).
  const long = (...names: string[]) => rest.some(a => names.some(l => a === l || a.startsWith(l + '=')))
  const flag = (short: string, ...names: string[]) => long(...names) || rest.some(a => /^-[a-zA-Z]+$/.test(a) && a.includes(short))
  switch (sub) {
    case 'push': {
      if (has('force-push') && (flag('f', '--force', '--force-with-lease', '--force-if-includes', '--mirror') || rest.some(a => a.startsWith('+')))) {
        return deny(r, 'force-push', 'git push with force')
      }
      if (has('delete-branch') && (flag('d', '--delete', '--prune') || rest.some(a => a.startsWith(':') && a.length > 1))) {
        return deny(r, 'delete-branch', 'git push deleting a branch')
      }
      if (has('push-default')) {
        if (long('--all', '--branches', '--mirror')) return deny(r, 'push-default', 'git push --all')
        const pos = positionals(rest, ['-o', '--push-option', '--repo', '--receive-pack', '--exec'])
        for (const spec of pos.slice(1)) {
          const target = branchOf(spec.includes(':') ? spec.slice(spec.indexOf(':') + 1) : spec)
          if (target && (r.protected ?? []).includes(target)) return deny(r, 'push-default', `git push to ${safe(target)}`, target)
        }
      }
      return null
    }
    case 'branch':
      if (has('delete-branch') && flag('d', '--delete')) return deny(r, 'delete-branch', 'git branch --delete')
      if (has('delete-branch') && rest.some(a => /^-[a-zA-Z]*D/.test(a))) return deny(r, 'delete-branch', 'git branch -D')
      return null
    case 'worktree':
      if (has('delete-branch') && (rest[0] === 'remove' || rest[0] === 'prune')) return deny(r, 'delete-branch', `git worktree ${rest[0]}`)
      return null
    case 'update-ref':
      if (has('delete-branch') && flag('d', '--delete')) return deny(r, 'delete-branch', 'git update-ref -d')
      return null
    case 'credential':
      if (has('credentials') && rest[0] === 'fill') return deny(r, 'credentials', 'git credential fill')
      return null
  }
  return null
}

function judgeGh(r: Rules, has: (id: string) => boolean, args: string[]): Denial | null {
  const [a, b] = args
  if (a === 'pr' && b === 'merge' && has('merge')) return deny(r, 'merge', 'gh pr merge')
  if (a === 'repo' && b === 'delete' && has('delete-branch')) return deny(r, 'delete-branch', 'gh repo delete')
  if (a === 'auth' && has('credentials') && (b === 'token' || args.includes('--show-token') || args.includes('-t'))) {
    return deny(r, 'credentials', `gh auth ${b === 'token' ? 'token' : 'status --show-token'}`)
  }
  if (a === 'api') {
    const paths = args.slice(1).filter(x => !x.startsWith('-'))
    if (has('merge') && paths.some(p => /pulls\/[^/]+\/merge\/?$/.test(p))) return deny(r, 'merge', 'gh api merging a PR')
    const method = args.find((x, i) => i > 0 && (args[i - 1] === '-X' || args[i - 1] === '--method'))?.toUpperCase()
      ?? args.find(x => /^(-X|--method=)/.test(x) && x.length > 2)?.replace(/^(-X|--method=)/, '').toUpperCase()
    if (has('delete-branch') && method === 'DELETE' && paths.some(p => /git\/refs\/heads\//.test(p))) return deny(r, 'delete-branch', 'gh api deleting a branch')
  }
  return null
}

// az (the Azure CLI with its azure-devops extension) is judged like gh,
// with the same rules and sentences. The extension's verbs: completing a
// PR (`az repos pr update --status completed`, `--auto-complete`,
// `--bypass-policy`, also on create) is the merge; `--delete-source-branch`,
// `az repos delete` and `az repos ref delete` delete; the REST routes under
// az rest, az devops invoke and curl are held to the same, by method and
// route.
function judgeAz(r: Rules, has: (id: string) => boolean, args: string[]): Denial | null {
  // The command words come first; flags (any order, --x=v or --x v) after.
  let i = 0
  while (i < args.length && args[i].startsWith('-')) i += optionTakesValue(args[i]) ? 2 : 1
  const words: string[] = []
  for (; i < args.length && !args[i].startsWith('-'); i++) words.push(args[i].toLowerCase())
  const rest = args.slice(i)
  const [a, b, c] = words
  if (a === 'repos' && b === 'pr' && (c === 'update' || c === 'create')) {
    const completes = opts(rest, '--status').some(v => v.toLowerCase() === 'completed') ||
      truthy(rest, '--auto-complete') || truthy(rest, '--bypass-policy')
    if (has('merge') && completes) return deny(r, 'merge', `az repos pr ${c} completing a PR`)
    if (has('delete-branch') && truthy(rest, '--delete-source-branch')) return deny(r, 'delete-branch', `az repos pr ${c} --delete-source-branch`)
  }
  if (a === 'repos' && b === 'delete' && has('delete-branch')) return deny(r, 'delete-branch', 'az repos delete')
  if (a === 'repos' && b === 'ref' && c === 'delete' && has('delete-branch')) return deny(r, 'delete-branch', 'az repos ref delete')
  if (has('credentials')) {
    if (a === 'account' && b === 'get-access-token') return deny(r, 'credentials', 'az account get-access-token')
    if (a === 'devops' && b === 'login') return deny(r, 'credentials', 'az devops login')
  }
  if (a === 'rest') {
    // A method override header is the method the service acts on.
    const override = optsMulti(rest, '--headers').map(h => /^x-http-method-override[=:]\s*(\w+)/i.exec(h)?.[1]).find(Boolean)
    const method = override ?? opts(rest, '--method', '-m')[0] ?? 'GET'
    return judgeRoute(r, has, method, opts(rest, '--url', '--uri', '-u')[0] ?? '')
  }
  if (a === 'devops' && b === 'invoke') {
    const resource = (opts(rest, '--resource')[0] ?? '').toLowerCase()
    const method = opts(rest, '--http-method')[0] ?? 'GET'
    const params = optsMulti(rest, '--route-parameters').map(p => p.toLowerCase())
    const area = (opts(rest, '--area')[0] ?? 'git').toLowerCase()
    if (area !== 'git') return null
    if (!isWrite(method)) return null
    if (has('merge') && (resource === 'merges' || (resource === 'pullrequests' && params.some(p => p.startsWith('pullrequestid='))))) {
      return deny(r, 'merge', `az devops invoke ${safe(method.toUpperCase())} on ${safe(resource)}`)
    }
    if (has('delete-branch') && (resource === 'refs' || (resource === 'repositories' && method.toUpperCase() === 'DELETE'))) {
      return deny(r, 'delete-branch', `az devops invoke ${safe(method.toUpperCase())} on ${safe(resource)}`)
    }
  }
  return null
}

// Clients that speak HTTP; given an Azure DevOps address they are judged
// by route and method as az rest is, and by what they authenticate with.
const HTTP_CLIENTS = new Set(['curl', 'wget', 'http', 'https', 'xh'])

const AZURE_HOST = /(^|[/@.])(dev\.azure\.com|[a-z0-9-]+\.visualstudio\.com|vssps\.dev\.azure\.com)([/:?#]|$)/i

function judgeHttp(r: Rules, has: (id: string) => boolean, args: string[]): Denial | null {
  const url = args.find(a => AZURE_HOST.test(a))
  if (!url) return null
  if (has('credentials') && args.some(a => /^(-u.*|--user(=.*)?|--oauth2-bearer(=.*)?)$/.test(a) || /authorization:|AZURE_DEVOPS_EXT_PAT/i.test(a))) {
    return deny(r, 'credentials', 'an HTTP call to Azure DevOps with credentials')
  }
  let method = ''
  for (let i = 0; i < args.length; i++) {
    const a = args[i]
    if (a === '-X' || a === '--request') method = args[i + 1] ?? ''
    else if (a.startsWith('--request=')) method = a.slice(10)
    else if (/^-X./.test(a)) method = a.slice(2)
  }
  return judgeRoute(r, has, method || 'GET', url)
}

// judgeRoute: a write method on an Azure DevOps Git route that completes
// a PR or deletes a ref or a repository.
function judgeRoute(r: Rules, has: (id: string) => boolean, method: string, url: string): Denial | null {
  const m = method.toUpperCase()
  if (!isWrite(m)) return null
  const path = url.split(/[?#]/)[0].toLowerCase().replace(/\/+$/, '')
  if (!path.includes('/_apis/git/')) return null
  if (has('merge') && (/\/pullrequests\/[^/]+$/.test(path) || /\/merges$/.test(path))) {
    return deny(r, 'merge', `${safe(m)} on an Azure DevOps pull request`)
  }
  if (has('delete-branch') && (/\/refs$/.test(path) || (m === 'DELETE' && /\/repositories\/[^/]+$/.test(path)))) {
    return deny(r, 'delete-branch', `${safe(m)} on an Azure DevOps ref or repository`)
  }
  return null
}

function isWrite(method: string): boolean {
  return !['GET', 'HEAD', 'OPTIONS', ''].includes(method.toUpperCase())
}

// az's global options that take a value before the command words.
function optionTakesValue(a: string): boolean {
  return ['--output', '-o', '--query', '--subscription', '--organization', '--org', '--project', '-p'].includes(a)
}

// opts: the values of the option under any of names, as --x v or --x=v.
function opts(args: string[], ...names: string[]): string[] {
  const out: string[] = []
  for (let i = 0; i < args.length; i++) {
    for (const n of names) {
      if (args[i] === n) out.push(args[i + 1] !== undefined && !args[i + 1].startsWith('-') ? args[i + 1] : '')
      else if (args[i].startsWith(n + '=')) out.push(args[i].slice(n.length + 1))
    }
  }
  return out
}

// optsMulti: every value after the option up to the next option, for
// those that take several (--route-parameters a=1 b=2).
function optsMulti(args: string[], name: string): string[] {
  const out: string[] = []
  for (let i = 0; i < args.length; i++) {
    if (args[i] === name) {
      for (let j = i + 1; j < args.length && !args[j].startsWith('-'); j++) out.push(args[j])
    } else if (args[i].startsWith(name + '=')) out.push(args[i].slice(name.length + 1))
  }
  return out
}

// truthy: a boolean option given true, or bare (az takes true/false, and
// a bare flag counts as given).
function truthy(args: string[], name: string): boolean {
  return opts(args, name).some(v => !['false', 'no', 'n', '0', 'off'].includes(v.toLowerCase()))
}

// Commands that print or copy what they are given.
const READERS = new Set([
  'cat', 'head', 'tail', 'less', 'more', 'bat', 'nl', 'tac', 'cp', 'mv', 'scp', 'rsync', 'base64', 'xxd', 'od',
  'hexdump', 'strings', 'grep', 'egrep', 'fgrep', 'rg', 'ag', 'awk', 'sed', 'cut', 'sort', 'uniq', 'tar', 'zip',
  'gzip', 'openssl', 'gpg', 'jq', 'yq', 'diff', 'cmp', 'source', '.', 'curl', 'dd',
])

// Environment variable names that look like they hold a secret.
const SECRET_NAME = /token|secret|key|password|passwd|credential|(^|_)pat$/i

function judgeSecret(r: Rules, cmd: string, args: string[], words: string[]): Denial | null {
  if (cmd === 'security' && /^(find-(generic|internet)-password|dump-keychain|export)$/.test(args[0] ?? '')) {
    return deny(r, 'credentials', `security ${args[0]}`)
  }
  if (cmd === 'printenv') {
    // printenv NAME... prints only what it is asked for: fine unless a
    // name looks secret. With no name it prints everything.
    const names = args.filter(a => !a.startsWith('-'))
    if (!names.length) return deny(r, 'credentials', 'printenv printing the environment')
    const secret = names.find(n => SECRET_NAME.test(n))
    if (secret) return deny(r, 'credentials', `printenv ${safe(secret)}`)
  } else if (cmd === 'env' && args.every(a => a.startsWith('-'))) {
    return deny(r, 'credentials', 'env printing the environment')
  }
  // echo and the like given a secret variable ($AZURE_DEVOPS_EXT_PAT).
  if (cmd === 'echo' || cmd === 'printf' || cmd === 'print' || READERS.has(cmd)) {
    for (const w of words) {
      const v = /\$\{?([A-Za-z_][A-Za-z0-9_]*)/.exec(w)?.[1]
      if (v && /^AZURE_DEVOPS_EXT_PAT$|^AZURE_DEVOPS_.*(PAT|TOKEN)$/i.test(v)) return deny(r, 'credentials', `${cmd} of ${safe(v)}`)
    }
  }
  // A reader given a secret, or a secret redirected in.
  for (let i = 0; i < words.length; i++) {
    const w = words[i]
    const redirected = w === '<' || words[i - 1] === '<'
    const path = w.startsWith('<') && w.length > 1 ? w.slice(1) : w
    if ((READERS.has(cmd) || redirected) && !path.startsWith('-') && path !== '<' && isSecret(r, abs(r, path))) {
      return deny(r, 'credentials', `${cmd} of ${safe(abs(r, path))}`)
    }
  }
  return null
}

// commands splits a command line into simple commands, each a list of
// words with quotes removed; redirections stay words ("<", ">").
export function commands(line: string): string[][] {
  const out: string[][] = []
  let words: string[] = []
  let word = ''
  let inWord = false
  const endWord = () => {
    if (inWord) words.push(word)
    word = ''
    inWord = false
  }
  const endCommand = () => {
    endWord()
    if (words.length) out.push(words)
    words = []
  }
  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (c === '\\' && i + 1 < line.length) {
      if (line[i + 1] !== '\n') {
        word += line[i + 1]
        inWord = true
      }
      i++
    } else if (c === "'") {
      const j = line.indexOf("'", i + 1)
      const end = j < 0 ? line.length : j
      word += line.slice(i + 1, end)
      inWord = true
      i = end
    } else if (c === '"') {
      // Inside double quotes a $( ) or backtick still runs: its inside
      // is read as commands too.
      let j = i + 1
      let s = ''
      while (j < line.length && line[j] !== '"') {
        if (line[j] === '\\' && j + 1 < line.length) {
          s += line[j + 1]
          j += 2
          continue
        }
        s += line[j++]
      }
      for (const m of s.matchAll(/\$\(([^)]*)\)|`([^`]*)`/g)) out.push(...commands(m[1] ?? m[2] ?? ''))
      word += s
      inWord = true
      i = j
    } else if (c === ' ' || c === '\t') {
      endWord()
    } else if (c === ';' || c === '&' || c === '|' || c === '\n' || c === '(' || c === ')' || c === '`' || c === '{' || c === '}') {
      endCommand()
    } else if (c === '$' && line[i + 1] === '(') {
      endCommand()
      i++
    } else if (c === '<' || c === '>') {
      endWord()
      words.push(c)
    } else {
      word += c
      inWord = true
    }
  }
  endCommand()
  return out
}

// strip drops what runs a command without being it: env assignments,
// sudo, command, exec, nohup, time, env with assignments.
function strip(words: string[]): string[] {
  let i = 0
  for (;;) {
    const w = words[i]
    if (w === undefined) return []
    if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(w)) i++
    else if (['sudo', 'command', 'exec', 'nohup', 'time', 'builtin'].includes(w)) i++
    else if (w === 'env' && words[i + 1] !== undefined && (/=/.test(words[i + 1]) || words[i + 1].startsWith('-'))) {
      i++
      while (words[i] !== undefined && (/=/.test(words[i]) || words[i].startsWith('-'))) i++
    } else break
  }
  return words.slice(i)
}

// positionals are args without their options, valued options skipping
// their value.
function positionals(args: string[], valued: string[]): string[] {
  const out: string[] = []
  for (let i = 0; i < args.length; i++) {
    if (valued.includes(args[i])) i++
    else if (args[i] === '<' || args[i] === '>') i++
    else if (!args[i].startsWith('-')) out.push(args[i])
  }
  return out
}

function branchOf(ref: string): string {
  return ref.replace(/^\+/, '').replace(/^refs\/heads\//, '')
}

function base(p: string): string {
  return p.slice(p.lastIndexOf('/') + 1)
}

// abs is p made absolute and clean: ~ is home, a relative path is from
// the session's folder.
export function abs(r: Rules, p: string): string {
  if (p === '~' || p.startsWith('~/')) p = (r.home ?? '') + p.slice(1)
  else if (p.startsWith('$HOME/') || p === '$HOME') p = (r.home ?? '') + p.slice(5)
  if (!p.startsWith('/')) p = (r.cwd ?? r.writable?.[0] ?? '/') + '/' + p
  const parts: string[] = []
  for (const s of p.split('/')) {
    if (s === '' || s === '.') continue
    if (s === '..') parts.pop()
    else parts.push(s)
  }
  return '/' + parts.join('/')
}

function under(p: string, root: string): boolean {
  return root !== '' && (p === root || p.startsWith(root.endsWith('/') ? root : root + '/'))
}

// isSecret: p is a credential store or inside one; a glob counts by the
// part before its first wildcard.
function isSecret(r: Rules, p: string): boolean {
  const fixed = p.replace(/[*?[{].*$/, '').replace(/\/$/, '') || '/'
  return (r.secrets ?? []).some(s => under(fixed, s) || (fixed !== p && under(s, fixed) && fixed !== '/' && fixed !== r.home))
}

// isWorktreeRoot: removing p takes a worktree with it: the thread's own,
// tm's worktrees folder, a project's folder in it or a worktree in that.
function isWorktreeRoot(r: Rules, p: string): boolean {
  const own = r.role === 'thread' ? r.writable?.[0] : undefined
  if (own && under(own, p)) return true
  const wt = r.worktrees
  if (!wt) return false
  if (under(wt, p)) return true
  if (!under(p, wt)) return false
  return p.slice(wt.length + 1).split('/').length <= 2
}

// safe keeps a path or branch to the characters a journal line needs.
function safe(s: string): string {
  return s.replace(/[^A-Za-z0-9._/~@+-]/g, '?').slice(0, 100)
}
