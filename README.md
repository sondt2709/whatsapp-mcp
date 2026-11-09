# whatsapp-mcp

`whatsapp-mcp` gives AI agents the WhatsApp functions of your personal account through [MCP](https://modelcontextprotocol.io) and a CLI. It links to your account as a linked device (like WhatsApp Web), with the Go library [whatsmeow](https://github.com/tulir/whatsmeow).

- List chats, read and search messages, wait for new messages.
- Send text (also as a reply), send files, download files.
- Messages stay unread on your phone: the tool never sends read receipts or presence.
- All local data is encrypted. The key is in the macOS Keychain or the Linux Secret Service.
- One static Go binary for macOS and Linux. No CGO, no Python, no Node.

> This project is not affiliated with WhatsApp or Meta. It uses an unofficial protocol library. WhatsApp can block accounts that it thinks are automated. Use it for your own account, and send only what you would send yourself.

## Install

```sh
go install github.com/sondt2709/whatsapp-mcp/cmd/whatsapp-mcp@latest
```

Or build from the source (Go 1.26 or later):

```sh
git clone https://github.com/sondt2709/whatsapp-mcp && cd whatsapp-mcp
CGO_ENABLED=0 go build -o whatsapp-mcp ./cmd/whatsapp-mcp
```

## Link your account

```sh
whatsapp-mcp pair                         # shows a QR code
whatsapp-mcp pair --phone +15551234567    # or: a pairing code to type on the phone
```

On the phone, open WhatsApp › Settings › Linked devices › Link a device, and scan the code. The phone lists the device as `whatsapp-mcp`. Then check the state:

```sh
whatsapp-mcp status
```

## Add it to an MCP client

Claude Code:

```sh
claude mcp add whatsapp -- whatsapp-mcp mcp
```

Other clients (JSON configuration):

```json
{
  "mcpServers": {
    "whatsapp": { "command": "whatsapp-mcp", "args": ["mcp"] }
  }
}
```

Useful flags of `whatsapp-mcp mcp`:

| Flag | Meaning |
|---|---|
| `--send-dir <folder>` | A folder that `send_file` may read. Repeat it for more folders. The default is the download folder. |
| `--download-dir <folder>` | The default folder of `download_file`. The default is `~/Downloads` (`$XDG_DOWNLOAD_DIR` on Linux). |
| `--read-only` | Start the daemon with sending turned off. The send tools are then not listed. |
| `--retention <duration>` | Delete stored messages older than this. The default is `2160h` (90 days). |
| `--data-dir <folder>` | The data folder. Use a second folder for a second account. |

## Tools

| Tool | What it does |
|---|---|
| `get_status` | The state of the link (`not_paired`, `locked`, `connecting`, `connected`, `logged_out`), the account, and the next action of the user. |
| `list_chats` | Direct chats and groups, the most recent first. Search with `query` (name or phone number). |
| `list_messages` | Stored messages, the newest first, of one chat or of all chats. Search with `query`, filter with `before` and `after`. Returns a `cursor`. |
| `wait_for_messages` | New messages after a `cursor`, the oldest first. Returns at once when there are some, else waits up to `timeout_seconds` (default 60, maximum 600). |
| `send_message` | Send text to a chat. `quote_message_id` sends a reply. The same `request_id` never sends a second message. |
| `send_file` | Send a file from an allowed folder. Images go as images, other files as documents. Maximum 100 MB. |
| `download_file` | Save the file of a message in a folder. Never overwrites a file. |

Each tool has a CLI command with the same inputs. With `--json`, the command prints the same JSON as the tool:

```sh
whatsapp-mcp list-chats --query team --json
whatsapp-mcp list-messages --chat +15551234567 --limit 20
whatsapp-mcp wait --cursor <cursor> --timeout 120
whatsapp-mcp send --chat +15551234567 --text "On my way"
whatsapp-mcp send-file --chat <chat_id> --path ~/Downloads/report.pdf
whatsapp-mcp download --file <file_id> --dir ~/Downloads
```

### IDs

Each item has one ID, and every tool and command uses it the same way:

| ID | Format |
|---|---|
| `user_id` | `<n>@lid` (the WhatsApp LID address) when it is known, else `<phone>@s.whatsapp.net` |
| `chat_id` | The `user_id` of the other person for a direct chat (your own `user_id` for "Message yourself"), or `<n>@g.us` for a group |
| `message_id` | `<chat_id>/<WhatsApp message ID>` |
| `file_id` | The `message_id` of the message with the file |

Inputs also accept `+<phone number>` and the other address form of a person.

## How it works

```
 MCP client ──stdio──▶ whatsapp-mcp mcp ──┐
 your shell ─────────▶ whatsapp-mcp <cmd> ─┤  Unix socket, /v1 JSON API (mode 0600, same user only)
                                           ▼
              whatsapp-mcp daemon ── WhatsApp (linked device)
                      │
                      ▼
              whatsapp.db (encrypted; key in the system key store)
```

- One daemon holds the WhatsApp connection of a data folder. The other commands start it when it does not run. All MCP sessions share it, so many agents can use one account.
- The daemon stores the messages that it receives: live, from the offline queue after a restart, and from the history that the phone sends at pairing.
- `send_file` and `download_file` read and write your files in the client process (the CLI or `whatsapp-mcp mcp`), which has the folder access of your terminal or MCP client. The daemon writes no temporary files.

## Security and privacy

- **Encrypted data.** The database holds the device keys, the contacts and the messages. Each page of the file, its WAL and its temporary files is encrypted with the Adiantum VFS of [go-sqlite3](https://github.com/ncruces/go-sqlite3) and a random 256-bit key.
- **Key store.**
  - macOS: the key is a generic password in the login Keychain, service `whatsapp-mcp`. Only the `whatsapp-mcp` binary can read it without a dialog. After an update, macOS asks one time.
  - Linux: the key is in the Secret Service (GNOME Keyring, KWallet). Without a Secret Service, pair with `whatsapp-mcp pair --passphrase` and unlock after each daemon start with `whatsapp-mcp unlock`.
  - The key is never written to a file.
- **No read state.** The tool never marks messages as read and never shows you as online. WhatsApp still sends delivery receipts (two grey ticks), as your phone does.
- **Sending.** `send_file` reads only from the allowed folders, and never a file or folder whose name starts with `.`. MCP clients can ask you before each send (the send tools have `openWorldHint`). Use `--read-only` to turn sending off.
- **Untrusted text.** Message text comes from other people. The tools remove hidden characters, and the tool descriptions tell the agent to treat the text as data.
- **Retention.** Messages are deleted after 90 days (`--retention`). `whatsapp-mcp logout` unlinks the device and deletes the database and the key.
- **Limits.**
  - A program that runs as your user can call the daemon like an agent can.
  - On Linux, any program of your user can read an unlocked keyring.
  - Downloaded files are plaintext in the folder that you choose.
  - Use full-disk encryption (FileVault, LUKS) and encrypted swap.

## Troubleshooting

| State or error | What to do |
|---|---|
| `not_paired` | Run `whatsapp-mcp pair`. |
| `locked` | macOS: allow `whatsapp-mcp` in the Keychain dialog, then call again. Linux with a passphrase: run `whatsapp-mcp unlock`. If the key item was deleted: run `whatsapp-mcp logout`, then `whatsapp-mcp pair`. |
| `logged_out` | The phone removed the device. Run `whatsapp-mcp logout`, then `whatsapp-mcp pair`. |
| `file_expired` | WhatsApp keeps files on its servers for about two weeks. Open the file on the phone. |
| `path_not_allowed` | Move the file into a `--send-dir` folder, or add the folder with `--send-dir`. |

The daemon log is `daemon.log` in the data folder. It never contains message text.

## Development

```sh
task build      # bin/whatsapp-mcp
task test       # unit and end-to-end tests (a fake WhatsApp backend; no account)
task lint       # golangci-lint
task leakcheck  # forbidden terms from .leakcheck.local (not committed)
```

Changes go through [OpenSpec](https://github.com/Fission-AI/OpenSpec) in `openspec/`.

## License

MIT
