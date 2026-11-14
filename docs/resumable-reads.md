# Resumable body reads and receipt status

`messages read --checkpoint <path>` saves each completed body extraction to a private file. The checkpoint is replaced atomically after every item and synchronized before the item-completion event is emitted. Successful bodies survive a later failure or interruption.

```sh
mail-app-cli messages read 101 102 103 --checkpoint ./selected-bodies.json --json
```

Run the same selection with the same `--body-source` and `--body-alternatives` options to resume. Each saved success receives a fresh metadata read before its body is reused. The current read, flagged, deleted, account, mailbox, recipient, and date metadata comes from that observation. The saved body, extracted links, header controls, and extraction diagnostics are reused only when the stable message identity and message size still match.

The full requested ID selection is retained even when an ID cannot initially be located. A later invocation can extract a newly located ID while validating and reusing the other bodies. Saved successes remain bound to their original account. Failed extractions are attempted once per invocation. Running the command again explicitly retries those failures. A failed freshness check keeps the original successful extraction as identity evidence, so a recycled local ID can never become an unchecked fresh extraction on a later invocation. An absent or changed local ID is refused; locate the intended message again and use a new checkpoint path. Checkpoints are evidence for body reads and are never used as mutation selections.

Checkpoints contain message bodies and must remain regular files with mode `0600`. Their parent directory must already exist. Corrupt files, changed selections, and changed body options are rejected before extraction. Completion means that every requested extraction succeeded; the body diagnostics continue to describe MIME alternatives, nontext parts, images, and partial files.

`--events` writes JSONL progress to stderr while the final normal result remains on stdout:

```sh
mail-app-cli messages read 101 102 --checkpoint ./selected-bodies.json --events --json \
  > ./result.json 2> ./progress.jsonl
```

Read events include `started`, `metadata_started` and `metadata_completed` for reused bodies, `body_started` and `body_completed` for new extractions, `item_completed`, and `completed`. Item events include the ID and any failure. Each metadata read honors `--timeout`, and all reads share `--budget`.

`messages status <receipt-file>` inspects a batch receipt locally without contacting Mail or repeating an action. It accepts a live durable JSONL journal, a final batch receipt or envelope, and a manifest receipt or envelope. Manifest parent receipts list the child receipts so each group can be inspected independently.

```sh
mail-app-cli messages status ./cleanup.json.journal.jsonl --json
```

The result reports the latest durable phase, counts, and last action. Terminal records distinguish `completed`, `stopped`, and `interrupted`. A journal without a terminal record reports `running_or_interrupted`: a local file cannot establish whether the process is still running. A trailing line without its newline is ignored and reported as `partialLine:true`. Malformed complete records are rejected. `completed` describes a finished workflow; inspect the failure and unverified counts to determine its outcome.
