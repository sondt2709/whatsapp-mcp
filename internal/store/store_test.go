package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func open(t *testing.T, dir string, key []byte) *DB {
	t.Helper()
	db, err := Open(context.Background(), dir, key, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

// grep counts the canary in all database files of the folder.
func grep(t *testing.T, dir, canary string) int {
	t.Helper()
	n := 0
	for _, p := range Files(dir) {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		n += bytes.Count(b, []byte(canary))
	}
	return n
}

// TestNoPlaintextOnDisk writes a canary text into our tables and into the
// whatsmeow tables that hold plaintext (event buffer, contacts), and searches
// the files while the database is open (with its WAL) and after the close.
func TestNoPlaintextOnDisk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	canary := "wmcp-canary-" + hex.EncodeToString(newKey(t)[:6])
	db := open(t, dir, newKey(t))

	if _, err := db.SaveMessage(ctx, Row{Chat: "1@g.us", ID: "A1", Sender: "2@lid", Time: time.Now(), Text: "hello " + canary,
		SenderName: "Sender " + canary, QuoteText: "quote " + canary}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveChat(ctx, Chat{ID: "1@g.us", Name: "Group " + canary, Kind: "group"}); err != nil {
		t.Fatal(err)
	}
	dev := db.Container.NewDevice()
	jid := types.NewJID("15550000001", types.DefaultUserServer)
	dev.ID = &jid
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64),
		AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := db.Container.PutDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	var h [32]byte
	if err := dev.EventBuffer.PutBufferedEvent(ctx, h, []byte("buffered "+canary), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := dev.Contacts.PutContactName(ctx, types.NewJID("15550000002", types.DefaultUserServer), "Contact "+canary, ""); err != nil {
		t.Fatal(err)
	}

	if n := grep(t, dir, canary); n != 0 {
		t.Fatalf("open database: %d canary matches in the files, want 0", n)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if n := grep(t, dir, canary); n != 0 {
		t.Fatalf("closed database: %d canary matches in the files, want 0", n)
	}
}

func TestWrongKey(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, newKey(t))
	db.Close()
	if _, err := Open(context.Background(), dir, newKey(t), nil); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("Open with another key: err = %v, want ErrWrongKey", err)
	}
}

func TestFileModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	db := open(t, dir, newKey(t))
	defer db.Close()
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %o, want 700", info.Mode().Perm())
	}
	for _, p := range Files(dir) {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o, want 600", filepath.Base(p), info.Mode().Perm())
		}
	}
}

func TestMessages(t *testing.T) {
	ctx := context.Background()
	db := open(t, t.TempDir(), newKey(t))
	defer db.Close()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	add := func(chat, id, text string, min int) {
		t.Helper()
		ok, err := db.SaveMessage(ctx, Row{Chat: chat, ID: id, Sender: "9@lid", Time: t0.Add(time.Duration(min) * time.Minute), Text: text})
		if err != nil || !ok {
			t.Fatalf("SaveMessage %s: %v %v", id, ok, err)
		}
	}
	add("a@g.us", "1", "Hello World", 0)
	add("a@g.us", "2", "second 100%", 1)
	add("b@lid", "3", "hello again", 2)
	if ok, _ := db.SaveMessage(ctx, Row{Chat: "a@g.us", ID: "1", Sender: "9@lid", Time: t0, Text: "x"}); ok {
		t.Fatal("a duplicate message was stored")
	}

	all, _ := db.Messages(ctx, Filter{Limit: 10})
	if len(all) != 3 || all[0].ID != "3" || all[2].ID != "1" {
		t.Fatalf("Messages: want 3 newest first, got %v", ids(all))
	}
	if got, _ := db.Messages(ctx, Filter{Chat: "a@g.us", Limit: 10}); len(got) != 2 {
		t.Fatalf("chat filter: got %v", ids(got))
	}
	if got, _ := db.Messages(ctx, Filter{Query: "HELLO", Limit: 10}); len(got) != 2 {
		t.Fatalf("query is not case-insensitive: got %v", ids(got))
	}
	if got, _ := db.Messages(ctx, Filter{Query: "%", Limit: 10}); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("query %% must match only a text with %%: got %v", ids(got))
	}
	if got, _ := db.Messages(ctx, Filter{After: t0, Before: t0.Add(2 * time.Minute), Limit: 10}); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("time filter: got %v", ids(got))
	}
	if got, _ := db.Messages(ctx, Filter{Limit: 1}); len(got) != 1 {
		t.Fatalf("limit: got %v", ids(got))
	}

	after, _ := db.After(ctx, all[2].Row, "", 10)
	if len(after) != 2 || after[0].ID != "2" {
		t.Fatalf("After: want 2 then 3, got %v", ids(after))
	}
	last := all[0].Row
	if lr, _ := db.LastRow(ctx); lr < last {
		t.Fatalf("LastRow = %d, want %d or more", lr, last)
	}
	if n, _ := db.Prune(ctx, t0.Add(10*time.Minute)); n != 3 {
		t.Fatalf("Prune deleted %d, want 3", n)
	}
	add("a@g.us", "4", "new", 20)
	if r, _, _ := db.Message(ctx, "a@g.us", "4"); r.Row <= last {
		t.Fatalf("row %d after a prune is not above the last row %d", r.Row, last)
	}
}

func TestChats(t *testing.T) {
	ctx := context.Background()
	db := open(t, t.TempDir(), newKey(t))
	defer db.Close()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.SaveChat(ctx, Chat{ID: "1@lid", Name: "Alice", Kind: "direct", Phone: "15550001111", Last: t0}))
	must(db.SaveChat(ctx, Chat{ID: "2@g.us", Name: "Team Alpha", Kind: "group", Last: t0.Add(time.Hour)}))
	must(db.SaveChat(ctx, Chat{ID: "1@lid", Kind: "direct", Last: t0.Add(-time.Hour)}))

	c, ok, _ := db.GetChat(ctx, "1@lid")
	if !ok || c.Name != "Alice" || c.Phone != "15550001111" || !c.Last.Equal(t0) {
		t.Fatalf("an empty name or an older time changed the chat: %+v", c)
	}
	if got, _ := db.Chats(ctx, "", "", 10); len(got) != 2 || got[0].ID != "2@g.us" {
		t.Fatalf("Chats: want the newest first, got %+v", got)
	}
	if got, _ := db.Chats(ctx, "team", "", 10); len(got) != 1 || got[0].ID != "2@g.us" {
		t.Fatalf("query by name: %+v", got)
	}
	if got, _ := db.Chats(ctx, "+1555000", "", 10); len(got) != 1 || got[0].ID != "1@lid" {
		t.Fatalf("query by phone: %+v", got)
	}
	if got, _ := db.Chats(ctx, "", "group", 10); len(got) != 1 {
		t.Fatalf("kind filter: %+v", got)
	}
	if c, ok, _ := db.ChatByPhone(ctx, "15550001111"); !ok || c.ID != "1@lid" {
		t.Fatalf("ChatByPhone: %+v %v", c, ok)
	}
}

func TestHTTPKeys(t *testing.T) {
	ctx := context.Background()
	db := open(t, t.TempDir(), newKey(t))
	defer db.Close()
	if err := db.AddHTTPKey(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.HasHTTPKey(ctx, "h1"); !ok {
		t.Fatal("HasHTTPKey is false for a stored hash")
	}
	if ok, _ := db.HasHTTPKey(ctx, "h2"); ok {
		t.Fatal("HasHTTPKey is true for another hash")
	}
	if n, _ := db.DeleteHTTPKeys(ctx); n != 1 {
		t.Fatalf("DeleteHTTPKeys = %d, want 1", n)
	}
}

func ids(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
