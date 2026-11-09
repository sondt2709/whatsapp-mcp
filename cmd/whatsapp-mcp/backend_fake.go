//go:build fakewa

// This file is only in the binary of the e2e tests (go build -tags fakewa).
// It replaces WhatsApp with the fake backend and the key store with a test
// file, so the tests need no account and no key store of the system.

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sondt2709/whatsapp-mcp/internal/daemon"
	"github.com/sondt2709/whatsapp-mcp/internal/fakewa"
	"github.com/sondt2709/whatsapp-mcp/internal/keystore"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

func backend(dataDir string) daemon.NewBackend {
	f := fakewa.Factory(filepath.Join(dataDir, "fake-inbox"))
	return func(ctx context.Context, db *store.DB, r time.Duration, l *slog.Logger, on func()) (daemon.Backend, error) {
		return f(ctx, db, r, l, on)
	}
}

func keyStore() keystore.Store {
	return &testKeys{path: os.Getenv("WHATSAPP_MCP_FAKE_KEYFILE")}
}

// testKeys keeps keys in a JSON file. Only for the e2e tests.
type testKeys struct {
	mu   sync.Mutex
	path string
}

func (k *testKeys) load() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(k.path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (k *testKeys) save(m map[string]string) error {
	b, _ := json.Marshal(m)
	return os.WriteFile(k.path, b, 0o600)
}

func (k *testKeys) Name() string { return "test key file" }

func (k *testKeys) Get(account string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.path == "" {
		return nil, keystore.ErrUnavailable
	}
	v, ok := k.load()[account]
	if !ok {
		return nil, keystore.ErrNotFound
	}
	return hex.DecodeString(v)
}

func (k *testKeys) Set(account string, key []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.path == "" {
		return keystore.ErrUnavailable
	}
	m := k.load()
	m[account] = hex.EncodeToString(key)
	return k.save(m)
}

func (k *testKeys) Delete(account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.path == "" {
		return nil
	}
	m := k.load()
	delete(m, account)
	return k.save(m)
}
