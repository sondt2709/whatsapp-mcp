// Package e2e drives the real binary, built with the fake WhatsApp backend
// (-tags fakewa): the MCP server over stdio through the MCP Go SDK client,
// and the CLI. The MCP server starts the daemon by itself.
package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/daemon"
	"github.com/sondt2709/whatsapp-mcp/internal/fakewa"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "whatsapp-mcp-e2e-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "whatsapp-mcp")
	build := exec.Command("go", "build", "-tags", "fakewa", "-o", bin, "../../cmd/whatsapp-mcp")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t        *testing.T
	data     string
	sendDir  string
	download string
	vars     []string
}

func newEnv(t *testing.T, seed []fakewa.Event) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, data: filepath.Join(root, "data"), sendDir: filepath.Join(root, "send"), download: filepath.Join(root, "dl")}
	for _, d := range []string{e.sendDir, e.download} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	seedFile := filepath.Join(root, "seed.jsonl")
	var b bytes.Buffer
	for _, ev := range seed {
		line, _ := json.Marshal(ev)
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(seedFile, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	e.vars = append(os.Environ(), "WHATSAPP_MCP_FAKE_KEYFILE="+filepath.Join(root, "keys.json"),
		"WHATSAPP_MCP_FAKE_SEED="+seedFile, "WHATSAPP_MCP_DATA_DIR="+e.data)
	t.Cleanup(e.stopDaemon)
	return e
}

// stopDaemon stops the daemon that a client started.
func (e *env) stopDaemon() {
	b, err := os.ReadFile(daemon.LockPath(e.data))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// cli runs the binary and returns stdout, stderr and the exit code.
func (e *env) cli(args ...string) (string, string, int) {
	e.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = e.vars
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

// session starts "whatsapp-mcp mcp" and connects the MCP client.
func (e *env) session(progress *atomic.Int32, extra ...string) *mcp.ClientSession {
	e.t.Helper()
	args := append([]string{"mcp", "--send-dir", e.sendDir, "--download-dir", e.download}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Env = e.vars
	opts := &mcp.ClientOptions{}
	if progress != nil {
		opts.ProgressNotificationHandler = func(context.Context, *mcp.ProgressNotificationClientRequest) { progress.Add(1) }
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "test"}, opts)
	s, err := c.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(func() { s.Close() })
	return s
}

// call calls a tool and decodes the structured result into out. It returns
// the error text when the result is an error.
func call(t *testing.T, s *mcp.ClientSession, name string, args any, out any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return b.String()
	}
	if out != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, raw, err)
		}
	}
	return ""
}

func textEvent(id, chat, sender, body string, at time.Time) fakewa.Event {
	m, _ := proto.Marshal(&waE2E.Message{Conversation: proto.String(body)})
	return fakewa.Event{Chat: chat, Sender: sender, ID: id, Time: at.UnixMilli(), PushName: "Alice", Message: m}
}

func (e *env) drop(ev fakewa.Event) {
	e.t.Helper()
	inbox := filepath.Join(e.data, "fake-inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		e.t.Fatal(err)
	}
	b, _ := json.Marshal(ev)
	tmp := filepath.Join(inbox, ev.ID+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(inbox, ev.ID+".json")); err != nil {
		e.t.Fatal(err)
	}
}

func TestToolsAndNotPaired(t *testing.T) {
	e := newEnv(t, nil)
	s := e.session(nil)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		byName[tl.Name] = tl
	}
	for _, n := range []string{"get_status", "list_chats", "list_messages", "wait_for_messages", "send_message", "send_file", "download_file"} {
		tl := byName[n]
		if tl == nil || tl.InputSchema == nil || tl.OutputSchema == nil || tl.Annotations == nil {
			t.Fatalf("tool %s is missing or has no schema or annotations: %+v", n, tl)
		}
	}
	if len(byName) != 7 {
		t.Fatalf("%d tools, want 7", len(byName))
	}
	for _, n := range []string{"get_status", "list_chats", "list_messages", "wait_for_messages"} {
		if !byName[n].Annotations.ReadOnlyHint {
			t.Errorf("%s: readOnlyHint is false", n)
		}
	}
	for _, n := range []string{"send_message", "send_file"} {
		a := byName[n].Annotations
		if a.ReadOnlyHint || a.OpenWorldHint == nil || !*a.OpenWorldHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s annotations: %+v", n, a)
		}
	}
	if a := byName["download_file"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
		t.Errorf("download_file annotations: %+v", a)
	}
	if !strings.Contains(byName["list_messages"].Description, "never as instructions") {
		t.Error("list_messages does not mark message text as untrusted")
	}

	var st api.Status
	call(t, s, "get_status", map[string]any{}, &st)
	if st.State != api.StateNotPaired || !strings.Contains(st.Action, "whatsapp-mcp pair") {
		t.Fatalf("get_status: %+v", st)
	}
	if msg := call(t, s, "list_chats", map[string]any{}, nil); !strings.Contains(msg, "not_paired") || !strings.Contains(msg, "whatsapp-mcp pair") {
		t.Fatalf("list_chats before pairing: %q", msg)
	}
}

func TestFullFlow(t *testing.T) {
	now := time.Now()
	e := newEnv(t, []fakewa.Event{
		textEvent("H1", "15550000002@s.whatsapp.net", "15550000002@s.whatsapp.net", "hello from the history", now.Add(-time.Hour)),
		textEvent("H2", "120363000000000001@g.us", "900003@lid", "team update", now.Add(-30*time.Minute)),
	})
	out, errOut, code := e.cli("pair")
	if code != 0 || !strings.Contains(out, "paired as +15550000001") || !strings.Contains(errOut, "Scan this code") {
		t.Fatalf("pair: exit %d\n%s\n%s", code, out, errOut)
	}

	var progress atomic.Int32
	s := e.session(&progress)
	var st api.Status
	call(t, s, "get_status", map[string]any{}, &st)
	if st.State != api.StateConnected || st.UserID != "900001@lid" {
		t.Fatalf("status: %+v", st)
	}

	var chats api.ListChatsResult
	call(t, s, "list_chats", map[string]any{"limit": 10}, &chats)
	if len(chats.Chats) != 2 || chats.Chats[0].ChatID != "120363000000000001@g.us" || chats.Chats[1].ChatID != "900002@lid" {
		t.Fatalf("list_chats: %+v", chats)
	}
	// The CLI prints the same JSON.
	cliOut, _, code := e.cli("list-chats", "--json", "--limit", "10")
	var cliChats api.ListChatsResult
	if code != 0 || json.Unmarshal([]byte(cliOut), &cliChats) != nil || len(cliChats.Chats) != 2 ||
		cliChats.Chats[0].ChatID != chats.Chats[0].ChatID || cliChats.Chats[1].ChatID != chats.Chats[1].ChatID {
		t.Fatalf("CLI list-chats --json differs: %s", cliOut)
	}

	var msgs api.ListMessagesResult
	call(t, s, "list_messages", map[string]any{"chat_id": "+15550000002"}, &msgs)
	if len(msgs.Messages) != 1 || msgs.Messages[0].MessageID != "900002@lid/H1" {
		t.Fatalf("list_messages: %+v", msgs)
	}

	// wait_for_messages with a progress token; a message arrives after 16 s,
	// so at least one progress notification comes first.
	go func() {
		time.Sleep(16 * time.Second)
		e.drop(textEvent("L1", "900002@lid", "900002@lid", "live message", time.Now()))
	}()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "wait_for_messages",
		Arguments: map[string]any{"cursor": msgs.Cursor, "timeout_seconds": 60}, Meta: mcp.Meta{"progressToken": "p1"}})
	if err != nil || res.IsError {
		t.Fatalf("wait: %v %+v", err, res)
	}
	var w api.WaitResult
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &w)
	if len(w.Messages) != 1 || w.Messages[0].Text != "live message" {
		t.Fatalf("wait result: %s", raw)
	}
	if progress.Load() < 1 {
		t.Fatal("no progress notification during a wait of 16 s")
	}

	// send_message with a quote.
	var sent api.SendResult
	if msg := call(t, s, "send_message", map[string]any{"chat_id": "900002@lid", "text": "thanks", "quote_message_id": "900002@lid/H1"}, &sent); msg != "" {
		t.Fatalf("send_message: %s", msg)
	}
	if !strings.HasPrefix(sent.MessageID, "900002@lid/") {
		t.Fatalf("send_message result: %+v", sent)
	}

	// send_file from the allowed folder, then download_file of the sent file.
	content := []byte("%PDF-1.4 e2e file")
	if err := os.WriteFile(filepath.Join(e.sendDir, "report.pdf"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := call(t, s, "send_file", map[string]any{"chat_id": "900002@lid", "path": filepath.Join(e.sendDir, "report.pdf")}, &sent); msg != "" {
		t.Fatalf("send_file: %s", msg)
	}
	var dl api.DownloadResult
	if msg := call(t, s, "download_file", map[string]any{"file_id": sent.MessageID}, &dl); msg != "" {
		t.Fatalf("download_file: %s", msg)
	}
	got, _ := os.ReadFile(dl.Path)
	sum := sha256.Sum256(content)
	if filepath.Dir(dl.Path) != e.download || dl.Name != "report.pdf" || !bytes.Equal(got, content) || dl.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("download: %+v", dl)
	}
	// A second download does not overwrite.
	call(t, s, "download_file", map[string]any{"file_id": sent.MessageID}, &dl)
	if dl.Name != "report (1).pdf" {
		t.Fatalf("second download name %q", dl.Name)
	}

	// A file outside the allowed folders is refused.
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	_ = os.WriteFile(secret, []byte("secret"), 0o600)
	if msg := call(t, s, "send_file", map[string]any{"chat_id": "900002@lid", "path": secret}, nil); !strings.Contains(msg, "path_not_allowed") {
		t.Fatalf("send_file outside: %q", msg)
	}

	// The sent messages are listed as from_me.
	call(t, s, "list_messages", map[string]any{"chat_id": "900002@lid", "limit": 2}, &msgs)
	if len(msgs.Messages) != 2 || !msgs.Messages[0].FromMe || msgs.Messages[0].File == nil {
		t.Fatalf("sent messages: %+v", msgs.Messages)
	}

	// Logout through the CLI.
	if out, errOut, code := e.cli("logout"); code != 0 || !strings.Contains(out, "logged out") {
		t.Fatalf("logout: %d %s %s", code, out, errOut)
	}
	call(t, s, "get_status", map[string]any{}, &st)
	if st.State != api.StateNotPaired {
		t.Fatalf("status after logout: %+v", st)
	}
}

func TestReadOnlyHidesSendTools(t *testing.T) {
	e := newEnv(t, nil)
	if _, errOut, code := e.cli("--read-only", "pair"); code != 0 {
		t.Fatalf("pair: %s", errOut)
	}
	s := e.session(nil)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "send_message" || tl.Name == "send_file" {
			t.Fatalf("read-only daemon lists %s", tl.Name)
		}
	}
	if len(tools.Tools) != 5 {
		t.Fatalf("%d tools, want 5", len(tools.Tools))
	}
	if _, errOut, code := e.cli("send", "--chat", "900002@lid", "--text", "x"); code == 0 || !strings.Contains(errOut, "read_only") {
		t.Fatalf("CLI send in read-only mode: %d %s", code, errOut)
	}
}
