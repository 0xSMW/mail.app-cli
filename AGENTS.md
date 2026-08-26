Release content must be user-facing only: never include verification details, private inbox/message/account evidence, or other internal proof.

`.surface` is a snapshot of every command and flag. `go test ./cmd` fails when it is stale; regenerate with `UPDATE_SURFACE=1 go test ./cmd -run TestSurfaceSnapshot` and commit the diff so reviewers see surface changes.

Plans live in `docs/` (CLI ergonomics, TUI) and `plans/` (reliability roadmap).
