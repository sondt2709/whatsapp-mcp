## ADDED Requirements

### Requirement: Download a file on request
The tool `download_file` SHALL save the file of a stored message to `dir` and return `path`, `name`, `mime`, `size` and `sha256`. The default `dir` SHALL be `~/Downloads`; on Linux it SHALL be `XDG_DOWNLOAD_DIR` when set. The daemon SHALL download only when a client asks, never when a message arrives. The daemon SHALL stream the bytes to the client process, and the client process SHALL write the file. The daemon SHALL write no temporary file.

#### Scenario: Recent file
- **WHEN** an agent calls `download_file` with the `file_id` of a document that arrived today
- **THEN** the file is in `~/Downloads` with the document name, and its SHA-256 matches the hash in the message

#### Scenario: Message without a file
- **WHEN** the `file_id` is a message without a file
- **THEN** the call returns `no_file`

### Requirement: Use a safe file name and never overwrite
The file name SHALL be the base name that the sender gave, without hidden characters, path separators, `:` and leading dots, at most 255 bytes. With no usable name, it SHALL be `whatsapp-<WhatsApp message ID>` and the extension of the MIME type. When the name exists, the client SHALL use `name (1).ext`, `name (2).ext` and so on. The `dir` SHALL exist; the call SHALL not create it.

#### Scenario: Name with a path
- **WHEN** the sender gave the file name `../../.zshrc`
- **THEN** the saved name is `zshrc` inside `dir`

#### Scenario: Name exists
- **WHEN** `report.pdf` exists in `dir`
- **THEN** the call saves `report (1).pdf` and the old file does not change

#### Scenario: Folder does not exist
- **WHEN** `dir` does not exist
- **THEN** the call returns `invalid_argument` and writes nothing

### Requirement: Mark the file as downloaded
On macOS the client SHALL set the `com.apple.quarantine` attribute on the saved file. On Linux it SHALL set nothing.

#### Scenario: macOS quarantine
- **WHEN** a file is saved on macOS
- **THEN** `xattr -p com.apple.quarantine <path>` prints a value

### Requirement: Report an expired file
When the WhatsApp CDN answers HTTP 403, 404 or 410, the call SHALL return `file_expired` with the message that the file is only on the phone now. A file larger than 100 MB SHALL return `too_large` without a download.

#### Scenario: File of last month
- **WHEN** an agent calls `download_file` for a file that arrived 30 days ago and the CDN answers 403
- **THEN** the call returns `file_expired` and writes nothing
