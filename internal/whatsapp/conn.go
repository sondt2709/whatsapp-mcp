// Package whatsapp is the WhatsApp side of whatsapp-mcp: the linked-device
// connection (whatsmeow), pairing, the conversion of events to stored rows,
// sending and downloading. It never sends a read receipt or presence.
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

// DeviceName is the name of the linked device on the phone.
const DeviceName = "whatsapp-mcp"

func init() {
	wastore.SetOSInfo(DeviceName, [3]uint32{1, 0, 0})
}

// connectWait limits the wait for the login before a send or a download.
const connectWait = 30 * time.Second

// Conn is the WhatsApp client of one data folder. Only the daemon holds it.
type Conn struct {
	db        *store.DB
	cli       *whatsmeow.Client
	log       *slog.Logger
	onMessage func() // called after each new stored message
	retention time.Duration
	now       func() time.Time

	loggedOut atomic.Bool

	mu     sync.Mutex
	groups map[string]bool // groups with a name in the chat index
}

// Open makes the client of the first device in the database. It does not connect.
func Open(ctx context.Context, db *store.DB, retention time.Duration, log *slog.Logger, onMessage func()) (*Conn, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	dev, err := db.Container.GetFirstDevice(ctx)
	if err != nil {
		return nil, err
	}
	c := &Conn{db: db, log: log, onMessage: onMessage, retention: retention, now: time.Now, groups: map[string]bool{}}
	c.cli = whatsmeow.NewClient(dev, Logger(log, "client"))
	c.cli.AddEventHandler(c.handle)
	return c, nil
}

// Client returns the whatsmeow client for the send and download functions.
func (c *Conn) Client() *whatsmeow.Client { return c.cli }

// Paired reports whether the device has keys.
func (c *Conn) Paired() bool { return c.cli.Store.ID != nil }

// LoggedOut reports whether the phone unlinked the device.
func (c *Conn) LoggedOut() bool { return c.loggedOut.Load() }

// Connected reports whether the client is connected and logged in.
func (c *Conn) Connected() bool { return c.cli.IsConnected() && c.cli.IsLoggedIn() }

// Connect connects a paired client. whatsmeow reconnects after a network
// error. The connection does not use the cancel of ctx, so it stays open
// until Close.
func (c *Conn) Connect(ctx context.Context) error {
	if !c.Paired() {
		return api.Errorf(api.NotPaired, "run \"whatsapp-mcp pair\" in a terminal")
	}
	if c.cli.IsConnected() {
		return nil
	}
	return c.cli.ConnectContext(context.WithoutCancel(ctx))
}

// Ready waits for the login and returns an error with a code when the client
// cannot send or download.
func (c *Conn) Ready() error {
	switch {
	case !c.Paired():
		return api.Errorf(api.NotPaired, "run \"whatsapp-mcp pair\" in a terminal")
	case c.LoggedOut():
		return api.Errorf(api.LoggedOut, "the phone removed this device: run \"whatsapp-mcp logout\", then \"whatsapp-mcp pair\"")
	case !c.cli.WaitForConnection(connectWait):
		return api.Errorf(api.NotConnected, "no connection to WhatsApp after %s: check the network and call again", connectWait)
	}
	return nil
}

// Close disconnects.
func (c *Conn) Close() { c.cli.Disconnect() }

// Me returns the addresses of the account.
func (c *Conn) Me() Me {
	var me Me
	if id := c.cli.Store.ID; id != nil {
		me.PN = id.ToNonAD()
	}
	if !c.cli.Store.LID.IsEmpty() {
		me.LID = c.cli.Store.LID.ToNonAD()
	}
	return me
}

// PushName returns the profile name of the account.
func (c *Conn) PushName() string { return c.cli.Store.PushName }

// Resolver returns the ID resolver of the account.
func (c *Conn) Resolver() Resolver { return Resolver{LIDs: c.cli.Store.LIDs} }

// Rows returns the row maker of the account.
func (c *Conn) Rows() Rows { return Rows{Res: c.Resolver(), Me: c.Me(), Names: c} }

// Name returns the contact name of an address, or "".
func (c *Conn) Name(ctx context.Context, j types.JID) string {
	ci, err := c.cli.Store.Contacts.GetContact(ctx, j)
	if err != nil || !ci.Found {
		return ""
	}
	for _, n := range []string{ci.FullName, ci.FirstName, ci.BusinessName} {
		if n != "" {
			return n
		}
	}
	return ""
}

func (c *Conn) handle(evt any) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch v := evt.(type) {
	case *events.Message:
		c.save(ctx, v)
	case *events.HistorySync:
		c.saveHistory(ctx, v)
	case *events.Connected:
		c.loggedOut.Store(false)
		go c.readChats()
	case *events.LoggedOut:
		c.loggedOut.Store(true)
		c.log.Error("whatsapp: the phone removed this device", "reason", v.Reason.String())
	case *events.StreamReplaced:
		c.log.Error("whatsapp: another client connected with the same device keys, so this connection closed")
	case *events.TemporaryBan:
		c.log.Error("whatsapp: temporary ban", "ban", v.String())
	}
}

func (c *Conn) save(ctx context.Context, v *events.Message) {
	rows := c.Rows()
	r, ok := rows.FromEvent(ctx, v)
	if !ok || c.retention > 0 && r.Time.Before(c.now().Add(-c.retention)) {
		return
	}
	added, err := c.db.SaveMessage(ctx, r)
	if err != nil {
		c.log.Error("whatsapp: cannot store a message", "err", err)
		return
	}
	if err := c.db.SaveChat(ctx, rows.ChatOf(ctx, v, r)); err != nil {
		c.log.Error("whatsapp: cannot store a chat", "err", err)
	}
	if v.Info.Chat.Server == types.GroupServer {
		c.mu.Lock()
		known := c.groups[r.Chat]
		c.groups[r.Chat] = true
		c.mu.Unlock()
		if !known {
			// A network call in an event handler runs in a goroutine:
			// whatsmeow runs the handlers one at a time.
			go c.readGroup(v.Info.Chat)
		}
	}
	if added && c.onMessage != nil {
		c.onMessage()
	}
}

func (c *Conn) saveHistory(ctx context.Context, v *events.HistorySync) {
	for _, conv := range v.Data.GetConversations() {
		jid, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		kind, ok := ChatKind(jid)
		if !ok {
			continue
		}
		if kind == api.KindGroup {
			ch := store.Chat{ID: jid.ToNonAD().String(), Kind: kind, Name: conv.GetName()}
			if ts := conv.GetConversationTimestamp(); ts > 0 {
				ch.Last = time.Unix(int64(ts), 0)
			}
			_ = c.db.SaveChat(ctx, ch)
		}
		for _, hm := range conv.GetMessages() {
			if evt, err := c.cli.ParseWebMessage(jid, hm.GetMessage()); err == nil {
				c.save(ctx, evt)
			}
		}
	}
}

// readChats adds the groups and the named contacts to the chat index.
func (c *Conn) readChats() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	groups, err := c.cli.GetJoinedGroups(ctx)
	if err != nil {
		c.log.Warn("whatsapp: cannot read the groups", "err", err)
	}
	for _, g := range groups {
		if err := c.db.SaveChat(ctx, store.Chat{ID: g.JID.ToNonAD().String(), Name: g.Name, Kind: api.KindGroup}); err == nil {
			c.mu.Lock()
			c.groups[g.JID.ToNonAD().String()] = true
			c.mu.Unlock()
		}
	}
	contacts, err := c.cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		c.log.Warn("whatsapp: cannot read the contacts", "err", err)
	}
	res := c.Resolver()
	me := c.Me()
	for jid, ci := range contacts {
		name := ci.FullName
		if name == "" {
			name = ci.FirstName
		}
		if name == "" {
			name = ci.BusinessName
		}
		if name == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) || me.Is(jid) {
			continue
		}
		id := res.User(ctx, jid, types.JID{})
		_ = c.db.SaveChat(ctx, store.Chat{ID: id.String(), Name: name, Kind: api.KindDirect, Phone: res.Phone(ctx, jid, types.JID{})})
	}
}

func (c *Conn) readGroup(jid types.JID) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, err := c.cli.GetGroupInfo(ctx, jid)
	if err != nil {
		c.mu.Lock()
		delete(c.groups, jid.ToNonAD().String())
		c.mu.Unlock()
		return
	}
	_ = c.db.SaveChat(ctx, store.Chat{ID: jid.ToNonAD().String(), Name: g.Name, Kind: api.KindGroup})
}

// PairEvent is one step of the pairing.
type PairEvent struct {
	QR   string // a new QR code to show
	Code string // the pairing code to type on the phone
}

// ErrPairTimeout is returned when nobody scans the code before WhatsApp stops
// giving codes (after about 160 seconds).
var ErrPairTimeout = errors.New("pairing timed out")

// Pair links a new device and gives the codes to show. With a phone number,
// it gives one pairing code; else a QR code each time WhatsApp sends one.
// After the scan it waits up to wait for the history sync.
func (c *Conn) Pair(ctx context.Context, phone string, wait time.Duration, show func(PairEvent)) error {
	if c.Paired() {
		return errors.New("this data folder is paired already: run \"whatsapp-mcp logout\" first")
	}
	history := make(chan struct{})
	var once sync.Once
	id := c.cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.HistorySync); ok {
			once.Do(func() { close(history) })
		}
	})
	defer c.cli.RemoveEventHandler(id)
	qrc, err := c.cli.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := c.cli.ConnectContext(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	phone = strings.TrimPrefix(strings.TrimSpace(phone), "+")
	asked := false
	for item := range qrc {
		switch {
		case item.Event == whatsmeow.QRChannelEventCode:
			if phone == "" {
				show(PairEvent{QR: item.Code})
				continue
			}
			if !asked {
				asked = true
				code, err := c.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Mac OS)")
				if err != nil {
					c.cli.Disconnect()
					return fmt.Errorf("pairing code: %w", err)
				}
				show(PairEvent{Code: code})
			}
		case item == whatsmeow.QRChannelSuccess:
			select {
			case <-history:
				// Give the next history parts some seconds.
				time.Sleep(5 * time.Second)
			case <-time.After(wait):
			case <-ctx.Done():
			}
			return nil
		case item == whatsmeow.QRChannelTimeout:
			return ErrPairTimeout
		case item.Error != nil:
			return item.Error
		default:
			return fmt.Errorf("pairing failed: %s", item.Event)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("pairing stopped")
}

// Logout unlinks the device on the phone. It connects when necessary.
func (c *Conn) Logout(ctx context.Context) error {
	if !c.Paired() {
		return nil
	}
	if !c.cli.IsConnected() {
		if err := c.cli.ConnectContext(ctx); err != nil {
			return err
		}
	}
	return c.cli.Logout(ctx)
}

// Logger passes the warnings and errors of whatsmeow to slog. The info and
// debug lines are dropped: they can hold message content.
func Logger(log *slog.Logger, module string) waLog.Logger {
	return slogLogger{log: log, module: module}
}

type slogLogger struct {
	log    *slog.Logger
	module string
}

// noise are the starts of whatsmeow warnings that are not problems.
var noise = []string{"Error sending close to websocket"}

func (l slogLogger) Warnf(msg string, args ...any) {
	text := fmt.Sprintf(msg, args...)
	for _, n := range noise {
		if strings.HasPrefix(text, n) {
			return
		}
	}
	l.log.Warn("whatsmeow: "+text, "module", l.module)
}

func (l slogLogger) Errorf(msg string, args ...any) {
	l.log.Error("whatsmeow: "+fmt.Sprintf(msg, args...), "module", l.module)
}
func (l slogLogger) Infof(string, ...any)  {}
func (l slogLogger) Debugf(string, ...any) {}
func (l slogLogger) Sub(module string) waLog.Logger {
	if l.module != "" {
		module = l.module + "/" + module
	}
	return slogLogger{log: l.log, module: module}
}
