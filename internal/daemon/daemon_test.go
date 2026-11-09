package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/client"
	"github.com/sondt2709/whatsapp-mcp/internal/daemon"
	"github.com/sondt2709/whatsapp-mcp/internal/fakewa"
	"github.com/sondt2709/whatsapp-mcp/internal/keystore"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

type env struct {
	dir   string
	inbox string
	keys  *keystore.Memory
	cl    *client.Client
	stop  func()
}

// start runs a daemon with the fake backend in the data folder.
func start(t *testing.T, dir string, keys *keystore.Memory, readOnly bool) *env {
	t.Helper()
	inbox := filepath.Join(dir, "fake-inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	factory := fakewa.Factory(inbox)
	cfg := daemon.Config{DataDir: dir, Retention: 90 * 24 * time.Hour, ReadOnly: readOnly, Version: "test", Keys: keys,
		Backend: func(ctx context.Context, db *store.DB, r time.Duration, l *slog.Logger, on func()) (daemon.Backend, error) {
			return factory(ctx, db, r, l, on)
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, cfg) }()
	cl := client.New(dir)
	cl.NoStart = true
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := cl.Status(context.Background()); err == nil {
			break
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("daemon stopped: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the daemon did not answer")
		}
		time.Sleep(20 * time.Millisecond)
	}
	e := &env{dir: dir, inbox: inbox, keys: keys, cl: cl}
	var once sync.Once
	e.stop = func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("daemon: %v", err)
			}
		})
	}
	t.Cleanup(e.stop)
	return e
}

func (e *env) pair(t *testing.T) {
	t.Helper()
	var qr string
	err := e.cl.Pair(context.Background(), daemon.PairRequest{}, func(ev daemon.PairEvent) { qr = ev.QR })
	if err != nil || qr == "" {
		t.Fatalf("pair: %v, qr %q", err, qr)
	}
}

var seq int

// drop puts one event in the fake inbox.
func (e *env) drop(t *testing.T, ev fakewa.Event) {
	t.Helper()
	seq++
	b, _ := json.Marshal(ev)
	tmp := filepath.Join(e.inbox, fmt.Sprintf("%06d.tmp", seq))
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, strings.TrimSuffix(tmp, ".tmp")+".json"); err != nil {
		t.Fatal(err)
	}
}

func text(id, from, body string, at time.Time) fakewa.Event {
	m, _ := proto.Marshal(&waE2E.Message{Conversation: proto.String(body)})
	return fakewa.Event{Chat: from, Sender: from, ID: id, Time: at.UnixMilli(), PushName: "Alice", Message: m}
}

// eventually polls f until it returns true.
func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestNotPaired(t *testing.T) {
	e := start(t, t.TempDir(), keystore.NewMemory(), false)
	s, _ := e.cl.Status(context.Background())
	if s.State != api.StateNotPaired || !strings.Contains(s.Action, "whatsapp-mcp pair") {
		t.Fatalf("status: %+v", s)
	}
	if _, err := e.cl.ListChats(context.Background(), api.ListChatsInput{}); api.CodeOf(err) != api.NotPaired {
		t.Fatalf("list_chats before pairing: %v", err)
	}
}

func TestFlow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keys := keystore.NewMemory()
	e := start(t, dir, keys, false)
	e.pair(t)

	s, _ := e.cl.Status(ctx)
	if s.State != api.StateConnected || s.UserID != "900001@lid" || s.Phone != "+15550000001" {
		t.Fatalf("status after pairing: %+v", s)
	}
	if _, err := keys.Get(keystore.Account(dir)); err != nil {
		t.Fatalf("no key in the key store after pairing: %v", err)
	}

	// A message with the phone address of a known person.
	e.drop(t, text("M1", "15550000002@s.whatsapp.net", "hello \u202Eworld", time.Now()))
	var res api.ListMessagesResult
	eventually(t, "the message is stored", func() bool {
		res, _ = e.cl.ListMessages(ctx, api.ListMessagesInput{ChatID: "+15550000002"})
		return len(res.Messages) == 1
	})
	m := res.Messages[0]
	if m.ChatID != "900002@lid" || m.SenderID != "900002@lid" || m.MessageID != "900002@lid/M1" || m.Text != "hello world" {
		t.Fatalf("message: %+v", m)
	}
	chats, _ := e.cl.ListChats(ctx, api.ListChatsInput{Query: "ali"})
	if len(chats.Chats) != 1 || chats.Chats[0].ChatID != "900002@lid" || chats.Chats[0].Phone != "+15550000002" {
		t.Fatalf("list_chats: %+v", chats)
	}
	if _, err := e.cl.ListMessages(ctx, api.ListMessagesInput{ChatID: "123@g.us"}); api.CodeOf(err) != api.NotFound {
		t.Fatalf("unknown chat: %v", err)
	}

	// wait_for_messages: two waiters get the same message.
	var wg sync.WaitGroup
	results := make([]api.WaitResult, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = e.cl.Wait(ctx, api.WaitInput{Cursor: res.Cursor, TimeoutSeconds: 10})
		}(i)
	}
	time.Sleep(300 * time.Millisecond)
	sent := time.Now()
	e.drop(t, text("M2", "900002@lid", "second", time.Now()))
	wg.Wait()
	for i := range results {
		if errs[i] != nil || len(results[i].Messages) != 1 || results[i].Messages[0].MessageID != "900002@lid/M2" {
			t.Fatalf("waiter %d: %+v %v", i, results[i], errs[i])
		}
	}
	if d := time.Since(sent); d > 2*time.Second {
		t.Fatalf("wait returned %s after the message", d)
	}
	// The returned cursor gives nothing new, and the wait times out.
	w, err := e.cl.Wait(ctx, api.WaitInput{Cursor: results[0].Cursor, TimeoutSeconds: 1})
	if err != nil || !w.TimedOut || len(w.Messages) != 0 || w.Cursor != results[0].Cursor {
		t.Fatalf("timeout: %+v %v", w, err)
	}
	// A message between two calls comes at once with the old cursor.
	e.drop(t, text("M3", "900002@lid", "third", time.Now()))
	eventually(t, "M3 stored", func() bool {
		r, _ := e.cl.ListMessages(ctx, api.ListMessagesInput{Query: "third"})
		return len(r.Messages) == 1
	})
	w, _ = e.cl.Wait(ctx, api.WaitInput{Cursor: results[0].Cursor, TimeoutSeconds: 10})
	if len(w.Messages) != 1 || w.Messages[0].Text != "third" {
		t.Fatalf("message between two calls: %+v", w)
	}

	// Send a text and a file; download the file back.
	if _, err := e.cl.Send(ctx, api.SendMessageInput{ChatID: "900002@lid", Text: "reply", QuoteMessageID: "900002@lid/M1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	sr, err := e.cl.SendFile(ctx, "900002@lid", "notes.pdf", "see", "", bytes.NewReader([]byte("%PDF-1.4 data")))
	if err != nil {
		t.Fatalf("send file: %v", err)
	}
	body, f, err := e.cl.Download(ctx, sr.MessageID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if string(got) != "%PDF-1.4 data" || f.Name != "notes.pdf" || f.FileID != sr.MessageID {
		t.Fatalf("download: %q %+v", got, f)
	}
	mine, _ := e.cl.ListMessages(ctx, api.ListMessagesInput{ChatID: "900002@lid", Limit: 2})
	if len(mine.Messages) != 2 || !mine.Messages[0].FromMe || mine.Messages[0].File == nil {
		t.Fatalf("the sent messages are not listed: %+v", mine.Messages)
	}

	// An expired file.
	doc, _ := proto.Marshal(&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("old.pdf"), DirectPath: proto.String("/expired")}})
	e.drop(t, fakewa.Event{Chat: "900002@lid", Sender: "900002@lid", ID: "OLD", Time: time.Now().UnixMilli(), Message: doc})
	eventually(t, "OLD stored", func() bool {
		r, _ := e.cl.ListMessages(ctx, api.ListMessagesInput{Query: "old.pdf"})
		return len(r.Messages) == 1
	})
	if _, _, err := e.cl.Download(ctx, "900002@lid/OLD"); api.CodeOf(err) != api.FileExpired {
		t.Fatalf("expired file: %v", err)
	}

	// A second daemon on the same folder stops with ErrBusy.
	if err := daemon.Run(ctx, daemon.Config{DataDir: dir, Keys: keys}); !errors.Is(err, daemon.ErrBusy) {
		t.Fatalf("second daemon: %v", err)
	}

	// Restart: the pairing stays.
	e.stop()
	e = start(t, dir, keys, false)
	if s, _ := e.cl.Status(ctx); s.State != api.StateConnected {
		t.Fatalf("status after a restart: %+v", s)
	}

	// Logout deletes the database and the key.
	if _, err := e.cl.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if store.Exists(dir) {
		t.Fatal("the database exists after logout")
	}
	if _, err := keys.Get(keystore.Account(dir)); !errors.Is(err, keystore.ErrNotFound) {
		t.Fatalf("the key exists after logout: %v", err)
	}
	if s, _ := e.cl.Status(ctx); s.State != api.StateNotPaired {
		t.Fatalf("status after logout: %+v", s)
	}
}

func TestLockedWithoutKey(t *testing.T) {
	dir := t.TempDir()
	keys := keystore.NewMemory()
	e := start(t, dir, keys, false)
	e.pair(t)
	e.stop()
	_ = keys.Delete(keystore.Account(dir))
	e = start(t, dir, keys, false)
	s, _ := e.cl.Status(context.Background())
	if s.State != api.StateLocked || !strings.Contains(s.Action, "logout") {
		t.Fatalf("status without the key: %+v", s)
	}
	if _, err := e.cl.ListChats(context.Background(), api.ListChatsInput{}); api.CodeOf(err) != api.Locked {
		t.Fatalf("list_chats when locked: %v", err)
	}
}

func TestReadOnly(t *testing.T) {
	e := start(t, t.TempDir(), keystore.NewMemory(), true)
	e.pair(t)
	s, _ := e.cl.Status(context.Background())
	if !s.ReadOnly {
		t.Fatal("status does not report read_only")
	}
	if _, err := e.cl.Send(context.Background(), api.SendMessageInput{ChatID: "900002@lid", Text: "x"}); api.CodeOf(err) != api.ReadOnly {
		t.Fatalf("send in read-only mode: %v", err)
	}
	if _, err := e.cl.SendFile(context.Background(), "900002@lid", "a.txt", "", "", strings.NewReader("x")); api.CodeOf(err) != api.ReadOnly {
		t.Fatalf("send file in read-only mode: %v", err)
	}
}

func TestSocketMode(t *testing.T) {
	e := start(t, t.TempDir(), keystore.NewMemory(), false)
	p := daemon.SocketPath(e.dir)
	if b, err := os.ReadFile(daemon.SocketPathFile(e.dir)); err == nil {
		p = string(b)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket %s: %v mode %v", p, err, info.Mode().Perm())
	}
}

func TestCursor(t *testing.T) {
	for _, n := range []int64{0, 1, 123456789} {
		if got, err := daemon.ParseCursor(daemon.Cursor(n)); err != nil || got != n {
			t.Errorf("cursor %d: %d %v", n, got, err)
		}
	}
	for _, bad := range []string{"", "abc", "eDEy"} {
		if _, err := daemon.ParseCursor(bad); api.CodeOf(err) != api.InvalidArgument {
			t.Errorf("ParseCursor(%q): %v", bad, err)
		}
	}
}
