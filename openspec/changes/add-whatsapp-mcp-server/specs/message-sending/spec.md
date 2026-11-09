## ADDED Requirements

### Requirement: Send text
The tool `send_message` SHALL send `text` to `chat_id`. With `quote_message_id`, it SHALL send the text as a quote of that stored message. In a chat with disappearing messages, it SHALL copy the timer of the newest stored message of that chat. It SHALL return the `message_id` of the sent message. One call SHALL send one message also when the client calls again after a time-out with the same `request_id`.

#### Scenario: Plain text
- **WHEN** an agent calls `send_message` with a known `chat_id` and `text` `hello`
- **THEN** the chat on the phone shows `hello` from the user, and the result has the new `message_id`

#### Scenario: Quote of a message
- **WHEN** an agent calls `send_message` with `quote_message_id` of a stored message
- **THEN** the phone shows the text as a reply to that message

#### Scenario: Quote of a deleted message
- **WHEN** the quoted message is not stored (older than the retention time)
- **THEN** the call returns the error `not_found` and sends nothing

#### Scenario: Same request again
- **WHEN** a client calls `send_message` two times with the same `request_id`
- **THEN** the chat shows one message

### Requirement: Send only to known recipients
`send_message` and `send_file` SHALL send only to a chat that the daemon knows, or to `+<phone number>` that WhatsApp confirms as a WhatsApp user. Else they SHALL return `recipient_unknown` and send nothing.

#### Scenario: Number not on WhatsApp
- **WHEN** an agent calls `send_message` with a phone number that is not a WhatsApp user
- **THEN** the call returns `recipient_unknown` and sends nothing

### Requirement: Send a file from an allowed folder
The tool `send_file` SHALL send the file at `path` to `chat_id` with an optional `caption`. It SHALL send an image (`image/jpeg`, `image/png`, `image/webp`) as an image and each other file as a document with its base name. The client process (CLI or stdio MCP) SHALL read the file and stream it to the daemon. The file SHALL be inside a folder that `--send-dir` names; the default is the download folder. The call SHALL refuse a file whose name or any parent folder name inside the allowed folder starts with `.`, a symbolic link that resolves outside the allowed folder, and a file larger than 100 MB.

#### Scenario: Allowed file
- **WHEN** an agent calls `send_file` with a PDF inside the allowed folder
- **THEN** the chat shows a document with the PDF base name, and the result has the new `message_id`

#### Scenario: File outside the allowed folders
- **WHEN** an agent calls `send_file` with `~/.ssh/id_ed25519`
- **THEN** the call returns `path_not_allowed` and reads no byte of the file

#### Scenario: Link out of the folder
- **WHEN** the path is a symbolic link inside the allowed folder that points outside it
- **THEN** the call returns `path_not_allowed`

### Requirement: Read-only mode
With the daemon flag `--read-only`, the MCP server SHALL not list `send_message` and `send_file`, and the socket API SHALL refuse a send with `read_only`.

#### Scenario: Send in read-only mode
- **WHEN** the daemon runs with `--read-only` and a client calls the send API
- **THEN** the call returns `read_only` and sends nothing

### Requirement: Ask the client before a send
The MCP tools `send_message` and `send_file` SHALL have the annotations `readOnlyHint: false`, `destructiveHint: false` and `openWorldHint: true`, so that an MCP client can ask the user before the call. All other tools SHALL have `readOnlyHint: true`.

#### Scenario: Tool list
- **WHEN** an MCP client lists the tools
- **THEN** `send_message` and `send_file` have `openWorldHint: true` and the other tools have `readOnlyHint: true`
