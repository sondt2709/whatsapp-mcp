## Context

The repository is empty. The binary links to a personal WhatsApp account as a linked device with the library whatsmeow. WhatsApp has no server API to read old messages: the server sends each message to each device one time (live, or from the offline queue at connect), and the phone sends a history sync at pairing. So the binary must keep the messages that agents read later, and that local data is sensitive: it holds the device keys (with them, another program can read and send as the user), the contacts and the messages. whatsmeow also writes message plaintext to its own tables (`whatsmeow_event_buffer`, `whatsmeow_retry_buffer`).

MCP clients start one stdio server for each agent session, but one set of device keys allows only one connection. A second connection replaces the first (`events.StreamReplaced`).

Facts from a spike on a real account (two linked devices of one account, CGO_ENABLED=0):
- whatsmeow `sqlstore` works on `github.com/ncruces/go-sqlite3` with the Adiantum VFS. A canary text had zero matches in the database, the WAL and the shared-memory file; the control file without encryption had three.
- A Keychain item made through Security.framework with purego trusts only the binary that made it (access list with the partition `cdhash:`).
- 99.92% of the messages from other people had both address forms (LID and phone). The own chat arrives with the LID address also when the sender used the phone address.
- The CDN served files of 0 to 14 days. Files of 20 days gave HTTP 410, of 30 days and more HTTP 403.
- A media retry got the answer SUCCESS from the phone, but the new file did not pass the hash and HMAC checks (issue #2).
- A read receipt that one linked device sends in the own chat does not reach the other linked device. So a script cannot prove the unread rule through a second device.

## Goals / Non-Goals

**Goals:**
- All WhatsApp functions that an agent needs: list chats, read, search, wait, send text, send files, download files.
- No plaintext message, name or key on disk.
- One connection for many agents.
- One ID for each item in every output.
- No read receipt and no presence.
- macOS and Linux, one static binary.

**Non-Goals:**
- WhatsApp Channels, status updates, calls, reactions, edits, deletes, polls.
- A download after the CDN expiry (issue #2).
- Windows.
- Protection against malware that runs as the same user. Such a program can call the socket like an agent can.
- Several accounts in one data folder. A second account uses a second `--data-dir`.

## Decisions

### D1. A daemon and thin clients

```
 MCP client (stdio)      MCP client (HTTP, optional)     user shell
        │                         │ Bearer key                │
        ▼                         ▼                           ▼
 whatsapp-mcp mcp         daemon /mcp (127.0.0.1)     whatsapp-mcp <command>
 (reads/writes files)                                  (reads/writes files)
        └─────── Unix socket /v1 JSON, peer UID check ───────┘
                                  ▼
 whatsapp-mcp daemon: whatsmeow connection · event hub · retention job
                                  ▼
 <data>/whatsapp.db (Adiantum, key from the system key store)
```

The daemon owns the connection, the database and the event hub. The CLI and the stdio MCP server call the same `/v1` API through one Go client package, so both give the same JSON. Alternatives: a stdio server that takes the lock and lets other servers forward to it (complex hand-over when it exits, and no messages while no agent runs); a stdio server that fails with "busy" (bad for many sessions).

### D2. Encrypt the whole database
`github.com/ncruces/go-sqlite3` with `vfs/adiantum`, `PRAGMA hexkey` in the connect callback, `temp_store=memory`, WAL. The whatsmeow tables and our tables are in one file. Alternatives: SQLCipher (needs CGO); encrypt only our columns (whatsmeow tables keep plaintext and keys); keep messages only in RAM (an agent cannot read what arrived while it did not run). Adiantum is deterministic: a reader of many copies sees which 4 KiB blocks changed, not their content.

### D3. Key store
- macOS: Security.framework through `github.com/ebitengine/purego` (`SecItemAdd`, `SecItemCopyMatching`, `SecItemDelete`), generic password, service `whatsapp-mcp`, account = SHA-256 of the absolute data folder path (first 16 hex digits). Not `/usr/bin/security` and not `go-keyring`: an item made by `/usr/bin/security` trusts that tool, so any program can read it with `security find-generic-password -w`. After an update, the new binary has a new code hash, and macOS asks one time.
- Linux: the Secret Service through D-Bus (`github.com/godbus/dbus/v5`), attributes `service=whatsapp-mcp`, `account=<same>`. When none answers: a passphrase through `whatsapp-mcp unlock`, Argon2id (`golang.org/x/crypto/argon2`) with a salt in `<data>/key.salt`. The daemon stays `locked` until the unlock.
- The key stays in the daemon only. Clients never get it.

### D4. IDs
- `user_id`: LID first (`<n>@lid`), else the phone address. The daemon resolves the form when it stores a message (from `SenderAlt`, `RecipientAlt` and the whatsmeow LID store) and stores the canonical form.
- `chat_id`: the `user_id` of the other person, or `<n>@g.us`.
- `message_id`: `<chat_id>/<id>`, because a WhatsApp ID is unique only in its chat.
- `file_id`: the `message_id` (one file at most for each message).
- `cursor`: the opaque base64 of the store row ID.
- Inputs pass through one resolver that accepts the canonical ID, the other form and `+<phone>`.
- Alternative: keep the form that arrived. The spike showed the same own chat under two forms, so that alternative breaks the "one ID" goal.

### D5. Store layout (inside the encrypted file)
- `messages(row INTEGER PRIMARY KEY, chat, id, sender, sender_name, from_me, ts, text, raw, media_kind, quote_id, quote_author, quote_text, UNIQUE(chat, id))`.
- `chats(chat_id PRIMARY KEY, name, kind, phone, last_ts)`.
- `http_keys(hash PRIMARY KEY, created)`.
- Retention deletes `messages` rows older than `--retention`. `wait_for_messages` and the cursor use `row`.

### D6. Event hub for waiting
After each stored message, the daemon signals a hub (a closed channel replaced by a new one). Each waiter reads the rows after its cursor, then waits on the hub with its timeout. All waiters see all rows, so two agents both get each message. The stdio server sends `notifications/progress` each 15 seconds when the client gave a progress token.

### D7. Files go through the client
`download_file`: the daemon downloads into memory (at most 100 MB) and streams the bytes on the socket. The client writes to `dir` with the safe name, never overwrites (`os.Link` to a new name), and sets the quarantine attribute on macOS. `send_file`: the client checks the path rule, opens the file and streams it. The daemon writes no temporary file. The client runs inside the terminal or the MCP client, so on macOS it has the folder access (TCC) that a LaunchAgent may not have. With HTTP, the daemon does this work itself with its own folders.

### D8. Unread rule
The code calls whatsmeow through a small interface `waClient` that has no `MarkRead`, `SendPresence`, `SubscribePresence` or `SendChatPresence`. A unit test parses the Go files and fails when a file calls those methods on `*whatsmeow.Client`. whatsmeow sends delivery receipts by itself. The phone does the same, so this does not change what the sender sees.

### D9. MCP SDK
`github.com/modelcontextprotocol/go-sdk` (v1.8.0): typed tools with input and output schemas from Go structs, the stdio transport and the Streamable HTTP handler. Alternative: `mark3labs/mcp-go`. It works too, but the official SDK follows the spec first.

### D10. Package layout
- `cmd/whatsapp-mcp`: commands (cobra).
- `internal/api`: the JSON types and error codes, shared by all.
- `internal/whatsapp`: connection, pairing, event to row, send, download.
- `internal/store`: the encrypted database and its queries.
- `internal/keystore`: macOS and Linux back ends behind one interface.
- `internal/daemon`: lock, socket server, peer check, hub, retention.
- `internal/client`: socket client and the automatic start.
- `internal/mcpserver`: tool definitions over `internal/client`.
- `internal/files`: safe name, move, quarantine, send-path rule.
- `internal/sanitize`: hidden characters.

## Testing

| Layer | Runs in | WhatsApp |
|---|---|---|
| Unit: store, IDs, resolver, path rule, names, sanitize, hub, error codes, unread AST test | CI (Ubuntu, macOS) | A fake `waClient` |
| Replay: event-to-row parsing | CI | Real protos captured from a paired account with a capture tool that replaces addresses, IDs, names, texts and removes file bytes; read before the commit |
| MCP e2e: the real binary over stdio and the socket, all tools and all error states, `--http` with and without the key | CI | The fake back end behind a test build tag, fed with the replay fixtures |
| Key store | CI: a temporary keychain on the macOS runner; `gnome-keyring-daemon` under `dbus-run-session` on Ubuntu | None |
| Live: pair two devices of one account; device A sends text and a file to the own chat; device B gets them through `wait_for_messages` and downloads the file with the same SHA-256; canary grep of both data folders | Local only (`task live`), with the user | Real |

## Risks / Trade-offs

- [The key store item is lost] → the database cannot be read; `locked` tells the user to log out and pair again. No recovery, by design.
- [macOS asks for Keychain access after each update] → one dialog after each update; `get_status` reports `locked` with the action until the user allows it.
- [Linux Secret Service has no access list for each app] → any program of the user can read an unlocked keyring; documented; the passphrase mode is the stronger choice.
- [Same-user malware calls the socket] → not prevented; `--read-only`, the send annotations and the send path rule limit the damage.
- [A canonical ID changes when the LID of a person becomes known later (0.08% in the spike)] → the resolver accepts the old form as input, so an old ID still works.
- [A file is older than about two weeks] → `file_expired`; issue #2.
- [WhatsApp blocks automated accounts] → the tool sends only what an agent asks; live tests send only to the own chat with few messages and never run in CI.
- [Process memory and swap hold messages] → macOS encrypts swap; on Linux the user must encrypt swap; core dumps are off (`RLIMIT_CORE` 0).
- [HTTP mode on macOS: a LaunchAgent may have no access to `~/Downloads`] → the error names the folder; the user gives the daemon a folder that it can use.

## Migration Plan

New project, no migration. First release after this change. Rollback: `whatsapp-mcp service uninstall`, `whatsapp-mcp logout`, delete the binary.

## Open Questions

- Does a send from a linked device clear the unread count of that chat on the phone? Not verified; the tool description says that the result is not known.
- The tool-call time limit of each MCP client is different. The default `timeout_seconds` 60 must stay below it; the live test measures the limit of Claude Code.
