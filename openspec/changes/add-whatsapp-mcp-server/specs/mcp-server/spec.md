## ADDED Requirements

### Requirement: Serve the tools over stdio
`whatsapp-mcp mcp` SHALL be an MCP server on stdin and stdout. It SHALL list the tools `get_status`, `list_chats`, `list_messages`, `wait_for_messages`, `send_message`, `send_file` and `download_file`, each with a JSON Schema of its input and output, and return structured content. It SHALL write nothing to stdout except MCP messages.

#### Scenario: Tool list
- **WHEN** an MCP client sends `tools/list`
- **THEN** the result has the seven tools, each with an input schema and an output schema

#### Scenario: Call a tool
- **WHEN** an MCP client calls `list_chats` with `limit` 3
- **THEN** the result has `structuredContent` with at most three chats

### Requirement: Return errors that tell the next action
A tool error SHALL be a tool result with `isError: true`, the error `code` and a message that names the next action. For `not_paired`, `locked` and `logged_out` the message SHALL name the CLI command that the user must run.

#### Scenario: Locked key store
- **WHEN** the daemon is `locked` and an agent calls `list_chats`
- **THEN** the result has `isError: true`, `code` `locked` and the action to approve the Keychain dialog or to run `whatsapp-mcp unlock`

### Requirement: Serve the tools over Streamable HTTP with an API key
With `whatsapp-mcp daemon --http 127.0.0.1:<port>`, the daemon SHALL serve the same tools with the MCP Streamable HTTP transport at `/mcp`. It SHALL refuse an address that is not a loopback address. It SHALL require the header `Authorization: Bearer <key>`. It SHALL refuse a request with an `Origin` header that is not a loopback origin. `whatsapp-mcp http-key create` SHALL make a new key, show it one time and keep only its SHA-256 hash in the encrypted database. `whatsapp-mcp http-key revoke` SHALL delete it. With HTTP, `send_file` and `download_file` SHALL use the folders of the daemon.

#### Scenario: Missing key
- **WHEN** a client sends an MCP request to `/mcp` without the `Authorization` header
- **THEN** the daemon answers HTTP 401

#### Scenario: Valid key
- **WHEN** a client sends `tools/list` with the right key
- **THEN** the daemon answers with the seven tools

#### Scenario: Non-loopback address
- **WHEN** the user starts the daemon with `--http 0.0.0.0:8080`
- **THEN** the daemon exits with the message that only a loopback address is allowed

#### Scenario: Foreign origin
- **WHEN** a request has the header `Origin: https://example.com`
- **THEN** the daemon answers HTTP 403
