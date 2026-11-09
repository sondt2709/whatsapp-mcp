## ADDED Requirements

### Requirement: Pair with a QR code or a pairing code
The CLI command `whatsapp-mcp pair` SHALL link the binary to a WhatsApp account as a linked device with the name `whatsapp-mcp`. Without flags it SHALL show each QR code in the terminal. With `--phone <international number>` it SHALL show one pairing code to type on the phone. The command SHALL refuse to pair a data folder that is paired already.

#### Scenario: Pair with a QR code
- **WHEN** the user runs `whatsapp-mcp pair` and scans the QR code on the phone
- **THEN** the command prints `paired`, the data folder holds the encrypted database, and the phone lists `whatsapp-mcp` in Linked devices

#### Scenario: Nobody scans the code
- **WHEN** WhatsApp stops giving QR codes before a scan (about 160 seconds)
- **THEN** the command exits with a non-zero code and the message `pairing timed out`

#### Scenario: Data folder paired already
- **WHEN** the user runs `whatsapp-mcp pair` on a paired data folder
- **THEN** the command exits with a non-zero code and tells the user to run `whatsapp-mcp logout` first

### Requirement: Encrypt all local data
The database (device keys, contacts, chats, messages) SHALL be encrypted on disk with a random 256-bit key and the Adiantum VFS. The data folder SHALL have the mode 0700 and each file in it the mode 0600. No file in the data folder SHALL contain a message text, a contact name or a key in plaintext.

#### Scenario: A canary text is not on disk
- **WHEN** the account receives a message that contains a random canary text, and a test searches all files of the data folder for that text while the daemon runs and after it stops
- **THEN** the search finds zero matches

#### Scenario: A wrong key
- **WHEN** the daemon opens the database with a key that is not the key of the database
- **THEN** the open fails and the daemon reports the state `locked` with the message to pair again

### Requirement: Keep the key in the system key store
On macOS the key SHALL be a generic password in the login Keychain, with the service `whatsapp-mcp` and an access list that trusts only the `whatsapp-mcp` binary. On Linux the key SHALL be in the Secret Service when one runs; else the key SHALL come from a passphrase that the user types to `whatsapp-mcp unlock` (Argon2id). The binary SHALL never write the key to a file.

#### Scenario: macOS access list
- **WHEN** `whatsapp-mcp pair` creates the Keychain item
- **THEN** the decrypt entry of the item access list names only the `whatsapp-mcp` binary

#### Scenario: Linux without a Secret Service
- **WHEN** the daemon starts on Linux and no Secret Service answers
- **THEN** the daemon reports the state `locked` until the user runs `whatsapp-mcp unlock` and types the passphrase

#### Scenario: Key item deleted
- **WHEN** the key item is missing and the database exists
- **THEN** the daemon reports the state `locked` with the message to run `whatsapp-mcp logout` and pair again

### Requirement: Log out
The CLI command `whatsapp-mcp logout` SHALL unlink the device on the phone, delete the database files and delete the key item. It SHALL delete the local files also when the unlink fails, and then print how to remove the device on the phone.

#### Scenario: Log out a paired account
- **WHEN** the user runs `whatsapp-mcp logout`
- **THEN** the phone no longer lists the device, the data folder has no database files, and the key store has no `whatsapp-mcp` item for that data folder

### Requirement: Report the account state
The account SHALL have exactly one state of `not_paired`, `locked`, `connecting`, `connected` or `logged_out`. `whatsapp-mcp status` and the MCP tool `get_status` SHALL return the state, the phone number and the push name of the account, and the next action for the user when the state is not `connected`.

#### Scenario: Not paired
- **WHEN** an agent calls `get_status` on a data folder that was never paired
- **THEN** the result has the state `not_paired` and the action `ask the user to run "whatsapp-mcp pair" in a terminal`

#### Scenario: Phone unlinked the device
- **WHEN** the user removes the device on the phone while the daemon runs
- **THEN** the state becomes `logged_out` and the action tells the user to run `whatsapp-mcp logout`, then `whatsapp-mcp pair`
