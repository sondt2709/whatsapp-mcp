# CLAUDE.md

This file gives guidance to Claude Code (claude.ai/code) when it works with code in this repository.

## What this is

`whatsapp-mcp` is a Go binary. It links to a personal WhatsApp account as a linked device (whatsmeow) and gives AI agents the WhatsApp functions through MCP and a CLI: list chats, read and search messages, wait for new messages, send text, send files, download files. One static binary (`CGO_ENABLED=0`), macOS and Linux.

The design is in `openspec/changes/add-whatsapp-mcp-server/design.md` (after the archive: `openspec/specs/`).

## Commands

```sh
task build      # bin/whatsapp-mcp
task test       # go test -race ./... (unit and e2e, no WhatsApp account)
task lint       # golangci-lint v2 (.golangci.yml)
task leakcheck  # forbidden terms from .leakcheck.local (not committed)
task live       # live test with two linked devices of a real account (local only)
```

## Architecture

- `whatsapp-mcp daemon` holds the one WhatsApp connection of a data folder (lock `<data>/whatsapp-mcp.lock`), the encrypted database and an event hub. It serves a JSON API under `/v1/` on the Unix socket `<data>/whatsapp-mcp.sock` (mode 0600, peer user ID check).
- The CLI commands and `whatsapp-mcp mcp` (stdio MCP server) are clients of that API (`internal/client`). They start the daemon when none answers. They read and write the user files (send, download), so the daemon writes no temporary file.
- The database is one SQLite file with the Adiantum VFS (`github.com/ncruces/go-sqlite3`). It holds the whatsmeow tables and our tables. The key is in the macOS Keychain (Security.framework through purego), the Linux Secret Service, or comes from a passphrase.
- IDs: `user_id` LID first, `chat_id`, `message_id` = `<chat_id>/<id>`, `file_id` = `message_id`. All inputs go through one resolver.

## Rules

- **Writing:** use the ASD-STE100 style for all text (docs, comments, errors, OpenSpec artifacts, PRs). Use short sentences, the active voice and one term for one concept. Name the specific file, command or value.
- **No personal data in committed files:** no local paths, real names, phone numbers, real server names or other project names. Run `task leakcheck` and read `git diff --cached` before each commit, especially the md, txt, yaml and jsonl files. `tmp/` holds local files and is git-ignored.
- **Never send a read state:** no code calls `MarkRead`, `SendPresence`, `SubscribePresence` or `SendChatPresence`. A test checks this.
- **No plaintext on disk:** no message text, name or key in a file of the data folder, and no message text in a log.
- **Fixtures must copy real data.** WhatsApp fixtures come from the capture tool on a paired account, with addresses, IDs, names and texts replaced. Read its output before a commit. Do not write the shape of external data by hand.
- **A new test must be able to fail.** Break the code one time and confirm that the test fails, then restore the code.
- **OpenSpec:** significant changes go through `openspec/changes/<name>/` (proposal, design, specs, tasks) before code.
- **PR titles** use Conventional Commits (`.github/workflows/pr-title.yml`). Merges are squash-only.

## Facts about WhatsApp and whatsmeow

- whatsmeow sends no read receipt and no presence unless the code asks. It sends delivery receipts.
- One set of device keys can have one connection. A second connection replaces the first (`events.StreamReplaced`).
- whatsmeow writes message plaintext to its own tables (`whatsmeow_event_buffer`, `whatsmeow_retry_buffer`). This is why the whole file is encrypted.
- The WhatsApp CDN keeps a file for about two weeks. After that, a download gives HTTP 403, 404 or 410 (`file_expired`).
- The own chat ("Message yourself") arrives with the LID address, also when the sender used the phone address.
- A media retry answer can arrive at the next connect, so match it to the message ID.
