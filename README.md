# mail-app-cli

<img width="783" height="738" alt="image" src="https://github.com/user-attachments/assets/110a5dc4-b044-434d-8752-14048059c7aa" />

A command-line interface for controlling macOS Mail.app. Provides complete scriptable access to accounts, mailboxes, messages, and attachments.

## Features

- See accounts, mailboxes, and unread counts
- List, read, search, and filter messages
- Archive, move, delete, flag, and mark mail
- Batch message actions with dry-run safety
- Create, edit, send, and delete drafts
- Send mail with files and signatures
- Export messages and attachments for automation
- Validate exported message JSON before migration work
- Manage rules and explore smart mailboxes and threads
- Browse signatures and VIP mail
- Read Gmail Archive and All Mail reliably
- Output scriptable JSON for every workflow

## Installation

### From Source

```bash
go install github.com/0xSMW/mail.app-cli@latest
```

### Build Locally

```bash
git clone https://github.com/0xSMW/mail.app-cli.git
cd mail.app-cli
go build -o mail-app-cli
```

## Usage

### Accounts

List all Mail.app accounts:

```bash
mail-app-cli accounts list
```

Show details for a specific account:

```bash
mail-app-cli accounts show "Gmail"
```

### Mailboxes

List all mailboxes:

```bash
mail-app-cli mailboxes list
```

List mailboxes for a specific account:

```bash
mail-app-cli mailboxes list --account "Gmail"
```

### Messages

List messages in a mailbox:

```bash
mail-app-cli messages list --account "Gmail" --mailbox "INBOX"
```

List with filters:

```bash
# Show only unread messages
mail-app-cli messages list -a "Gmail" -m "INBOX" --unread

# Show only flagged messages
mail-app-cli messages list -a "Gmail" -m "INBOX" --flagged

# Show messages since a specific date
mail-app-cli messages list -a "Gmail" -m "INBOX" --since "2025-12-01"

# Show messages since a specific date and time
mail-app-cli messages list -a "Gmail" -m "INBOX" --since "2025-12-14 09:00:00"

# Combine filters
mail-app-cli messages list -a "Gmail" -m "INBOX" --unread --since "2025-12-01" --limit 10
```

Show full message details:

```bash
mail-app-cli messages show <message-id> -a "Gmail" -m "INBOX"
```

Mark message as read/unread:

```bash
# Mark as read
mail-app-cli messages mark <message-id> -a "Gmail" -m "INBOX" --read

# Mark as unread
mail-app-cli messages mark <message-id> -a "Gmail" -m "INBOX" --read=false
```

Flag/unflag a message:

```bash
# Flag a message
mail-app-cli messages flag <message-id> -a "Gmail" -m "INBOX" --flagged

# Unflag a message
mail-app-cli messages flag <message-id> -a "Gmail" -m "INBOX" --flagged=false
```

Archive a message:

```bash
mail-app-cli messages archive <message-id> -a "Gmail" -m "INBOX"
```

Move a message to another mailbox:

```bash
mail-app-cli messages move <message-id> "Archive" -a "Gmail" -m "INBOX"
```

Delete a message:

```bash
mail-app-cli messages delete <message-id> -a "Gmail" -m "INBOX"
```

### Sending Email

Send a message:

```bash
mail-app-cli send \
  --account "Gmail" \
  --to user@example.com \
  --subject "Hello" \
  --body "Message content here"
```

Send from a file and append a Mail.app signature:

```bash
mail-app-cli send \
  --account "Gmail" \
  --to user@example.com \
  --subject "Hello" \
  --body-file body.md \
  --signature "Work"
```

Send to multiple recipients:

```bash
mail-app-cli send \
  -a "Gmail" \
  -t user1@example.com \
  -t user2@example.com \
  -c cc@example.com \
  -s "Multi-recipient message" \
  --body "Content"
```

### Search

Search for messages across all mailboxes:

```bash
mail-app-cli search "important meeting"
```

Search with limit:

```bash
mail-app-cli search "project update" --limit 20
```

### Attachments

List attachments in a message:

```bash
mail-app-cli attachments list <message-id> -a "Gmail" -m "INBOX"
```

Save an attachment:

```bash
mail-app-cli attachments save <message-id> "document.pdf" -a "Gmail" -m "INBOX"
```

Save to a specific path:

```bash
mail-app-cli attachments save <message-id> "document.pdf" -a "Gmail" -m "INBOX" -o ~/Downloads/document.pdf
```

### Batch Operations

Preview a bulk archive:

```bash
mail-app-cli messages batch archive -a "Gmail" -m "INBOX" --query "receipt" --dry-run
```

Archive message IDs from stdin:

```bash
jq -r '.[].id' messages.json | mail-app-cli messages batch archive -a "Gmail" -m "INBOX" --stdin --yes
```

Move selected messages:

```bash
mail-app-cli messages batch move "Receipts" -a "Gmail" -m "INBOX" --query "invoice" --yes
```

Mark, flag, or delete selected messages:

```bash
mail-app-cli messages batch mark -a "Gmail" -m "INBOX" --read=false 123 456
mail-app-cli messages batch flag -a "Gmail" -m "INBOX" --flagged=false --stdin < ids.txt
mail-app-cli messages batch delete -a "Gmail" -m "INBOX" --query "old alert" --dry-run
```

### Export and Validation

Export messages as JSON:

```bash
mail-app-cli export messages -a "Gmail" -m "INBOX" --format json --output inbox.json
```

Export attachments:

```bash
mail-app-cli export attachments -a "Gmail" -m "INBOX" --output ./attachments
```

Validate an exported message file:

```bash
mail-app-cli import messages -a "Gmail" -m "Archive" --format json --file inbox.json --dry-run
```

### Drafts

Create and review a draft:

```bash
mail-app-cli drafts create -a "Gmail" --to user@example.com --subject "Review" --body-file body.md
mail-app-cli drafts list -a "Gmail"
mail-app-cli drafts show <draft-id> -a "Gmail"
```

Send or delete a draft:

```bash
mail-app-cli drafts update <draft-id> -a "Gmail" --subject "Updated" --body-file revised.md
mail-app-cli drafts send <draft-id> -a "Gmail"
mail-app-cli drafts delete <draft-id> -a "Gmail" --dry-run
```

### Rules, Smart Mailboxes, Threads, Signatures, and VIP Messages

Inspect higher-level Mail.app surfaces:

```bash
mail-app-cli rules list
mail-app-cli smart list
mail-app-cli signatures list
mail-app-cli threads list -a "Gmail" -m "INBOX"
mail-app-cli messages vip --limit 25
```

Manage supported rule actions:

```bash
mail-app-cli rules show "Receipts"
mail-app-cli rules create "Receipts" -a "Gmail" --from-domain stripe.com --move-to Receipts --dry-run
mail-app-cli rules enable "Receipts"
mail-app-cli rules disable "Receipts"
mail-app-cli rules delete "Receipts" --dry-run
```

Preview rule application with normal selectors:

```bash
mail-app-cli rules apply "Receipts" -a "Gmail" -m "INBOX" --query "stripe" --dry-run
```

Inspect smart mailbox, signature, and thread details:

```bash
mail-app-cli smart show "Unread Receipts"
mail-app-cli smart query "receipt" --limit 20
mail-app-cli signatures show "Work"
mail-app-cli threads show <thread-id> -a "Gmail" -m "INBOX"
mail-app-cli threads archive <thread-id> -a "Gmail" -m "INBOX" --dry-run
```

### Sync

Trigger sync and get a structured result:

```bash
mail-app-cli sync --account "Gmail" --mailbox "INBOX" --wait --json
```

## JSON Output and jq

All commands output JSON format for easy parsing and scripting. The output is formatted with 2-space indentation for human readability while remaining machine-parseable.

### Pretty Printing

For even prettier output, pipe through `jq`:

```bash
mail-app-cli accounts list | jq
```

### jq Examples

#### Filter accounts by email domain

```bash
mail-app-cli accounts list | jq '.[] | select(.emailAddress | endswith("@gmail.com"))'
```

#### Get only enabled accounts

```bash
mail-app-cli accounts list | jq '.[] | select(.enabled==true) | .name'
```

#### Count unread messages across all mailboxes

```bash
mail-app-cli mailboxes list | jq '[.[].unreadCount] | add'
```

#### Find mailboxes with unread messages

```bash
mail-app-cli mailboxes list | jq '.[] | select(.unreadCount > 0) | {account, name, unread: .unreadCount}'
```

#### Get just the subject lines from messages

```bash
mail-app-cli messages list -a "Gmail" -m "INBOX" | jq '.[].subject'
```

#### Filter unread messages from specific sender

```bash
mail-app-cli messages list -a "Gmail" -m "INBOX" | jq '.[] | select(.read==false and (.sender | contains("boss@company.com")))'
```

#### Search and format results as CSV

```bash
mail-app-cli search "important" | jq -r '.[] | [.account, .mailbox, .subject, .sender] | @csv'
```

#### Count messages by account

```bash
mail-app-cli search "project" | jq 'group_by(.account) | map({account: .[0].account, count: length})'
```

#### Get attachment names from a message

```bash
mail-app-cli attachments list <message-id> -a "Gmail" -m "INBOX" | jq '.[].name'
```

#### Find large attachments (>1MB)

```bash
mail-app-cli attachments list <message-id> -a "Gmail" -m "INBOX" | jq '.[] | select(.fileSize > 1048576)'
```

### Scripting Examples

#### Check for unread messages

```bash
#!/bin/bash
unread=$(mail-app-cli messages list -a "Gmail" -m "INBOX" --unread | jq 'length')
if [ $unread -gt 0 ]; then
  echo "You have $unread unread messages"
fi
```

#### Archive all read messages

```bash
#!/bin/bash
mail-app-cli messages list -a "Gmail" -m "INBOX" | jq -r '.[] | select(.read==true) | .id' | while read -r msg_id; do
  mail-app-cli messages archive "$msg_id" -a "Gmail" -m "INBOX"
done
```

#### Daily unread summary

```bash
#!/bin/bash
echo "Today's Unread Email Summary"
echo "============================"
mail-app-cli mailboxes list | jq -r '.[] | select(.unreadCount > 0) | "\(.account)/\(.name): \(.unreadCount) unread"'
```

#### Save all attachments from a sender

```bash
#!/bin/bash
SENDER="colleague@company.com"
ACCOUNT="Gmail"
MAILBOX="INBOX"

# Find all messages from sender
mail-app-cli messages list -a "$ACCOUNT" -m "$MAILBOX" | jq -r ".[] | select(.sender | contains(\"$SENDER\")) | .id" | while read -r msg_id; do
  # Get attachments for each message
  mail-app-cli attachments list "$msg_id" -a "$ACCOUNT" -m "$MAILBOX" | jq -r '.[].name' | while read -r att_name; do
    echo "Saving: $att_name from message $msg_id"
    mail-app-cli attachments save "$msg_id" "$att_name" -a "$ACCOUNT" -m "$MAILBOX" -o "~/Downloads/$att_name"
  done
done
```

## Project Structure

```
mail-app-cli/
├── cmd/              # Cobra command definitions
│   ├── root.go
│   ├── accounts.go
│   ├── batch.go
│   ├── drafts.go
│   ├── export.go
│   ├── import.go
│   ├── mailboxes.go
│   ├── messages.go
│   ├── rules.go
│   ├── send.go
│   ├── search.go
│   ├── signatures.go
│   ├── smart.go
│   ├── threads.go
│   ├── vip.go
│   └── attachments.go
├── pkg/
│   └── mail/        # Mail.app AppleScript/JXA client
│       ├── accounts.go
│       ├── attachments.go
│       ├── automation.go
│       ├── bulk.go
│       ├── client.go
│       ├── drafts.go
│       ├── envelope_index.go
│       ├── mailboxes.go
│       ├── message_actions.go
│       ├── message_content.go
│       ├── messages.go
│       ├── models.go
│       ├── rules.go
│       ├── search.go
│       ├── send.go
│       ├── signatures.go
│       ├── smart_mailboxes.go
│       ├── sync.go
│       └── unified.go
└── main.go
```

## How It Works

The CLI uses AppleScript and JavaScript for Automation (JXA) to interact with Mail.app. This provides:

- Native integration with Mail.app
- Access to all Mail.app features
- No external dependencies or APIs required
- Works with all mail providers configured in Mail.app

## Requirements

- macOS (tested on macOS 15+)
- Mail.app configured with at least one account
- Go 1.21+ (for building from source)

## Development

### Prerequisites

- Go 1.21 or higher
- macOS with Mail.app

### Building

```bash
go build -o mail-app-cli
```

### Testing

```bash
# Test account listing
./mail-app-cli accounts list

# Test mailbox listing
./mail-app-cli mailboxes list

# Test message listing
./mail-app-cli messages list -a "Your Account" -m "INBOX" --limit 5
```
