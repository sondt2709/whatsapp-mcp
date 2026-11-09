package keystore

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// testStore runs the same checks on any Store.
func testStore(t *testing.T, s Store) {
	t.Helper()
	account := "test-" + Account(t.TempDir())
	t.Cleanup(func() { _ = s.Delete(account) })

	if _, err := s.Get(account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a new account: err = %v, want ErrNotFound", err)
	}
	k1, _ := NewKey()
	if err := s.Set(account, k1); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get(account)
	if err != nil || !bytes.Equal(got, k1) {
		t.Fatalf("Get after Set: %x, %v; want %x", got, err, k1)
	}
	k2, _ := NewKey()
	if err := s.Set(account, k2); err != nil {
		t.Fatalf("Set again: %v", err)
	}
	if got, _ := s.Get(account); !bytes.Equal(got, k2) {
		t.Fatalf("Set did not replace the key")
	}
	if err := s.Delete(account); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(account); err != nil {
		t.Fatalf("Delete of a missing item: %v", err)
	}
}

func TestMemory(t *testing.T) { testStore(t, NewMemory()) }

// TestSystem uses the real key store. CI sets WHATSAPP_MCP_TEST_KEYRING with
// a temporary keychain (macOS) or gnome-keyring in a D-Bus session (Linux).
func TestSystem(t *testing.T) {
	if os.Getenv("WHATSAPP_MCP_TEST_KEYRING") == "" {
		t.Skip("set WHATSAPP_MCP_TEST_KEYRING=1 to use the system key store")
	}
	testStore(t, System())
}

func TestAccountIsStable(t *testing.T) {
	d := t.TempDir()
	if Account(d) != Account(d+"/.") {
		t.Fatal("Account differs for the same folder")
	}
	if Account(d) == Account(t.TempDir()) {
		t.Fatal("Account is the same for two folders")
	}
	if len(Account(d)) != 16 {
		t.Fatalf("Account length %d, want 16", len(Account(d)))
	}
}

func TestPassphrase(t *testing.T) {
	d := t.TempDir()
	if _, err := NewSalt(d); err != nil {
		t.Fatal(err)
	}
	if !HasPassphrase(d) {
		t.Fatal("HasPassphrase is false after NewSalt")
	}
	a, err := FromPassphrase(d, "correct horse")
	if err != nil || len(a) != KeySize {
		t.Fatalf("FromPassphrase: %d bytes, %v", len(a), err)
	}
	b, _ := FromPassphrase(d, "correct horse")
	c, _ := FromPassphrase(d, "wrong horse")
	if !bytes.Equal(a, b) || bytes.Equal(a, c) {
		t.Fatal("the same passphrase must give the same key, another passphrase another key")
	}
	if _, err := FromPassphrase(d, ""); err == nil {
		t.Fatal("an empty passphrase must fail")
	}
	info, _ := os.Stat(SaltPath(d))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("salt mode %o, want 600", info.Mode().Perm())
	}
}
