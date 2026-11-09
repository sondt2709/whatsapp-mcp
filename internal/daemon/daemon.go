// Package daemon is the process that holds the WhatsApp connection of a data
// folder. It keeps the encrypted database open, stores the messages, wakes
// the waiters and serves the JSON API on a Unix socket. The CLI and the MCP
// server are its clients.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/keystore"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
	"github.com/sondt2709/whatsapp-mcp/internal/whatsapp"
)

// Backend is the WhatsApp side of the daemon. whatsapp.Conn implements it;
// tests use a fake.
type Backend interface {
	Paired() bool
	LoggedOut() bool
	Connected() bool
	Connect(ctx context.Context) error
	Ready() error
	Close()
	Me() whatsapp.Me
	PushName() string
	Rows() whatsapp.Rows
	Client() whatsapp.Client
	Pair(ctx context.Context, phone string, wait time.Duration, show func(whatsapp.PairEvent)) error
	Logout(ctx context.Context) error
}

// NewBackend makes the backend of an open database.
type NewBackend func(ctx context.Context, db *store.DB, retention time.Duration, log *slog.Logger, onMessage func()) (Backend, error)

// conn adapts whatsapp.Conn to Backend.
type conn struct{ *whatsapp.Conn }

func (c conn) Client() whatsapp.Client { return c.Conn.Client() }

// WhatsAppBackend is the real backend.
func WhatsAppBackend(ctx context.Context, db *store.DB, retention time.Duration, log *slog.Logger, onMessage func()) (Backend, error) {
	c, err := whatsapp.Open(ctx, db, retention, log, onMessage)
	if err != nil {
		return nil, err
	}
	return conn{c}, nil
}

// Config is the configuration of a daemon.
type Config struct {
	DataDir   string
	Retention time.Duration
	ReadOnly  bool
	Version   string
	Keys      keystore.Store
	Backend   NewBackend
	Log       *slog.Logger
}

// Daemon is the state of a running daemon.
type Daemon struct {
	cfg Config
	hub *Hub
	log *slog.Logger

	mu         sync.Mutex
	db         *store.DB
	be         Backend
	locked     string // the action of the locked state; "" when not locked
	retryKey   bool   // the key store refused once; try again later
	lastKeyTry time.Time
	pairing    bool
}

// New returns a daemon. It does not open anything.
func New(cfg Config) *Daemon {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Backend == nil {
		cfg.Backend = WhatsAppBackend
	}
	if cfg.Keys == nil {
		cfg.Keys = keystore.System()
	}
	return &Daemon{cfg: cfg, hub: NewHub(), log: cfg.Log}
}

func (d *Daemon) account() string { return keystore.Account(d.cfg.DataDir) }

// Run holds the lock, opens the database when it can, connects, and serves
// the socket API until ctx ends.
func Run(ctx context.Context, cfg Config) error {
	syscall.Umask(0o077)
	noCoreDumps()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	unlock, err := lock(cfg.DataDir)
	if err != nil {
		return err
	}
	defer unlock()
	d := New(cfg)
	d.start(ctx)
	defer d.stop()

	ln, path, err := listen(cfg.DataDir)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go d.background(ctx)
	d.log.Info("daemon: serving", "socket", path)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// start opens the database with the stored key, when the folder has one.
func (d *Daemon) start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !store.Exists(d.cfg.DataDir) {
		return
	}
	if keystore.HasPassphrase(d.cfg.DataDir) {
		d.locked = "the database key comes from a passphrase: ask the user to run \"whatsapp-mcp unlock\" in a terminal"
		return
	}
	d.openWithStoredKey(ctx)
}

// openWithStoredKey reads the key from the key store and opens the database.
// The caller holds d.mu.
func (d *Daemon) openWithStoredKey(ctx context.Context) {
	d.lastKeyTry = time.Now()
	key, err := d.cfg.Keys.Get(d.account())
	switch {
	case errors.Is(err, keystore.ErrNotFound):
		d.locked = fmt.Sprintf("the %s has no key for this data folder, so the database cannot be read: ask the user to run \"whatsapp-mcp logout\", then \"whatsapp-mcp pair\"", d.cfg.Keys.Name())
		return
	case errors.Is(err, keystore.ErrDenied):
		d.locked = fmt.Sprintf("the %s refused the access: ask the user to allow whatsapp-mcp in the dialog on the screen, then call again", d.cfg.Keys.Name())
		d.retryKey = true
		return
	case err != nil:
		d.locked = fmt.Sprintf("cannot read the key from the %s (%v): ask the user to unlock the key store, then call again", d.cfg.Keys.Name(), err)
		d.retryKey = true
		return
	}
	if err := d.open(ctx, key); err != nil {
		d.log.Error("daemon: cannot open the database", "err", err)
	}
}

// open opens the database and the backend and starts the connection. The
// caller holds d.mu.
func (d *Daemon) open(ctx context.Context, key []byte) error {
	db, err := store.Open(ctx, d.cfg.DataDir, key, whatsapp.Logger(d.log, "store"))
	if errors.Is(err, store.ErrWrongKey) {
		d.locked = "the key does not open the database: ask the user to run \"whatsapp-mcp logout\", then \"whatsapp-mcp pair\""
		return err
	}
	if err != nil {
		d.locked = fmt.Sprintf("cannot open the database: %v", err)
		return err
	}
	be, err := d.cfg.Backend(ctx, db, d.cfg.Retention, d.log, d.hub.Notify)
	if err != nil {
		db.Close()
		d.locked = fmt.Sprintf("cannot start the WhatsApp client: %v", err)
		return err
	}
	d.db, d.be, d.locked, d.retryKey = db, be, "", false
	if be.Paired() {
		if err := be.Connect(ctx); err != nil {
			d.log.Warn("daemon: connect failed; trying again later", "err", err)
		}
	}
	return nil
}

func (d *Daemon) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.be != nil {
		d.be.Close()
	}
	if d.db != nil {
		d.db.Close()
	}
	d.be, d.db = nil, nil
}

// background connects again after a failure, tries the key store again,
// and deletes old messages each hour.
func (d *Daemon) background(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	lastPrune := time.Time{}
	for {
		d.mu.Lock()
		if d.be != nil && d.be.Paired() && !d.be.LoggedOut() && !d.be.Connected() && !d.pairing {
			_ = d.be.Connect(ctx)
		}
		if d.db != nil && d.cfg.Retention > 0 && time.Since(lastPrune) > time.Hour {
			lastPrune = time.Now()
			if n, err := d.db.Prune(ctx, time.Now().Add(-d.cfg.Retention)); err != nil {
				d.log.Error("daemon: retention", "err", err)
			} else if n > 0 {
				d.log.Info("daemon: deleted old messages", "count", n)
			}
		}
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Status returns the account state.
func (d *Daemon) Status(ctx context.Context) api.Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	// After a refused key store access, try again at most each 30 seconds,
	// so that the user can allow the dialog and call again.
	if d.retryKey && d.db == nil && time.Since(d.lastKeyTry) > 30*time.Second {
		d.openWithStoredKey(ctx)
	}
	s := api.Status{ReadOnly: d.cfg.ReadOnly, Version: d.cfg.Version}
	switch {
	case d.locked != "":
		s.State, s.Action = api.StateLocked, d.locked
	case d.be == nil || !d.be.Paired():
		s.State, s.Action = api.StateNotPaired, "ask the user to run \"whatsapp-mcp pair\" in a terminal"
	case d.be.LoggedOut():
		s.State, s.Action = api.StateLoggedOut, "the phone removed this device: ask the user to run \"whatsapp-mcp logout\", then \"whatsapp-mcp pair\""
	case d.be.Connected():
		s.State = api.StateConnected
	default:
		s.State, s.Action = api.StateConnecting, "the daemon is connecting to WhatsApp; stored messages can be read now"
	}
	if d.be != nil && d.be.Paired() {
		me := d.be.Me()
		if !me.PN.IsEmpty() {
			s.Phone = "+" + me.PN.User
		}
		s.UserID = me.ID().String()
		s.PushName = d.be.PushName()
	}
	return s
}

// session returns the open database and backend for a request, or the error
// of the state.
func (d *Daemon) session() (*store.DB, Backend, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.locked != "":
		return nil, nil, api.Errorf(api.Locked, "%s", d.locked)
	case d.be == nil || !d.be.Paired():
		return nil, nil, api.Errorf(api.NotPaired, "ask the user to run \"whatsapp-mcp pair\" in a terminal")
	}
	return d.db, d.be, nil
}

// actions returns the actions of the open session.
func (d *Daemon) actions() (whatsapp.Actions, Backend, error) {
	db, be, err := d.session()
	if err != nil {
		return whatsapp.Actions{}, nil, err
	}
	return whatsapp.Actions{DB: db, Cli: be.Client(), Rows: be.Rows()}, be, nil
}

// PairRequest is the body of POST /v1/pair.
type PairRequest struct {
	Phone      string `json:"phone,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// PairEvent is one line of the answer of POST /v1/pair.
type PairEvent struct {
	QR     string     `json:"qr,omitempty"`
	Code   string     `json:"code,omitempty"`
	Paired bool       `json:"paired,omitempty"`
	Error  *api.Error `json:"error,omitempty"`
}

// Pair makes a new key, opens a new database and links the device.
func (d *Daemon) Pair(ctx context.Context, req PairRequest, show func(PairEvent)) error {
	d.mu.Lock()
	if d.be != nil && d.be.Paired() {
		d.mu.Unlock()
		return api.Errorf(api.InvalidArgument, "this data folder is paired already: run \"whatsapp-mcp logout\" first")
	}
	if d.pairing {
		d.mu.Unlock()
		return api.Errorf(api.InvalidArgument, "a pairing runs already")
	}
	if d.db == nil {
		if store.Exists(d.cfg.DataDir) && d.locked != "" {
			d.mu.Unlock()
			return api.Errorf(api.Locked, "%s", d.locked)
		}
		key, err := d.newKey(req.Passphrase)
		if err != nil {
			d.mu.Unlock()
			return err
		}
		if err := d.open(ctx, key); err != nil {
			d.mu.Unlock()
			return err
		}
	} else {
		// The database has no paired device (an earlier pairing stopped):
		// start a new client on the same database.
		if d.be != nil {
			d.be.Close()
		}
		be, err := d.cfg.Backend(ctx, d.db, d.cfg.Retention, d.log, d.hub.Notify)
		if err != nil {
			d.mu.Unlock()
			return err
		}
		d.be = be
	}
	d.pairing = true
	be := d.be
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.pairing = false
		d.mu.Unlock()
	}()
	return be.Pair(ctx, req.Phone, 30*time.Second, func(e whatsapp.PairEvent) {
		show(PairEvent{QR: e.QR, Code: e.Code})
	})
}

// newKey makes the key of a new database and keeps it in the key store, or
// derives it from the passphrase. The caller holds d.mu.
func (d *Daemon) newKey(passphrase string) ([]byte, error) {
	if passphrase != "" {
		if _, err := keystore.NewSalt(d.cfg.DataDir); err != nil {
			return nil, err
		}
		return keystore.FromPassphrase(d.cfg.DataDir, passphrase)
	}
	key, err := keystore.NewKey()
	if err != nil {
		return nil, err
	}
	if err := d.cfg.Keys.Set(d.account(), key); err != nil {
		if errors.Is(err, keystore.ErrUnavailable) {
			return nil, api.Errorf(api.Locked, "no system key store is available (%v): run \"whatsapp-mcp pair --passphrase\"", err)
		}
		return nil, api.Errorf(api.Locked, "cannot store the key in the %s: %v", d.cfg.Keys.Name(), err)
	}
	return key, nil
}

// Unlock opens the database with the key of a passphrase.
func (d *Daemon) Unlock(ctx context.Context, passphrase string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		return nil
	}
	if !keystore.HasPassphrase(d.cfg.DataDir) {
		return api.Errorf(api.InvalidArgument, "this data folder does not use a passphrase")
	}
	key, err := keystore.FromPassphrase(d.cfg.DataDir, passphrase)
	if err != nil {
		return api.Errorf(api.InvalidArgument, "%v", err)
	}
	if err := d.open(ctx, key); err != nil {
		if errors.Is(err, store.ErrWrongKey) {
			d.locked = "the database key comes from a passphrase: ask the user to run \"whatsapp-mcp unlock\" in a terminal"
			return api.Errorf(api.Locked, "the passphrase is wrong")
		}
		return err
	}
	return nil
}

// LogoutResult is the answer of POST /v1/logout.
type LogoutResult struct {
	Unlinked bool   `json:"unlinked"`
	Message  string `json:"message,omitempty"`
}

// Logout unlinks the device on the phone and deletes the database and the key.
// It deletes the local data also when the unlink fails.
func (d *Daemon) Logout(ctx context.Context) (LogoutResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res := LogoutResult{Unlinked: true}
	if d.be != nil && d.be.Paired() && !d.be.LoggedOut() {
		lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := d.be.Logout(lctx)
		cancel()
		if err != nil {
			res.Unlinked = false
			res.Message = fmt.Sprintf("could not unlink the device (%v): on the phone, remove %q in WhatsApp > Settings > Linked devices", err, whatsapp.DeviceName)
		}
	}
	if d.be != nil {
		d.be.Close()
	}
	if d.db != nil {
		d.db.Close()
	}
	d.be, d.db, d.locked, d.retryKey = nil, nil, "", false
	if err := store.Remove(d.cfg.DataDir); err != nil {
		return res, err
	}
	_ = os.Remove(keystore.SaltPath(d.cfg.DataDir))
	if err := d.cfg.Keys.Delete(d.account()); err != nil && !errors.Is(err, keystore.ErrUnavailable) {
		return res, fmt.Errorf("the files are deleted, but the key item is not: %w", err)
	}
	return res, nil
}
