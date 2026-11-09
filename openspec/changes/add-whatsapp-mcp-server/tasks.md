## 1. Repository setup (PR 1)

- [x] 1.1 Add `go.mod` (`github.com/sondt2709/whatsapp-mcp`), `LICENSE` (MIT), `Taskfile.yml` (build, test, lint, leakcheck, live), `.golangci.yml`, `scripts/leakcheck.sh`
- [x] 1.2 Add `CLAUDE.md` with the rules: writing style, no personal data in committed files, fixtures copy real data, each new test must be able to fail, OpenSpec for significant changes, Conventional Commits PR titles
- [x] 1.3 Add `.github/workflows/ci.yml` (lint, unit tests on Ubuntu and macOS, build of darwin and linux for amd64 and arm64 with `CGO_ENABLED=0`, govulncheck, gitleaks) and `.github/workflows/pr-title.yml`
- [x] 1.4 Add `internal/api`: the JSON types of chats, messages, files, status and the error codes

## 2. Encrypted store and key store (PR 1)

- [x] 2.1 `internal/keystore`: the interface, the macOS back end through purego, the Linux Secret Service back end, the passphrase back end (Argon2id), and a memory back end for tests
- [x] 2.2 `internal/store`: open the Adiantum database with the key, file modes 0700 and 0600, the whatsmeow container on the same `*sql.DB`, the tables `messages`, `chats`, `http_keys`
- [x] 2.3 Store queries: save a message (ignore duplicates), list chats (query, kind, limit), list messages (chat, query, before, after, limit), rows after a cursor, retention delete
- [x] 2.4 Test: a canary text in a stored message, a contact name and the whatsmeow event buffer give zero matches in all data files; a wrong key fails
- [ ] 2.5 Test the keystore back ends in CI (temporary keychain on macOS, `gnome-keyring-daemon` on Ubuntu)

## 3. WhatsApp layer (PR 1)

- [x] 3.1 `internal/whatsapp`: the `waClient` interface without read or presence methods, the connection, the device name `whatsapp-mcp`, the logger that drops message text
- [x] 3.2 Pairing with a QR code and with a pairing code; logout that deletes the database and the key item
- [x] 3.3 Event to row: text and media placeholders, quote reference, kinds to ignore, history sync, chat names from groups and contacts
- [x] 3.4 ID resolver: LID first, `+<phone>` input, both forms of one person to one canonical ID, own chat
- [x] 3.5 Send text (plain, quote, disappearing timer, message ID from `request_id`), known-recipient check (`IsOnWhatsApp`)
- [x] 3.6 Send a file (image or document, upload, caption)
- [x] 3.7 Download a file into memory (100 MB limit, HTTP 403/404/410 to `file_expired`)
- [x] 3.8 Unread AST test: no call of `MarkRead`, `SendPresence`, `SubscribePresence`, `SendChatPresence` on the client
- [ ] 3.9 Capture tool for replay fixtures (addresses, IDs, names and texts replaced, file bytes removed); capture from a paired account, read the output, add replay tests

## 4. Daemon and socket API (PR 1)

- [x] 4.1 `internal/daemon`: data folder lock, account states, the connection loop with reconnect, retention job each hour
- [x] 4.2 Socket server with `/v1` endpoints for each tool, the long-path fallback, peer UID check, error codes
- [x] 4.3 Event hub and `wait` endpoint with cursor and timeout
- [x] 4.4 File streaming endpoints for download and send
- [x] 4.5 `--read-only` flag
- [ ] 4.6 `internal/client`: socket client and the automatic start of the daemon

## 5. Files and sanitizing (PR 1)

- [x] 5.1 `internal/files`: safe name, never overwrite, quarantine on macOS, default download folder (XDG on Linux)
- [x] 5.2 Send-path rule: `--send-dir`, hidden names, links out of the folder, 100 MB limit
- [x] 5.3 `internal/sanitize`: remove hidden characters from text, names and quotes

## 6. CLI and stdio MCP (PR 1)

- [ ] 6.1 `cmd/whatsapp-mcp`: `pair`, `unlock`, `status`, `logout`, `daemon`, and one command for each tool with `--json`
- [ ] 6.2 `internal/mcpserver`: the seven tools with schemas, annotations, untrusted-text descriptions, error results with actions, progress notifications in `wait_for_messages`
- [ ] 6.3 `whatsapp-mcp mcp` stdio command
- [ ] 6.4 MCP e2e tests: the real binary over stdio with the fake back end, every tool and every error state, CLI `--json` equal to the tool result
- [ ] 6.5 `README.md`: install, pair, add to an MCP client, tools, security model and its limits

## 7. Live verification (PR 1)

- [ ] 7.1 `task live`: pair devices A and B, A sends a canary text and a file to the own chat, B receives them through `wait_for_messages`, B downloads the file with the same SHA-256, canary grep of both data folders, then logout of both
- [ ] 7.2 Measure the tool-call time limit of Claude Code for `wait_for_messages`

## 8. HTTP transport and service (PR 2)

- [ ] 8.1 `daemon --http`: loopback only, Streamable HTTP at `/mcp`, Bearer key check, Origin check
- [ ] 8.2 `http-key create` and `http-key revoke` (hash in the database)
- [ ] 8.3 e2e tests: no key gives 401, right key lists the tools, foreign origin gives 403, non-loopback address refused
- [ ] 8.4 `service install` and `service uninstall`: LaunchAgent on macOS, systemd user unit on Linux; test on both
- [ ] 8.5 Update `README.md` for HTTP and the service
