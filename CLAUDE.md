# mail-app-cli

Go CLI over macOS Mail.app. Read `AGENTS.md` for release and surface-snapshot rules, `docs/` for the 2.0 CLI and TUI plans.

## Lessons

From the 2.0 CLI rework (PR #13), where the first pass needed 17 follow-up fix commits:

- Build from a fresh clone before pushing; a gitignore pattern swallowed the embedded skill file.
- Anchor gitignore binary names (`/name`); bare patterns match same-named directories anywhere.
- Never paste live inbox data into docs; fictionalize names, IDs, subjects, addresses.
- Never guess a mutation target; an ID missing from a healthy index fails closed.
- Verification must separate "read failed" from "absent"; errors collapsed into success.
- Once side effects happen, supplemental failures become notices, never replace the receipt.
- Validate everything that can fail after a mutation, including jq runtime, not just parse.
- Identity and not-found answers must bypass long-lived caches; stale lists misroute commands.
- Lossy key sanitization causes cache collisions; encode instead of stripping characters.
- Honor injected writers everywhere; TTY detection read os.Stdout while tests passed buffers.
- Make entrypoints idempotent; cobra globals leaked flags between in-process runs.
- Fallback paths must reuse the real resolver, not a hand-rolled approximation of it.
- Substring error classification is order-fragile; "executable not found" became not_found.
- Round-trip own outputs; `export` stdout envelope was not accepted by `import`.
- Inventories and snapshots must be complete; excluding help/completion by name hid surface.
- Every scope source (flag, env, config) must apply uniformly; documenting inconsistency is not fixing it.
- Fix a review finding fully, then re-run reviewers; several partial fixes shipped.
- Unit-test error paths with a fake osascript; live testing only exercised happy paths.
- Deferred findings need explicit rationale, not silent omission from the fix list.
