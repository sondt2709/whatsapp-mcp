//go:build !fakewa

package main

import (
	"github.com/sondt2709/whatsapp-mcp/internal/daemon"
	"github.com/sondt2709/whatsapp-mcp/internal/keystore"
)

// backend returns the real WhatsApp backend.
func backend(string) daemon.NewBackend { return daemon.WhatsAppBackend }

// keyStore returns the key store of the system.
func keyStore() keystore.Store { return keystore.System() }
