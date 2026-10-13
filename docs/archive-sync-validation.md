# Controlled archive and synchronization validation

`scripts/archive-sync-check.py` checks one deliberately prepared, disposable,
fixture sent between the user's own configured accounts, or to the same account,
in a selected receiving account. It invokes only `mail-app-cli` for
Mail operations, serially. Its default mode reads the fixture and previews the
archive; it does not send, mark, archive, synchronize, or clean up mail.

An applied run requires both `--apply` and `--confirm-disposable`. The latter is
the operator's assertion that the selected message exists solely for this test.
Never use an ordinary inbox message. The script requires the exact unique fixture
subject, a sender address belonging to an enabled own configured sender account,
and a positive live
INBOX baseline for the supplied local ID before it attempts the mutation.

## Prepare the fixture

Generate the exact subject without opening Mail:

```sh
python3 scripts/archive-sync-check.py --new-marker
```

The output has the form `mail-app-cli-archive-fixture-<UUID>`. Keep that exact
subject. Use a separate UUID and separate fixture for each comparison run.

The CLI's `import messages` command validates exported JSON only; it cannot
create a message in Mail. The test harness therefore accepts an existing
disposable fixture and does not attempt native import or send mail. An operator
may prepare a message to an own configured receiving account under separate sending authorization using
the CLI's `send --dry-run` preview followed by `send` to that receiving account's own
address. Do not use external recipients. Sending between two own accounts can
produce an incoming fixture without a Sent label on the receiving account.
A submitted send receipt is insufficient:
observe actual delivery of the unique fixture to INBOX before running this check.

Read the selected account and resolve the fixture's current INBOX ID with
explicit serial commands:

```sh
mail-app-cli accounts show 'Fixture Account' --json
mail-app-cli messages scan INBOX 'All Mail' -a 'Fixture Account' \
  --query 'mail-app-cli-archive-fixture-REPLACE-WITH-UUID' --limit 0 --json
```

The account and destination must match live `mailboxes list -a ACCOUNT
--no-cache --json` names exactly. Gmail's usual archive destination is `All Mail`;
pass `--destination Archive` only for an account whose archive destination is
actually named `Archive`.

## Run the preview and the applied check

Use a new, absolute evidence directory outside the repository for every run.
The script creates it with private permissions and refuses an existing path.
Account names, message identities, receipts, and command output belong in this
private directory; do not copy them into release notes or other public release
content.

```sh
python3 scripts/archive-sync-check.py \
  --cli /absolute/path/to/mail-app-cli \
  --account 'Fixture Account' \
  --message-id 123456 \
  --marker 'mail-app-cli-archive-fixture-REPLACE-WITH-UUID' \
  --evidence-dir /absolute/private/path/archive-sync-preview
```

Replace the example ID and marker with the freshly observed disposable fixture.
If the fixture was sent from a different own configured account, add
`--sender-account 'Own Sender Account'` to both preview and applied commands.
The harness verifies that account is enabled and that the fixture's sender address
belongs to it. Omitting this option requires the sender to belong to `--account`.
The preview must finish with `dry-run-passed`. Then run the same parameters with
a different evidence directory and the explicit opt-in flags:

```sh
python3 scripts/archive-sync-check.py \
  --cli /absolute/path/to/mail-app-cli \
  --account 'Fixture Account' \
  --message-id 123456 \
  --marker 'mail-app-cli-archive-fixture-REPLACE-WITH-UUID' \
  --evidence-dir /absolute/private/path/archive-sync-applied \
  --apply --confirm-disposable
```

An applied run performs these gates:

1. Live account and mailbox validation, complete fixture scans, full stable
   identity capture, dry-run receipt inspection, and a fresh INBOX check.
2. One archive operation with `--mark-read --verify --settle 10s
   --verify-budget 30s`, saving the final receipt and durable journal privately.
3. A complete live scan proving absence in INBOX and one read destination copy.
4. An explicit `sync -a ACCOUNT -m INBOX --wait --timeout 20`. The sync response,
   including its reported scope, is recorded. Scope depends on the tested binary:
   older releases may request a global new-mail check despite `-a`.
5. Repeated complete INBOX/destination scans and read-only `messages reconcile`
   calls against the archive receipt over at least 60 seconds after sync.
   Every observation must prove source absence and destination read/presence.

The default pause interval is 15 seconds, each command is bounded to 60 seconds,
and the whole run has a 300-second budget. `--settle-seconds` may increase the
observation window to 300 seconds, `--interval-seconds` is limited to 30 seconds,
and `--command-timeout` cannot exceed 60 seconds. Set `--budget-seconds` high
enough for the selected window and command overhead (maximum 900 seconds).
Two stable count samples from `sync --wait` do not prove archive success; the
fixture scans and receipt reconciliation supply the required identity evidence.
The final scan/reconciliation pair begins at least 60 seconds after sync completed;
time spent waiting for an earlier command to return cannot satisfy that final gate.

The stable identity is the complete sender, exact unique subject, sent date
normalized to UTC, and message size. This intentionally survives local-ID
changes. The CLI's saved receipt may also enrich it with the real RFC Message-ID;
the script never treats a numeric local ID as an RFC identity. A duplicate marker,
changed identity, incomplete scan, unread destination, missing destination,
recurrence in INBOX, command failure, or timeout fails the run.

## Read results and handle failure

Each invocation records stdout, stderr, start/end UTC timestamps, exit code, and
timeout information in numbered files. `result.json` records the validated
identity, whether an archive was attempted, sync response, and successful
observation timestamps. `archive-receipt.json` and its `.journal.jsonl` sidecar
retain the mutation evidence. Exit zero with `status: passed` means all recorded
gates passed over the observation window; it does not guarantee that a provider
cannot change state later. Exit zero with `dry-run-passed` proves only the preview.
On command timeout or interruption, the harness sends SIGTERM to its dedicated
CLI process group and allows up to five seconds for the CLI's Go cancellation
handler to stop its bridge processes. Those bridges use separate process groups;
killing the CLI group alone cannot guarantee their cleanup. The private step
record includes the actual `stoppedExitCode` and `shutdownMode`. If the CLI does
not exit within that grace period, the harness hard-stops its own CLI group and
waits up to two seconds to reap it, marking bridge cleanup unconfirmed. It starts
no further Mail operations after an uncertain result.
The result remains uncertain even if the process exits after applying the move.

The script never retries a failed or uncertain archive and never deletes the
fixture. A passed run leaves the fixture read in the archive destination. A failed
or interrupted run leaves the fixture wherever Mail last placed it. Inspect the
saved step logs and journal before deciding what to do next. If the receipt has
complete relocation scopes and identity, a separate serial read-only check is:

```sh
mail-app-cli messages reconcile /absolute/private/path/archive-receipt.json \
  --settle 2s --verify-budget 20s --json
```

Do not replay the original ID after a timeout, recurrence, or ID change. Do not
replace an archive failure with deletion. Keep private evidence for diagnosis.

## Offline validation

Syntax and argument handling can be checked without opening Mail:

```sh
python3 -c "import ast, pathlib; ast.parse(pathlib.Path('scripts/archive-sync-check.py').read_text())"
python3 scripts/archive-sync-check.py --help
python3 scripts/archive-sync-check.py --new-marker
python3 scripts/test_archive_sync_check.py
```

These checks do not establish native behavior. Run the opt-in recipe only with
the user's authorization for the disposable fixture mutation and synchronization.
The offline tests mock all Mail operations and exercise the applied sequence,
preview isolation, recurrence, invalid identity/coverage, delayed final sampling,
and timeout evidence/signal ordering. A separate technical subprocess shim
creates an owned child in a detached process group and handles CLI SIGTERM by
stopping that child; the test checks that both owned processes exited. It does
not invoke Mail or prove cleanup of an unresponsive real bridge after hard-stop
escalation.
