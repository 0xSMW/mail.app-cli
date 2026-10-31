# Gmail labels

`labels` is an explicit Gmail provider adapter for a Mail.app account. It lists provider labels, resolves one Mail.app message to its Gmail identity, and adds or removes an existing user label or `IMPORTANT`. It reports provider confirmation separately from Mail.app's local view.

Use the Mail.app account name with `--account` (`-a`). The selected account must be enabled, use IMAP, and expose the email address of the Gmail account you authorize. Account names resolve exactly. The adapter compares the authenticated Gmail profile with the selected account's actual email addresses before operating.

Use `--email <address>` on any `labels` command to select the Gmail provider address when Mail.app lists aliases before the primary address. The selector must match an actual address configured on the selected Mail.app account. Without it, operational commands use the single credential set found across all of that account's addresses. If several addresses have credentials, the command requires `--email`; a Keychain read error stops selection. Authorization defaults to the account's first address, so pass `--email` when connecting a different address from that same account.

## Connect an account

For a source build, use the same signing identity on every update so macOS
Keychain recognizes the CLI across builds:

```sh
MAIL_APP_CLI_SIGN_IDENTITY="Developer ID Application: Your Team (...)" make install
```

The build uses the stable identifier `ai.smw.mail-app-cli`. `make build` accepts
the same environment variable. Authorize or import credentials through the
installed signed binary. Replacing it with an ad-hoc build changes its security
identity; routine credential reads return an access error rather than opening
a password dialog. A locked login Keychain must be unlocked explicitly.

Create a desktop OAuth client with the Gmail API enabled and grant `https://www.googleapis.com/auth/gmail.modify`. A full mail scope also works. Connect using the downloaded desktop client JSON:

```sh
mail-app-cli labels auth connect -a "Work" --client-secret /private/path/client-secret.json
mail-app-cli labels auth connect -a "Work" --email primary@example.com --client-secret /private/path/client-secret.json
```

The command opens browser authorization and accepts the response on a temporary loopback listener. Credentials are saved to macOS Keychain only after the Gmail profile matches the selected Mail.app account.

Use a stable signed build for consistent macOS Keychain access across updates. Normal credential reads run without prompting; if access requires interaction, the command returns an actionable error. Reconnect or explicitly import credentials with the installed signed build to establish its access.

If you already have authorized credentials, import a private JSON file:

```sh
mail-app-cli labels auth import -a "Work" --credentials /private/path/credentials.json
```

The import format uses standard OAuth field names:

```json
{
  "client_id": "your-desktop-client-id",
  "client_secret": "your-desktop-client-secret",
  "refresh_token": "your-refresh-token",
  "scope": "https://www.googleapis.com/auth/gmail.modify"
}
```

`client_id` and `refresh_token` are required. `client_secret` is optional for clients that do not require one. Optional fields include `access_token` and `expiry` (RFC 3339). If `scope` is omitted, a fresh OAuth refresh response must establish the required scope. The standard `authorized_user` format's extra `type` field is accepted. Keep the file private; pass its path, never token values, on the command line. Commands return account and connection status without exposing credentials.

## Inspect and change labels

```sh
mail-app-cli labels list -a "Work" --json
mail-app-cli labels show 12345 -a "Work" --json
mail-app-cli labels remove 12345 -a "Work" --label IMPORTANT --dry-run --json
mail-app-cli labels remove 12345 -a "Work" --label IMPORTANT --json
mail-app-cli labels add 12345 -a "Work" --label "Projects/Current" --json
mail-app-cli labels remove 12345 12346 12347 -a "Work" --label IMPORTANT --dry-run --json
mail-app-cli labels remove 12345 12346 12347 -a "Work" --label IMPORTANT --json
```

The message argument is the numeric Mail.app ID from list or search output. If you omit `--mailbox`, the CLI locates the message. Pass `--mailbox` (`-m`) when you need to address a specific Mail.app location. A numeric local ID is never sent as a Gmail message ID: resolution requires a true RFC Message-ID and independent header checks, and fails if more than one provider message matches.

`--label` accepts a canonical label ID from `labels list` or an exact full label name, including the complete parent path for nested labels. `IMPORTANT` and existing user labels can be changed. Other system labels have their own semantics: use the existing archive, move, read, flag, draft, and delete commands where appropriate. All Mail represents messages outside Spam and Trash; it is not a user label to add or remove.

`--dry-run` resolves the account, provider message, label, and current memberships without changing labels. Provider credentials and access to Mail.app are still needed. A real change always reads fresh provider state afterward and checks the exact requested label delta, including preservation of unrelated labels. There is no flag to disable this verification.

### Several messages

`show`, `add`, and `remove` accept several message IDs. Each ID must be numeric and appear once. The CLI resolves every ID before it contacts Gmail. If one ID is missing, nothing runs. All messages must be in one Mail.app account; for messages in different accounts, run the command once per account.

Messages run one at a time in argument order. Each change writes its own receipt and makes its own fresh provider read. The run stops at the first failure. Later IDs are reported as `not_attempted` and are never read or changed.

With one ID, the result keeps the single-message shape above, so `.data.status` still works. With several IDs, `data` is an aggregate:

```json
{
  "action": "remove",
  "label": "IMPORTANT",
  "dryRun": false,
  "matched": 3,
  "attempted": 2,
  "succeeded": 1,
  "failed": 1,
  "notAttempted": 1,
  "items": [
    {"messageId": "12345", "status": "changed_verified", "providerConfirmed": true, "mailStatus": "verified", "receiptPath": "/private/path/receipt-1.jsonl", "error": "", "result": {}},
    {"messageId": "12346", "status": "unknown", "providerConfirmed": false, "mailStatus": "pending", "receiptPath": "/private/path/receipt-2.jsonl", "error": "Gmail readback unavailable", "result": {}},
    {"messageId": "12347", "status": "not_attempted"}
  ]
}
```

`result` holds the full single-message result for that ID; it is shortened here. `label` is omitted for `show`. `succeeded` counts `changed_verified`, `noop`, `dry_run`, and `observed`. The command exits 0 only when `failed` is 0. Otherwise it returns the failed message's error code, or `mutation_failed` (exit 6) when that message has a receipt to reconcile. The aggregate data is written in both cases.

## Receipts and reconciliation

A dispatched label change writes a durable private receipt and returns its `receiptPath`. The JSON result includes the provider identity, before and after label IDs, and separate local Mail.app observations. A failed or unavailable readback preserves the receipt and returns a nonzero exit status. Do not repeat an uncertain mutation based only on its exit status. With several IDs, each item carries its own `receiptPath`; reconcile each receipt separately.

```sh
mail-app-cli labels reconcile /private/path/receipt.jsonl --json
```

Reconciliation reads the original receipt and checks current provider and Mail.app state without repeating the mutation or synchronizing Mail.app. It uses the account recorded in the receipt; an explicit `--account` must match it.

When several credential sets exist for that account, use `--email` to select the provider address recorded in the receipt. The current authenticated profile must still match that receipt.

`providerConfirmed: true` means a fresh provider read matched the requested delta. `mailStatus: pending` means Mail.app's local membership observation has not yet matched; an open Mail viewer may lag further. These fields describe the observed state, rather than promising that Gmail rules or another client cannot change labels later.

`labels list` supports `--count`, `--ids-only`, and `--jq`. Read-only show and reconcile commands support `--jq`. Add, remove, and credential commands reject `--jq` so a filter failure cannot hide a state-changing receipt. Existing archive and move commands retain their Mail.app behavior.
