// Package store is the encrypted database of a data folder. One SQLite file
// holds the whatsmeow tables (device keys, sessions, contacts) and the tables
// of whatsapp-mcp (messages, chats, HTTP keys). Every page of the file, of its
// WAL and of its temporary files is encrypted with the Adiantum VFS, so no
// message text, name or key is on disk in plaintext.
package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum" // registers the "adiantum" VFS
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// FileName is the database file in the data folder.
const FileName = "whatsapp.db"

// ErrWrongKey is returned when the key does not open the database.
var ErrWrongKey = errors.New("the key does not open the database")

// Path returns the database file of a data folder.
func Path(dataDir string) string { return filepath.Join(dataDir, FileName) }

// Exists reports whether the data folder has a database file.
func Exists(dataDir string) bool {
	_, err := os.Stat(Path(dataDir))
	return err == nil
}

// Files returns the database file and its WAL and shared-memory files.
func Files(dataDir string) []string {
	p := Path(dataDir)
	return []string{p, p + "-wal", p + "-shm"}
}

const schema = `
CREATE TABLE IF NOT EXISTS wm_messages (
  row          INTEGER PRIMARY KEY AUTOINCREMENT,
  chat         TEXT NOT NULL,
  id           TEXT NOT NULL,
  sender       TEXT NOT NULL,
  sender_name  TEXT NOT NULL DEFAULT '',
  from_me      INTEGER NOT NULL DEFAULT 0,
  ts           INTEGER NOT NULL,
  text         TEXT NOT NULL,
  raw          BLOB,
  media_kind   TEXT NOT NULL DEFAULT '',
  media_name   TEXT NOT NULL DEFAULT '',
  media_mime   TEXT NOT NULL DEFAULT '',
  media_size   INTEGER NOT NULL DEFAULT 0,
  quote_id     TEXT NOT NULL DEFAULT '',
  quote_author TEXT NOT NULL DEFAULT '',
  quote_text   TEXT NOT NULL DEFAULT '',
  UNIQUE (chat, id)
);
CREATE INDEX IF NOT EXISTS wm_messages_ts ON wm_messages(ts);
CREATE INDEX IF NOT EXISTS wm_messages_chat_ts ON wm_messages(chat, ts);
CREATE TABLE IF NOT EXISTS wm_chats (
  chat_id TEXT PRIMARY KEY,
  name    TEXT NOT NULL DEFAULT '',
  kind    TEXT NOT NULL,
  phone   TEXT NOT NULL DEFAULT '',
  last_ts INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS wm_http_keys (
  hash    TEXT PRIMARY KEY,
  created INTEGER NOT NULL
);
`

// DB is the open database of a data folder.
type DB struct {
	sql       *sql.DB
	Container *sqlstore.Container
}

// Open opens or creates the database of the data folder with the key. The
// folder gets the mode 0700 and each file the mode 0600. It returns
// ErrWrongKey when the key does not open an existing file.
func Open(ctx context.Context, dataDir string, key []byte, log waLog.Logger) (*DB, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("store: the key has %d bytes, want 32", len(key))
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := Path(dataDir)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	// The key goes in a PRAGMA on each new connection, not in the file name,
	// so that it is not in the URI parameters of the VFS.
	hexKey := hex.EncodeToString(key)
	dsn := "file:" + path + "?vfs=adiantum&_pragma=busy_timeout(10000)&_pragma=foreign_keys(on)"
	db, err := driver.Open(dsn, func(c *sqlite3.Conn) error {
		if err := c.Exec("PRAGMA hexkey='" + hexKey + "'"); err != nil {
			return err
		}
		if err := c.Exec("PRAGMA temp_store=memory"); err != nil {
			return err
		}
		return c.Exec("PRAGMA journal_mode=WAL")
	})
	if err != nil {
		return nil, wrongKey(err)
	}
	if _, err := db.ExecContext(ctx, `SELECT count(*) FROM sqlite_master`); err != nil {
		db.Close()
		return nil, wrongKey(err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create tables: %w", err)
	}
	if log == nil {
		log = waLog.Noop
	}
	c := sqlstore.NewWithDB(db, "sqlite3", log)
	if err := c.Upgrade(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: upgrade the whatsmeow tables: %w", err)
	}
	for _, p := range Files(dataDir) {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			db.Close()
			return nil, err
		}
	}
	return &DB{sql: db, Container: c}, nil
}

func wrongKey(err error) error {
	if strings.Contains(err.Error(), "not a database") {
		return ErrWrongKey
	}
	return err
}

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

// Remove deletes the database files of a data folder.
func Remove(dataDir string) error {
	for _, p := range Files(dataDir) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Row is one stored message.
type Row struct {
	Row        int64
	Chat       string // canonical chat ID
	ID         string // WhatsApp message ID
	Sender     string // canonical user ID
	SenderName string
	FromMe     bool
	Time       time.Time
	Text       string
	Raw        []byte // protobuf of waE2E.Message; it holds the file keys
	MediaKind  string // "" for no file
	MediaName  string
	MediaMIME  string
	MediaSize  int64
	QuoteID    string // WhatsApp ID of the quoted message, in the same chat
	QuoteAuth  string
	QuoteText  string
	ChatName   string // from wm_chats, when read
}

const rowCols = `m.row, m.chat, m.id, m.sender, m.sender_name, m.from_me, m.ts, m.text, m.raw, m.media_kind, m.media_name,
	m.media_mime, m.media_size, m.quote_id, m.quote_author, m.quote_text, coalesce(c.name, '')`

func scanRow(s interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var ts int64
	err := s.Scan(&r.Row, &r.Chat, &r.ID, &r.Sender, &r.SenderName, &r.FromMe, &ts, &r.Text, &r.Raw, &r.MediaKind, &r.MediaName,
		&r.MediaMIME, &r.MediaSize, &r.QuoteID, &r.QuoteAuth, &r.QuoteText, &r.ChatName)
	r.Time = time.UnixMilli(ts)
	return r, err
}

// SaveMessage stores a message. It returns false when the message was stored already.
func (d *DB) SaveMessage(ctx context.Context, r Row) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `INSERT INTO wm_messages(chat, id, sender, sender_name, from_me, ts, text, raw,
		media_kind, media_name, media_mime, media_size, quote_id, quote_author, quote_text)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(chat, id) DO NOTHING`,
		r.Chat, r.ID, r.Sender, r.SenderName, r.FromMe, r.Time.UnixMilli(), r.Text, r.Raw,
		r.MediaKind, r.MediaName, r.MediaMIME, r.MediaSize, r.QuoteID, r.QuoteAuth, r.QuoteText)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Message returns the stored message chat/id, and false when it is not stored.
func (d *DB) Message(ctx context.Context, chat, id string) (Row, bool, error) {
	r, err := scanRow(d.sql.QueryRowContext(ctx, `SELECT `+rowCols+` FROM wm_messages m LEFT JOIN wm_chats c ON c.chat_id = m.chat
		WHERE m.chat = ? AND m.id = ?`, chat, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	return r, err == nil, err
}

// Newest returns the newest stored message of a chat, and false for none.
func (d *DB) Newest(ctx context.Context, chat string) (Row, bool, error) {
	r, err := scanRow(d.sql.QueryRowContext(ctx, `SELECT `+rowCols+` FROM wm_messages m LEFT JOIN wm_chats c ON c.chat_id = m.chat
		WHERE m.chat = ? ORDER BY m.ts DESC LIMIT 1`, chat))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	return r, err == nil, err
}

// Filter selects messages for Messages.
type Filter struct {
	Chat   string // "" for all chats
	Query  string // case-insensitive text match; "" for all
	Before time.Time
	After  time.Time
	Limit  int
}

func likeArg(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.ToLower(q)) + "%"
}

// Messages returns the messages that match the filter, the newest first.
func (d *DB) Messages(ctx context.Context, f Filter) ([]Row, error) {
	q := `SELECT ` + rowCols + ` FROM wm_messages m LEFT JOIN wm_chats c ON c.chat_id = m.chat WHERE 1=1`
	var args []any
	if f.Chat != "" {
		q += ` AND m.chat = ?`
		args = append(args, f.Chat)
	}
	if f.Query != "" {
		q += ` AND lower(m.text) LIKE ? ESCAPE '\'`
		args = append(args, likeArg(f.Query))
	}
	if !f.Before.IsZero() {
		q += ` AND m.ts < ?`
		args = append(args, f.Before.UnixMilli())
	}
	if !f.After.IsZero() {
		q += ` AND m.ts > ?`
		args = append(args, f.After.UnixMilli())
	}
	q += ` ORDER BY m.ts DESC, m.row DESC LIMIT ?`
	args = append(args, f.Limit)
	return d.rows(ctx, q, args...)
}

// After returns at most limit messages with a row number above row, the
// oldest first. With a chat, only the messages of that chat.
func (d *DB) After(ctx context.Context, row int64, chat string, limit int) ([]Row, error) {
	q := `SELECT ` + rowCols + ` FROM wm_messages m LEFT JOIN wm_chats c ON c.chat_id = m.chat WHERE m.row > ?`
	args := []any{row}
	if chat != "" {
		q += ` AND m.chat = ?`
		args = append(args, chat)
	}
	q += ` ORDER BY m.row LIMIT ?`
	args = append(args, limit)
	return d.rows(ctx, q, args...)
}

// LastRow returns the highest row number that was ever used, 0 for none. Row
// numbers are never used again (AUTOINCREMENT), also after a delete.
func (d *DB) LastRow(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := d.sql.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'wm_messages'`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n.Int64, err
}

func (d *DB) rows(ctx context.Context, q string, args ...any) ([]Row, error) {
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Prune deletes the messages older than before and returns their number.
func (d *DB) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM wm_messages WHERE ts < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Chat is one chat of the index.
type Chat struct {
	ID    string
	Name  string
	Kind  string // api.KindDirect or api.KindGroup
	Phone string
	Last  time.Time
}

// SaveChat adds or updates a chat. An empty name or phone keeps the stored
// value, and an older time keeps the stored time.
func (d *DB) SaveChat(ctx context.Context, c Chat) error {
	var last int64
	if !c.Last.IsZero() {
		last = c.Last.UnixMilli()
	}
	_, err := d.sql.ExecContext(ctx, `INSERT INTO wm_chats(chat_id, name, kind, phone, last_ts) VALUES(?,?,?,?,?)
		ON CONFLICT(chat_id) DO UPDATE SET
		  name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE wm_chats.name END,
		  phone = CASE WHEN excluded.phone <> '' THEN excluded.phone ELSE wm_chats.phone END,
		  last_ts = max(wm_chats.last_ts, excluded.last_ts)`,
		c.ID, strings.TrimSpace(c.Name), c.Kind, c.Phone, last)
	return err
}

// GetChat returns one chat, and false when the index does not have it.
func (d *DB) GetChat(ctx context.Context, id string) (Chat, bool, error) {
	var c Chat
	var last int64
	err := d.sql.QueryRowContext(ctx, `SELECT chat_id, name, kind, phone, last_ts FROM wm_chats WHERE chat_id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Kind, &c.Phone, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if last > 0 {
		c.Last = time.UnixMilli(last)
	}
	return c, err == nil, err
}

// ChatByPhone returns the direct chat with the phone number (digits only).
func (d *DB) ChatByPhone(ctx context.Context, phone string) (Chat, bool, error) {
	var id string
	err := d.sql.QueryRowContext(ctx, `SELECT chat_id FROM wm_chats WHERE phone = ? AND kind = 'direct' ORDER BY last_ts DESC LIMIT 1`, phone).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, err
	}
	return d.GetChat(ctx, id)
}

// Chats returns the chats that match the query (name or phone, case
// insensitive) and the kind, the most recent first.
func (d *DB) Chats(ctx context.Context, query, kind string, limit int) ([]Chat, error) {
	q := `SELECT chat_id, name, kind, phone, last_ts FROM wm_chats WHERE 1=1`
	var args []any
	if query != "" {
		q += ` AND (lower(name) LIKE ? ESCAPE '\' OR phone LIKE ? ESCAPE '\')`
		args = append(args, likeArg(query), likeArg(strings.TrimPrefix(query, "+")))
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY last_ts DESC, name LIMIT ?`
	args = append(args, limit)
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		var last int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Phone, &last); err != nil {
			return nil, err
		}
		if last > 0 {
			c.Last = time.UnixMilli(last)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddHTTPKey stores the hash of an HTTP API key.
func (d *DB) AddHTTPKey(ctx context.Context, hash string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO wm_http_keys(hash, created) VALUES(?, ?)`, hash, time.Now().UnixMilli())
	return err
}

// HasHTTPKey reports whether the hash is a stored HTTP API key.
func (d *DB) HasHTTPKey(ctx context.Context, hash string) (bool, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT count(*) FROM wm_http_keys WHERE hash = ?`, hash).Scan(&n)
	return n > 0, err
}

// DeleteHTTPKeys deletes all HTTP API keys and returns their number.
func (d *DB) DeleteHTTPKeys(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM wm_http_keys`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
