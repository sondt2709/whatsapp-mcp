//go:build live

// Package live tests the real binary on a real WhatsApp account with two
// linked devices, A and B, of the same account. A sends to the own chat
// ("Message yourself"); B receives. Nothing goes to another person.
//
// Pair both devices first, with the binary that the test uses:
//
//	whatsapp-mcp --data-dir tmp/live/A pair
//	whatsapp-mcp --data-dir tmp/live/B pair
//
// Environment: WHATSAPP_MCP_LIVE_BIN (default ../../tmp/live/whatsapp-mcp),
// WHATSAPP_MCP_LIVE_DIR (default ../../tmp/live).
package live

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

type device struct {
	t    *testing.T
	bin  string
	data string
}

func (d device) cli(args ...string) []byte {
	d.t.Helper()
	cmd := exec.Command(d.bin, append([]string{"--data-dir", d.data}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		d.t.Fatalf("%v: %v\n%s", args, err, errb.String())
	}
	return out.Bytes()
}

func (d device) status() api.Status {
	var s api.Status
	if err := json.Unmarshal(d.cli("status", "--json"), &s); err != nil {
		d.t.Fatal(err)
	}
	return s
}

func call(t *testing.T, s *mcp.ClientSession, name string, args, out any) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: %+v", name, res.Content[0])
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

// waitFor calls wait_for_messages until a message matches.
func waitFor(t *testing.T, s *mcp.ClientSession, cursor, chat string, match func(api.Message) bool) (api.Message, string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var w api.WaitResult
		call(t, s, "wait_for_messages", map[string]any{"cursor": cursor, "chat_id": chat, "timeout_seconds": 30}, &w)
		cursor = w.Cursor
		for _, m := range w.Messages {
			if match(m) {
				return m, cursor
			}
		}
	}
	t.Fatal("device B got no matching message in 2 minutes")
	return api.Message{}, ""
}

func TestLive(t *testing.T) {
	root, _ := filepath.Abs(env("WHATSAPP_MCP_LIVE_DIR", "../../tmp/live"))
	bin, _ := filepath.Abs(env("WHATSAPP_MCP_LIVE_BIN", filepath.Join(root, "whatsapp-mcp")))
	a := device{t, bin, filepath.Join(root, "A")}
	b := device{t, bin, filepath.Join(root, "B")}
	sa, sb := a.status(), b.status()
	if sa.State != api.StateConnected || sb.State != api.StateConnected {
		t.Fatalf("pair and connect both devices first: A %s, B %s", sa.State, sb.State)
	}
	if sa.UserID != sb.UserID {
		t.Fatalf("A and B are not the same account: %s, %s", sa.UserID, sb.UserID)
	}
	own := sa.UserID
	t.Logf("own chat %s", own)

	cmd := exec.Command(bin, "--data-dir", b.data, "mcp", "--download-dir", filepath.Join(root, "dl"))
	if err := os.MkdirAll(filepath.Join(root, "dl"), 0o700); err != nil {
		t.Fatal(err)
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "live", Version: "test"}, nil)
	s, err := cl.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var start api.ListMessagesResult
	call(t, s, "list_messages", map[string]any{"limit": 1}, &start)
	cursor := start.Cursor

	// 1. Text with a canary: B gets it with the same message_id as A.
	nonce := make([]byte, 6)
	_, _ = rand.Read(nonce)
	canary := "wmcp-canary-" + hex.EncodeToString(nonce)
	var sent api.SendResult
	if err := json.Unmarshal(a.cli("send", "--chat", own, "--text", "live test "+canary, "--json"), &sent); err != nil {
		t.Fatal(err)
	}
	got, cursor := waitFor(t, s, cursor, own, func(m api.Message) bool { return strings.Contains(m.Text, canary) })
	if got.MessageID != sent.MessageID || got.ChatID != own || !got.FromMe {
		t.Fatalf("B got %+v, A sent %s", got, sent.MessageID)
	}
	t.Logf("text: A sent %s, B received the same ID", sent.MessageID)

	// 2. A file: B downloads the same bytes.
	sendDir := filepath.Join(root, "send")
	if err := os.MkdirAll(sendDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 40000)
	_, _ = rand.Read(data)
	path := filepath.Join(sendDir, "live-"+hex.EncodeToString(nonce)+".bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(a.cli("send-file", "--chat", own, "--path", path, "--send-dir", sendDir, "--json"), &sent); err != nil {
		t.Fatal(err)
	}
	got, _ = waitFor(t, s, cursor, own, func(m api.Message) bool { return m.MessageID == sent.MessageID })
	if got.File == nil {
		t.Fatalf("B got no file: %+v", got)
	}
	var dl api.DownloadResult
	call(t, s, "download_file", map[string]any{"file_id": got.File.FileID}, &dl)
	sum := sha256.Sum256(data)
	if dl.SHA256 != hex.EncodeToString(sum[:]) || dl.Size != int64(len(data)) {
		t.Fatalf("download: %+v, want sha256 %x", dl, sum)
	}
	t.Logf("file: %d bytes, sha256 %s on both sides", dl.Size, dl.SHA256)
	_ = os.Remove(dl.Path)
	_ = os.Remove(path)

	// 3. A reply that quotes the canary message.
	var q api.SendResult
	if err := json.Unmarshal(a.cli("send", "--chat", own, "--text", "quote test", "--quote", got.MessageID, "--json"), &q); err != nil {
		t.Fatal(err)
	}
	t.Logf("quote: %s (check it on the phone)", q.MessageID)

	// 4. No plaintext on disk: the canary is in no file of either data folder.
	for _, dir := range []string{a.data, b.data} {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Mode()&os.ModeSocket != 0 {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil && bytes.Contains(b, []byte(canary)) {
				t.Errorf("the canary is in plaintext in %s", p)
			}
			return nil
		})
	}
}
