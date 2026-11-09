## ADDED Requirements

### Requirement: One daemon holds the connection
`whatsapp-mcp daemon` SHALL hold the only WhatsApp connection of a data folder. It SHALL take an exclusive lock on `<data>/whatsapp-mcp.lock`. A second daemon on the same data folder SHALL exit with the message that a daemon runs already. The daemon SHALL stay connected and reconnect after a network error.

#### Scenario: Second daemon
- **WHEN** a daemon runs and the user starts a second daemon on the same data folder
- **THEN** the second daemon exits with a non-zero code and the message `another whatsapp-mcp daemon holds the connection`

### Requirement: Serve a JSON API on a Unix socket
The daemon SHALL serve a versioned JSON API under `/v1/` on the Unix socket `<data>/whatsapp-mcp.sock` with the mode 0600. When the path is longer than the system limit, it SHALL use a socket in the temporary folder and write its path to `<data>/whatsapp-mcp.sock.path`. Each response SHALL use the same JSON types as the MCP tools. Errors SHALL have a `code` from this list: `not_paired`, `locked`, `not_connected`, `logged_out`, `not_found`, `no_file`, `file_expired`, `too_large`, `path_not_allowed`, `recipient_unknown`, `read_only`, `invalid_argument`, `internal`.

#### Scenario: Status over the socket
- **WHEN** a client sends `GET /v1/status` on the socket
- **THEN** the daemon answers with the account state as JSON

### Requirement: Accept only the same user
The daemon SHALL read the user ID of each socket client (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS) and close the connection when it is not the user ID of the daemon.

#### Scenario: Other user
- **WHEN** a process of another user connects to the socket
- **THEN** the daemon closes the connection without an answer

### Requirement: Start the daemon when needed
When the CLI or the stdio MCP server finds no daemon on the socket, it SHALL start `whatsapp-mcp daemon` in the background with the same data folder, wait at most 10 seconds for the socket, and continue. The started daemon SHALL keep running after the client exits.

#### Scenario: First tool call
- **WHEN** no daemon runs and an agent calls `get_status` through the stdio MCP server
- **THEN** a daemon starts and the call returns the state

### Requirement: Install a background service
`whatsapp-mcp service install` SHALL install a macOS LaunchAgent or a systemd user unit that starts the daemon at login and restarts it after a crash. `whatsapp-mcp service uninstall` SHALL remove it.

#### Scenario: Install on macOS
- **WHEN** the user runs `whatsapp-mcp service install` on macOS
- **THEN** `launchctl print gui/<uid>/io.github.sondt2709.whatsapp-mcp` shows a running daemon

#### Scenario: Install on Linux
- **WHEN** the user runs `whatsapp-mcp service install` on Linux
- **THEN** `systemctl --user is-active whatsapp-mcp` prints `active`

### Requirement: Use the same data in the CLI
Each MCP tool SHALL have a CLI command with the same inputs as flags. With `--json`, the command SHALL print the same JSON as the tool result.

#### Scenario: CLI and MCP agree
- **WHEN** the user runs `whatsapp-mcp list-chats --json --limit 5` and an agent calls `list_chats` with `limit` 5
- **THEN** both outputs have the same `chat_id` values in the same order
