# Reusable cleanup manifests

A manifest selects exact messages and states the required result. It contains
no search query or classification rule. Review the complete selection before
applying it. Accounts come exclusively from each item; CLI account defaults do
not reassign the selection. Explicit `--account` and `--mailbox` flags constrain
every item; a conflicting account or source mailbox rejects the complete file.

```json
{
  "version": 1,
  "items": [
    {
      "account": "Work",
      "id": "205",
      "sourceMailbox": "INBOX",
      "identity": {
        "rfcMessageId": "<example@example.com>",
        "sender": "sender@example.com",
        "subject": "Example message",
        "dateSent": "2026-08-29T00:00:00Z",
        "messageSize": 42
      },
      "action": "move",
      "targetMailbox": "Processed",
      "read": true,
      "requiredPresent": ["Processed"],
      "requiredAbsent": ["INBOX"]
    }
  ]
}
```

```sh
mail-app-cli messages manifest apply cleanup.json --dry-run
mail-app-cli messages manifest apply cleanup.json
mail-app-cli messages manifest verify cleanup.json
```

Version 1 requires a positive original local ID, account, source mailbox,
complete stable metadata (sender, subject, canonical UTC sent timestamp, and
positive size), action, explicit desired boolean read state, and both membership
arrays. Include an RFC Message-ID when available; it must be a real header ID.
Unknown fields, conflicting memberships, and duplicate selections are rejected.
Archive and delete destinations must match the account's actual special mailbox,
including during verification and when the requested memberships already hold.
At least one required present mailbox must establish the resulting message and
its read state. There are four supported actions:

- `move`: provide the destination in `targetMailbox` and `requiredPresent`.
- `archive`: provide the account's archive destination and require its presence.
- `delete`: provide the account's actual special Trash mailbox and require its
  presence. This is a recoverable move to Trash.
- `mark`: omit `targetMailbox` and require presence in `sourceMailbox`.

For archive and delete, place the source in `requiredAbsent`. An archive's
resolved physical source must also appear in `requiredAbsent`; a selection from
a Gmail label may resolve to INBOX. For move, explicitly place the source in
either membership array. This allows a provider's retained backing mailbox to
be required present when that is the intended result. Mailbox names and paths
must be unambiguous.

Apply resolves the entire selection before changing any message. Missing,
ambiguous, mismatched, or unreadable identities fail the preflight without
mutations. A changed local ID is allowed only after the exact stable identity
has been resolved and checked against live message metadata in the same account.
All supplied metadata remains an exact guard, including sender display text and
message size even when an RFC Message-ID is supplied. A provider changing these
fields requires a newly reviewed selection rather than a weaker match.
An item is skipped only when all its required memberships and read states are
already verified. Other items require a unique identity in their source mailbox.
Actions execute sequentially, grouped by account, action, source, destination,
and desired read state. A group stops after three identical consecutive failures;
later groups are left unattempted. Relocations first set the requested read state.

Apply persists a private parent receipt linking each separate batch receipt
before that batch begins. These receipts include selected identities, mutation
phases, outcomes, and the final observation. The result reports its receipt path,
original IDs, current IDs, per-mailbox IDs, and per-item status. A mutation can
partially succeed; inspect both mutation status and final state. Reapply the same
file to skip items whose complete desired state is verified, or use `verify`
for a read-only observation. Verification neither repeats actions nor requests
account synchronization. Its result describes the current indexed state and
does not guarantee against later provider changes.
