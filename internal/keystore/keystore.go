// Package keystore keeps the database key of a data folder in the key store
// of the system: the macOS Keychain or the Linux Secret Service. When no key
// store is available, the key comes from a passphrase (Argon2id). The key is
// never written to a file.
package keystore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Service is the service name of the key items.
const Service = "whatsapp-mcp"

// KeySize is the size of a database key in bytes.
const KeySize = 32

// ErrNotFound is returned when the key store has no key for the account.
var ErrNotFound = errors.New("the key store has no key for this data folder")

// ErrUnavailable is returned when the system has no usable key store, for
// example a Linux server without a Secret Service.
var ErrUnavailable = errors.New("no system key store is available")

// ErrDenied is returned when the user or the system refused the access, for
// example when the user did not allow the Keychain dialog.
var ErrDenied = errors.New("the key store refused the access")

// Store keeps one key for each account.
type Store interface {
	Get(account string) ([]byte, error)
	Set(account string, key []byte) error
	Delete(account string) error
	Name() string
}

// Account returns the account name of a data folder: the first 16 hex digits
// of the SHA-256 of its absolute path.
func Account(dataDir string) string {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:8])
}

// NewKey returns a random key.
func NewKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// System returns the key store of the operating system.
func System() Store { return system() }

// SaltPath is the file with the salt of the passphrase mode. The salt is not secret.
func SaltPath(dataDir string) string { return filepath.Join(dataDir, "key.salt") }

// HasPassphrase reports whether the data folder uses the passphrase mode.
func HasPassphrase(dataDir string) bool {
	_, err := os.Stat(SaltPath(dataDir))
	return err == nil
}

// NewSalt writes a new random salt for the passphrase mode and returns it.
func NewSalt(dataDir string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if err := os.WriteFile(SaltPath(dataDir), salt, 0o600); err != nil {
		return nil, err
	}
	return salt, nil
}

// FromPassphrase derives the key of the data folder from a passphrase and
// the salt file (Argon2id, 3 passes, 64 MiB).
func FromPassphrase(dataDir, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("the passphrase is empty")
	}
	salt, err := os.ReadFile(SaltPath(dataDir))
	if err != nil {
		return nil, fmt.Errorf("read the salt: %w", err)
	}
	return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, KeySize), nil
}

// Memory is a key store in memory, for tests.
type Memory struct {
	mu   sync.Mutex
	keys map[string][]byte
}

// NewMemory returns an empty memory key store.
func NewMemory() *Memory { return &Memory{keys: map[string][]byte{}} }

// Get implements Store.
func (m *Memory) Get(account string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[account]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), k...), nil
}

// Set implements Store.
func (m *Memory) Set(account string, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[account] = append([]byte(nil), key...)
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, account)
	return nil
}

// Name implements Store.
func (m *Memory) Name() string { return "memory" }
