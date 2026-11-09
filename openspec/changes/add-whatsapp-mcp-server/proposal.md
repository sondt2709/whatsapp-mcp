## Why

AI agents cannot read or send the WhatsApp messages of their user. WhatsApp has no public API for a personal account. A linked device (the protocol that WhatsApp Web uses) can read and send. This change makes `whatsapp-mcp`: one Go binary that links to a personal account and gives agents the WhatsApp functions through MCP and a CLI, with the data encrypted on disk.

## What Changes

- New binary `whatsapp-mcp` (Go, `CGO_ENABLED=0`, macOS and Linux).
- A daemon holds the one WhatsApp connection, stores the received messages and serves a JSON API on a Unix socket.
- The CLI and the stdio MCP server are clients of the daemon. A Streamable HTTP MCP endpoint with an API key is optional.
- MCP tools: `get_status`, `list_chats`, `list_messages`, `wait_for_messages`, `send_message`, `send_file`, `download_file`.
- CLI commands: `pair`, `status`, `logout`, `daemon`, `mcp`, `service install`, `service uninstall`, and one command for each tool with `--json`.
- The database (device keys, contacts, messages) is encrypted with a 256-bit key. The key is in the macOS Keychain or the Linux Secret Service, else it comes from a passphrase.
- One canonical ID for each user, chat, message and file in all outputs.
- The tool never marks a message as read and never sends presence.
- Messages are kept for 90 days by default. A file is downloaded only on request, and only while WhatsApp keeps it (about two weeks).
- Out of scope: WhatsApp Channels (newsletters), status updates, calls, reactions, edits, deletes, Windows, a download after the CDN expiry (issue #2).

## Capabilities

### New Capabilities

- `account-link`: pairing, logout, account state, and the encryption of the local data with a key from the system key store.
- `message-sync`: the receipt and storage of messages, the retention, the canonical IDs and the rule that messages stay unread.
- `chat-reading`: list chats, read and search messages, wait for new messages.
- `message-sending`: send text (optionally as a quote) and send files, with the folder rule and the read-only mode.
- `file-download`: save the file of a message to a folder.
- `daemon-api`: the daemon, the Unix socket API, the caller check, the automatic start and the background service.
- `mcp-server`: the MCP tools and their errors, the stdio transport and the Streamable HTTP transport with an API key.

### Modified Capabilities

None. The repository has no specs yet.

## Impact

- New code: `cmd/whatsapp-mcp`, `internal/whatsapp`, `internal/store`, `internal/keystore`, `internal/daemon`, `internal/mcpserver`, `internal/files`, `internal/sanitize`.
- New dependencies: `go.mau.fi/whatsmeow`, `github.com/ncruces/go-sqlite3` (Adiantum VFS), `github.com/modelcontextprotocol/go-sdk`, `github.com/ebitengine/purego` (macOS Keychain), `github.com/godbus/dbus/v5` (Linux Secret Service), `golang.org/x/crypto` (Argon2id), `github.com/spf13/cobra` (CLI), `rsc.io/qr`.
- Public contracts: the MCP tool names and schemas, the `/v1` socket API, the CLI flags and the database location.
- Files on the machine of the user: the data folder (mode 0700), a socket (mode 0600), one item in the system key store, and optionally a LaunchAgent or a systemd user unit.
- CI: GitHub Actions for lint, unit tests on Ubuntu and macOS, build, govulncheck, gitleaks and the PR title check. No release job in this change.
- Delivery: two PRs. PR 1: the daemon, the encrypted store, the key store, the socket API, the CLI, the stdio MCP server and all tools. PR 2: Streamable HTTP with the API key, and `service install`.
