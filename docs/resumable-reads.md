# Resumable body reads and receipt status

`messages read --checkpoint <path>` saves each completed body extraction to a private file. The checkpoint is replaced atomically after every item and synchronized before the item-completion event is emitted. Successful bodies survive a later failure or interruption.

```sh
mail-app-cli messages read 101 102 103 --checkpoint ./selected-bodies.json --json
```

Run the same requested IDs with the same `--body-source` and `--body-alternatives` options to resume. The full selection is retained even when an ID cannot initially be located. A later invocation can read a newly located ID while validating the other saved messages.

Each saved success retains its original account and logical message identity independently of its latest body or local ID. A new invocation reads current metadata before using a saved body. If Mail has moved the message, regenerated its local ID, or reused the old ID for unrelated mail, recovery searches only the original account. Recovery requires an RFC Message-ID corroborated by the same sender address, exact subject, and sent date. Every indexed candidate receives an exact live metadata check, and exactly one physical message must match. Ambiguous candidates, incomplete discovery, changed accounts, and unrelated copies are refused. An explicit `--account` must match the original account exactly.

The result keeps the originally requested ID in `items[].id`. Its current Mail ID is in `items[].message.id`, and the current mailbox is in `items[].message.mailbox`. Read and flagged state come from the fresh observation. These reads never mark a message read or change its mailbox.

A saved disk body is reused only when its complete, account-scoped local message file has the same content revision and size. The revision covers the RFC message bytes, including the body and MIME parts; changes to Mail's separate read-state trailer do not invalidate it. Same-size content edits are detected. A changed body, changed size, or invalid extraction is read again only after the original RFC identity has been uniquely confirmed. The new extraction must still match the original account and logical identity, and its physical mailbox and metadata are checked again before the checkpoint is replaced.

Bodies rendered with `--body-source mail` are read again because a local file revision cannot prove Mail's rendered result is unchanged. A disk body without a trustworthy revision is also read again after secure RFC confirmation. Older checkpoints have no stored revision, so their first resume requires this confirmation and fresh extraction before reuse becomes possible.

Messages without a corroborated RFC identity use the strict original sender, subject, sent date, and size fallback. That fallback permits reuse of an unchanged, proven local body at the same local ID. It cannot authorize relocation or replacement content when a revision changes or is unavailable. Those cases remain incomplete rather than guessing at a match.

Failed operations are attempted once per invocation. Run the command again explicitly to retry. A failed recovery or extraction preserves the original identity and prior successful body, including when other messages succeed afterward. The checkpoint never turns a failed identity check into an unchecked fresh read of a recycled ID. Checkpoints are evidence for body reads and are never used as mutation selections.

Checkpoints contain message bodies and must remain regular files with mode `0600`. Their parent directory must already exist. Corrupt files, changed selections, and changed body options are rejected before extraction. Completion means that every requested extraction succeeded; body diagnostics continue to describe MIME alternatives, nontext parts, images, and partial files.

`--events` writes JSONL progress to stderr while the final normal result remains on stdout:

```sh
mail-app-cli messages read 101 102 --checkpoint ./selected-bodies.json --events --json \
  > ./result.json 2> ./progress.jsonl
```

Read events include `started`, metadata and body phases, `item_completed`, and `completed`. Item events include the requested ID and any failure. Each checkpoint item's metadata checks, recovery, revision reads, and extraction share one `--timeout`; all selected items also share `--budget`.

`messages status <receipt-file>` inspects a batch receipt locally without contacting Mail or repeating an action. It accepts a live durable JSONL journal, a final batch receipt or envelope, and a manifest receipt or envelope. Manifest parent receipts list the child receipts so each group can be inspected independently.

```sh
mail-app-cli messages status ./cleanup.json.journal.jsonl --json
```

The result reports the latest durable phase, counts, and last action. Terminal records distinguish `completed`, `stopped`, and `interrupted`. A journal without a terminal record reports `running_or_interrupted`: a local file cannot establish whether the process is still running. A trailing line without its newline is ignored and reported as `partialLine:true`. Malformed complete records are rejected. `completed` describes a finished workflow; inspect the failure and unverified counts to determine its outcome.
