// Package fakewa is a fake WhatsApp backend for tests. It stores events with
// the same code as the real backend (whatsapp.Rows), so the daemon, the
// socket API, the CLI and the MCP server run unchanged. The binary uses it
// only when it is built with the build tag "fakewa".
//
// Events come from JSON lines (Event), for example the replay fixtures of the
// capture tool, and from files that a test drops in the inbox folder.
package fakewa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
	"github.com/sondt2709/whatsapp-mcp/internal/whatsapp"
)

// The account of the fake device.
var (
	OwnPN  = types.NewJID("15550000001", types.DefaultUserServer)
	OwnLID = types.NewJID("900001", types.HiddenUserServer)
)

// LIDs is the fake LID store: phone user to LID user.
type LIDs map[string]string

// GetLIDForPN implements whatsapp.LIDMap.
func (l LIDs) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if u, ok := l[pn.User]; ok {
		return types.NewJID(u, types.HiddenUserServer), nil
	}
	return types.JID{}, nil
}

// GetPNForLID implements whatsapp.LIDMap.
func (l LIDs) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	for p, u := range l {
		if u == lid.User {
			return types.NewJID(p, types.DefaultUserServer), nil
		}
	}
	return types.JID{}, nil
}

// DefaultLIDs are the mappings that the fake knows.
var DefaultLIDs = LIDs{"15550000001": "900001", "15550000002": "900002", "15550000003": "900003"}

// Event is one received message in JSON. The capture tool writes this format.
type Event struct {
	Chat         string `json:"chat"`
	Sender       string `json:"sender"`
	SenderAlt    string `json:"sender_alt,omitempty"`
	RecipientAlt string `json:"recipient_alt,omitempty"`
	FromMe       bool   `json:"from_me,omitempty"`
	ID           string `json:"id"`
	Time         int64  `json:"time"` // Unix milliseconds
	PushName     string `json:"push_name,omitempty"`
	Message      []byte `json:"message"`        // protobuf of waE2E.Message
	File         []byte `json:"file,omitempty"` // bytes for a download (fake only)
	IsEdit       bool   `json:"is_edit,omitempty"`
}

func jid(s string) types.JID {
	if s == "" {
		return types.JID{}
	}
	j, _ := types.ParseJID(s)
	return j
}

// Backend is the fake backend.
type Backend struct {
	db        *store.DB
	onMessage func()
	inbox     string
	cli       *Client
	dev       *wastore.Device

	mu        sync.Mutex
	connected bool
	loggedOut bool
	stop      chan struct{}
}

// Open makes the fake backend of a database. Paired comes from the device
// row of the database, so it stays after a restart. The inbox folder is
// polled for event files (*.json) and the control file "logged_out".
func Open(ctx context.Context, db *store.DB, inbox string, onMessage func()) (*Backend, error) {
	dev, err := db.Container.GetFirstDevice(ctx)
	if err != nil {
		return nil, err
	}
	b := &Backend{db: db, onMessage: onMessage, inbox: inbox, dev: dev, cli: NewClient(), stop: make(chan struct{})}
	if inbox != "" {
		go b.poll()
	}
	return b, nil
}

// FakeClient returns the fake whatsmeow client.
func (b *Backend) FakeClient() *Client { return b.cli }

// Paired implements daemon.Backend.
func (b *Backend) Paired() bool { return b.dev.ID != nil }

// LoggedOut implements daemon.Backend.
func (b *Backend) LoggedOut() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.loggedOut }

// Connected implements daemon.Backend.
func (b *Backend) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected && !b.loggedOut
}

// Connect implements daemon.Backend.
func (b *Backend) Connect(context.Context) error {
	if !b.Paired() {
		return api.Errorf(api.NotPaired, "not paired")
	}
	b.mu.Lock()
	b.connected = true
	b.mu.Unlock()
	return nil
}

// Ready implements daemon.Backend.
func (b *Backend) Ready() error {
	switch {
	case !b.Paired():
		return api.Errorf(api.NotPaired, "run \"whatsapp-mcp pair\"")
	case b.LoggedOut():
		return api.Errorf(api.LoggedOut, "the phone removed this device")
	case !b.Connected():
		return api.Errorf(api.NotConnected, "not connected")
	}
	return nil
}

// Close implements daemon.Backend.
func (b *Backend) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
	b.connected = false
}

// Me implements daemon.Backend.
func (b *Backend) Me() whatsapp.Me {
	if !b.Paired() {
		return whatsapp.Me{}
	}
	return whatsapp.Me{PN: OwnPN, LID: OwnLID}
}

// PushName implements daemon.Backend.
func (b *Backend) PushName() string { return "Test Account" }

// Rows implements daemon.Backend.
func (b *Backend) Rows() whatsapp.Rows {
	return whatsapp.Rows{Res: whatsapp.Resolver{LIDs: DefaultLIDs}, Me: b.Me()}
}

// Client implements daemon.Backend.
func (b *Backend) Client() whatsapp.Client { return b.cli }

// Pair implements daemon.Backend: one QR code, then a paired device.
func (b *Backend) Pair(ctx context.Context, _ string, _ time.Duration, show func(whatsapp.PairEvent)) error {
	show(whatsapp.PairEvent{QR: "2@fake-qr-code"})
	dev := b.db.Container.NewDevice()
	id := types.NewADJID(OwnPN.User, 0, 7)
	dev.ID = &id
	dev.LID = types.NewADJID(OwnLID.User, 0, 7)
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64),
		AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := b.db.Container.PutDevice(ctx, dev); err != nil {
		return err
	}
	b.dev = dev
	b.mu.Lock()
	b.connected = true
	b.mu.Unlock()
	if seed := os.Getenv("WHATSAPP_MCP_FAKE_SEED"); seed != "" {
		if err := b.IngestFile(ctx, seed); err != nil {
			return err
		}
	}
	return nil
}

// Logout implements daemon.Backend.
func (b *Backend) Logout(ctx context.Context) error {
	if b.dev.ID == nil {
		return nil
	}
	return b.db.Container.DeleteDevice(ctx, b.dev)
}

// Ingest stores one event like the real backend does.
func (b *Backend) Ingest(ctx context.Context, e Event) error {
	m := &waE2E.Message{}
	if err := proto.Unmarshal(e.Message, m); err != nil {
		return err
	}
	if e.File != nil {
		if media, ok := whatsapp.MediaOf(m); ok {
			b.cli.mu.Lock()
			b.cli.files[media.Part.GetDirectPath()] = e.File
			b.cli.mu.Unlock()
		}
	}
	v := &events.Message{Info: types.MessageInfo{
		MessageSource: types.MessageSource{Chat: jid(e.Chat), Sender: jid(e.Sender), SenderAlt: jid(e.SenderAlt),
			RecipientAlt: jid(e.RecipientAlt), IsFromMe: e.FromMe},
		ID: e.ID, Timestamp: time.UnixMilli(e.Time), PushName: e.PushName,
	}, Message: m, IsEdit: e.IsEdit}
	rows := b.Rows()
	r, ok := rows.FromEvent(ctx, v)
	if !ok {
		return nil
	}
	added, err := b.db.SaveMessage(ctx, r)
	if err != nil {
		return err
	}
	if err := b.db.SaveChat(ctx, rows.ChatOf(ctx, v, r)); err != nil {
		return err
	}
	if added && b.onMessage != nil {
		b.onMessage()
	}
	return nil
}

// IngestFile stores each event of a JSON-lines file.
func (b *Backend) IngestFile(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return err
		}
		if err := b.Ingest(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// poll ingests the event files that a test drops in the inbox folder.
func (b *Backend) poll() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
		}
		if _, err := os.Stat(filepath.Join(b.inbox, "logged_out")); err == nil {
			b.mu.Lock()
			b.loggedOut = true
			b.mu.Unlock()
		}
		names, _ := filepath.Glob(filepath.Join(b.inbox, "*.json"))
		sort.Strings(names)
		for _, p := range names {
			if b.Paired() {
				_ = b.IngestFile(context.Background(), p)
			}
			_ = os.Remove(p)
		}
	}
}

// Client is a fake whatsmeow client. A phone number that starts with 1555 is
// on WhatsApp. A download of the direct path "/expired" gives HTTP 410.
type Client struct {
	mu    sync.Mutex
	files map[string][]byte
	sent  []Sent
	ids   map[types.MessageID]bool
}

// Sent is one message that the fake client sent.
type Sent struct {
	To      types.JID
	ID      types.MessageID
	Message *waE2E.Message
}

// NewClient returns an empty fake client.
func NewClient() *Client {
	return &Client{files: map[string][]byte{}, ids: map[types.MessageID]bool{}}
}

// Sent returns the sent messages. A second send with the same ID is not
// added, like WhatsApp shows one message.
func (c *Client) Sent() []Sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Sent(nil), c.sent...)
}

// SendMessage implements whatsapp.Client.
func (c *Client) SendMessage(_ context.Context, to types.JID, m *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var id types.MessageID
	if len(extra) > 0 {
		id = extra[0].ID
	}
	if id == "" {
		sum := sha256.Sum256([]byte(time.Now().String()))
		id = types.MessageID("3EB0" + strings.ToUpper(hex.EncodeToString(sum[:8])))
	}
	if !c.ids[id] {
		c.ids[id] = true
		c.sent = append(c.sent, Sent{To: to, ID: id, Message: m})
	}
	return whatsmeow.SendResponse{ID: id, Timestamp: time.Now()}, nil
}

// Upload implements whatsapp.Client.
func (c *Client) Upload(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	sum := sha256.Sum256(data)
	path := "/fake/" + hex.EncodeToString(sum[:8])
	c.mu.Lock()
	c.files[path] = append([]byte(nil), data...)
	c.mu.Unlock()
	return whatsmeow.UploadResponse{URL: "https://fake.invalid" + path, DirectPath: path, MediaKey: make([]byte, 32),
		FileSHA256: sum[:], FileEncSHA256: sum[:], FileLength: uint64(len(data))}, nil
}

// Download implements whatsapp.Client.
func (c *Client) Download(_ context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error) {
	path := msg.GetDirectPath()
	if path == "/expired" {
		return nil, whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: 410}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.files[path]
	if !ok {
		return nil, whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: 404}}
	}
	return append([]byte(nil), data...), nil
}

// IsOnWhatsApp implements whatsapp.Client.
func (c *Client) IsOnWhatsApp(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	var out []types.IsOnWhatsAppResponse
	for _, p := range phones {
		digits := strings.TrimPrefix(p, "+")
		out = append(out, types.IsOnWhatsAppResponse{Query: p, JID: types.NewJID(digits, types.DefaultUserServer),
			IsIn: strings.HasPrefix(digits, "1555")})
	}
	return out, nil
}

// Factory returns a function with the signature of daemon.NewBackend, without
// an import of the daemon package. inbox is "<data>/fake-inbox" when empty.
func Factory(inbox string) func(ctx context.Context, db *store.DB, _ time.Duration, _ *slog.Logger, onMessage func()) (*Backend, error) {
	return func(ctx context.Context, db *store.DB, _ time.Duration, _ *slog.Logger, onMessage func()) (*Backend, error) {
		return Open(ctx, db, inbox, onMessage)
	}
}
