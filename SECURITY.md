# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub: on the repository, open the **Security** tab and choose
**Report a vulnerability** (https://github.com/yugsoni21/unbroke/security/advisories/new).
Include what you found, how to reproduce it, and what you think the impact is. We aim to reply
within a week and will credit you in the fix unless you prefer not to be named.

## What Unbroke does, and what it protects

Unbroke is a developer tool that runs on your own machine. Knowing what it is meant to do helps
separate real problems from by-design behaviour.

- **It runs the test command you give it, on purpose.** `unbroke fix -test "npm test"` (and the
  "test command" box in the web interface) is passed to `sh -c` with your privileges, exactly as if
  you had typed it in your terminal. That is the feature. Only run `fix` on code you trust to run tests
  for, and treat the test command like any command you type.
- **That command and git hooks do not receive Unbroke's own settings.** Environment variables that
  start with `UNBROKE_` (including `UNBROKE_ANTHROPIC_API_KEY`) are removed before the test command
  and git are started, so a project's tests or hooks cannot read your model key.
- **Scanning never executes the code it reads.** It reads source text only.
- **The local web interface** (`unbroke ui`) listens on localhost only, checks the Host header (which
  defeats DNS rebinding), requires a random per-run token on every API call, and only serves its page to
  someone holding the launch key printed in the terminal (the link ends in `?k=...`), so another user
  or process on the same machine cannot load it and use it to run commands. It serves a strict
  Content-Security-Policy.
- **Branch names are validated** before use, and file paths are passed to git after `--`.
- **The fix agent edits files only with literal exact-once replacements, never touches files with your
  uncommitted changes, never pushes unless you pass `-open-pr`, and undoes everything if the tests
  fail.**

Out of scope: anything that requires you to already be running attacker-supplied commands as yourself.
