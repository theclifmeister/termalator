# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: [open a security advisory](https://github.com/theclifmeister/terminatr/security/advisories/new) (Security tab → "Report a vulnerability"). Don't open a public issue or pull request for it.

Include what you found, how to reproduce it, and which version or commit you tested. You should get an answer within a week. Once a fix is released, the advisory is published with credit to you, unless you'd rather stay anonymous.

## Supported versions

terminatr is pre-alpha. Only the latest release, and `main`, get security fixes.

## Scope

tm runs coding agents with your user's permissions, and its server listens on a unix socket that only your user can open. Reports that matter most:

- another local user, or a process outside your account, reaching the server's socket or a session;
- a thread agent getting around the access rules that tm sets up (reading or writing project state it shouldn't, see docs/SPEC.md);
- terminal escape sequences from a session that affect the terminal tm runs in, outside that session's pane;
- anything in the release archives or the install path that could deliver code other than what was built from this repository.

What an agent does with the permissions you grant it (for example yolo mode) is up to that agent and your settings, not a tm vulnerability.
