## ADDED Requirements

### Requirement: Store each received message
The daemon SHALL store each message of a direct chat or a group that it receives: live messages, the offline queue at connect, and the history sync at pairing. It SHALL store the sender, the time, the text, the quote reference and the raw message (which holds the file keys). It SHALL ignore edits, reactions, deletes, poll votes, status updates, broadcast lists and WhatsApp Channels. A message that it receives two times SHALL be stored one time.

#### Scenario: Offline for some hours
- **WHEN** the daemon was stopped for some hours and other people sent messages, and the daemon starts again
- **THEN** the daemon stores the queued messages, and `list_messages` returns them

#### Scenario: History at pairing
- **WHEN** the pairing ends and the phone sends the history sync
- **THEN** the daemon stores the chats and the messages of the history that are inside the retention time

### Requirement: Keep messages for a limited time
The daemon SHALL delete the stored messages that are older than the retention time. The default retention SHALL be 90 days, and the flag `--retention` SHALL change it. The daemon SHALL run the deletion at start and each hour.

#### Scenario: Old message deleted
- **WHEN** a stored message is older than the retention time and the deletion runs
- **THEN** `list_messages` no longer returns the message and `download_file` for it returns `not_found`

### Requirement: Use one canonical ID for each item
All outputs of the CLI, the socket API and the MCP tools SHALL use these IDs:
- `user_id`: the LID address `<n>@lid` when the daemon knows it, else the phone address `<n>@s.whatsapp.net`.
- `chat_id`: for a direct chat, the `user_id` of the other person (the own chat uses the own `user_id`); for a group, `<n>@g.us`.
- `message_id`: `<chat_id>/<WhatsApp message ID>`.
- `file_id`: equal to the `message_id` of the message that has the file.
All inputs SHALL accept the canonical ID, the other address form of the same user, and `+<phone number>`, and SHALL resolve them to the same chat. Names SHALL be labels only, never keys.

#### Scenario: Two address forms of one person
- **WHEN** a person sends one message with the phone address and one message with the LID address, and the daemon knows the mapping
- **THEN** both messages have the same `chat_id` and the same sender `user_id`

#### Scenario: Input with a phone number
- **WHEN** an agent calls `list_messages` with `chat_id` `+15551234567` for a known person
- **THEN** the result has the same messages and the same canonical `chat_id` as a call with the canonical ID

#### Scenario: Own chat
- **WHEN** the user sends a message to the own chat ("Message yourself") from the phone
- **THEN** the stored message has the own `user_id` as its `chat_id`

### Requirement: Keep messages unread
The binary SHALL never send a read receipt, a played receipt, presence or a change of the read state of a chat. Reading, waiting for, downloading or sending messages SHALL not change the unread count on the phone.

#### Scenario: No code path sends a read state
- **WHEN** the unit tests check the calls to the WhatsApp client
- **THEN** no code path calls `MarkRead`, `SendPresence`, `SubscribePresence`, `SendChatPresence` or an app state patch of the read state

#### Scenario: Agent reads a chat
- **WHEN** an agent calls `list_messages` and `download_file` for an unread chat
- **THEN** the chat stays unread on the phone (checked by the user; not testable by a script)

### Requirement: Keep message text out of logs
The daemon logs SHALL not contain a message text, a file name from a message or a contact name.

#### Scenario: Log after a received message
- **WHEN** the daemon receives a message with a canary text and logs at the debug level
- **THEN** the log output has zero matches for the canary text
