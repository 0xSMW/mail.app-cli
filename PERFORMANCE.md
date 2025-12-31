# Performance Characteristics

## Mail.app API Limitations

### Message Loading Performance

When loading messages from a mailbox, performance is **directly proportional to the total number of messages in that mailbox**, even when requesting only a few messages or a single specific message.

**Test Results:**

| Operation | Mailbox | Total Messages | Load Time |
|-----------|---------|----------------|-----------|
| List 5 messages | INBOX (empty) | 0 | 0.4s |
| List 5 messages | INBOX (small) | 19 | 0.4s |
| List 5 messages | All Mail (large) | 59,707 | 2.4-3.6s |
| Get 1 message by ID | INBOX (small) | 19 | 0.4s |
| Get 1 message by ID | All Mail (large) | 59,707 | 2.5-3.3s |

### Why This Happens

Mail.app's AppleScript/JXA bridge has a fundamental limitation:

When you call `mailbox.messages()`, Mail.app **must enumerate ALL messages** in the mailbox before returning, even if you only need a few. The API does not support:

- Streaming or lazy evaluation
- LIMIT clauses
- Direct index access without full enumeration

**Technical Details:**

```javascript
// This takes 2.4 seconds for a mailbox with 59,707 messages
let allMessages = mbox.messages();
let first5 = allMessages.slice(0, 5);

// Mail.app must:
// 1. Query its SQLite database for all message IDs
// 2. Create object references for each message
// 3. Package them into a JavaScript array
// 4. Return the array (only then can we slice it)
```

### What We Tried

1. **Direct index access** - Not supported by Mail.app API
2. **`.whose()` filtering for unread** - Reduced initial query time but accessing message properties took 60+ seconds
3. **`.whose({id: messageID})` for specific message** - Still takes 4+ seconds (slower than iteration!)
4. **Early slicing optimization** - Already implemented, can't slice before calling `.messages()`

**Why `.whose()` doesn't help:**
Mail.app's `.whose()` clause still enumerates all messages internally to filter them. It provides no performance benefit for large mailboxes.

### Current Optimizations

The code already implements several optimizations:

1. **Early array slicing** - Limit processing immediately after getting messages
2. **Smart multiplier** - Only fetch 3x messages when filters are active
3. **Inline filtering** - Apply filters before iteration to reduce work
4. **24-hour caching** - Avoid repeated expensive calls

### Recommendations

**For scripting/automation:**
- Use specific mailboxes (INBOX, Sent) rather than "All Mail" when possible
- Results are cached for 24 hours, so first call is slow but subsequent calls are instant
- Consider the 3-6 second load time for large mailboxes as acceptable for batch operations

**For interactive use:**
- Prefer smaller, focused mailboxes
- Use the cache warming (first load takes time, rest of day is fast)

### Comparison with Other Accounts

Performance varies by mailbox size, not account:

```
rmelton@gmail.com / All Mail (59,707 messages): 3.6s
rmelton@gmail.com / INBOX (0 messages): 0.4s
rmelton@gmail.com / Spam (850 messages): 0.6s
robert.melton@gmail.com / INBOX (19 messages): 0.4s
```

### Not a Bug

This is **expected behavior** given Mail.app's API design. All AppleScript/JXA-based Mail.app tools share this limitation.
