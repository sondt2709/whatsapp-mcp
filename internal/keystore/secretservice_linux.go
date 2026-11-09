//go:build linux

package keystore

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// secretService stores the key in the Secret Service (GNOME Keyring, KWallet)
// through D-Bus. The Secret Service has no access list for each program: any
// program of the user can read an unlocked keyring.
type secretService struct{}

func system() Store { return secretService{} }

// Name implements Store.
func (secretService) Name() string { return "Secret Service" }

func wrap(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// Get implements Store.
func (secretService) Get(account string) ([]byte, error) {
	s, err := keyring.Get(Service, account)
	if err != nil {
		return nil, wrap(err)
	}
	k, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("secret service: the stored key is not hex: %w", err)
	}
	return k, nil
}

// Set implements Store.
func (secretService) Set(account string, key []byte) error {
	if err := keyring.Set(Service, account, hex.EncodeToString(key)); err != nil {
		return wrap(err)
	}
	return nil
}

// Delete implements Store. A missing item is not an error.
func (secretService) Delete(account string) error {
	err := keyring.Delete(Service, account)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return wrap(err)
}
