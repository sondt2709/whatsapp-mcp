package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sondt2709/whatsapp-mcp/internal/keystore"
)

// maxSocketPath is below the limit of macOS (104 bytes) and Linux (108 bytes).
const maxSocketPath = 100

// SocketPath returns the socket of a data folder: <data>/whatsapp-mcp.sock,
// or a socket in the temporary folder when that path is too long.
func SocketPath(dataDir string) string {
	p := filepath.Join(dataDir, "whatsapp-mcp.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	return filepath.Join(os.TempDir(), "whatsapp-mcp-"+keystore.Account(dataDir)+".sock")
}

// SocketPathFile holds the socket path when it is not in the data folder.
func SocketPathFile(dataDir string) string { return filepath.Join(dataDir, "whatsapp-mcp.sock.path") }

// LockPath is the lock file of the daemon.
func LockPath(dataDir string) string { return filepath.Join(dataDir, "whatsapp-mcp.lock") }

// LogPath is the log file of a daemon that a client started.
func LogPath(dataDir string) string { return filepath.Join(dataDir, "daemon.log") }

// ErrBusy is returned when another daemon holds the data folder.
var ErrBusy = errors.New("another whatsapp-mcp daemon holds the connection of this data folder")

// lock takes the exclusive lock of the data folder.
func lock(dataDir string) (func(), error) {
	f, err := os.OpenFile(LockPath(dataDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock %s: %w", LockPath(dataDir), err)
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// listen makes the socket with the mode 0600 and accepts only the user of
// this process. The caller holds the lock, so an old socket file is stale.
func listen(dataDir string) (net.Listener, string, error) {
	p := SocketPath(dataDir)
	_ = os.Remove(p)
	if p != filepath.Join(dataDir, "whatsapp-mcp.sock") {
		if err := os.WriteFile(SocketPathFile(dataDir), []byte(p), 0o600); err != nil {
			return nil, "", err
		}
	} else {
		_ = os.Remove(SocketPathFile(dataDir))
	}
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, "", err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		ln.Close()
		return nil, "", err
	}
	return &peerListener{Listener: ln, uid: os.Getuid()}, p, nil
}

// peerListener closes each connection of another user.
type peerListener struct {
	net.Listener
	uid int
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, err := peerUID(c)
		if err == nil && uid == l.uid {
			return c, nil
		}
		c.Close()
	}
}

// Dial connects to the socket of a data folder.
func Dial(ctx context.Context, dataDir string) (net.Conn, error) {
	p := SocketPath(dataDir)
	if b, err := os.ReadFile(SocketPathFile(dataDir)); err == nil {
		p = strings.TrimSpace(string(b))
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	return d.DialContext(ctx, "unix", p)
}
