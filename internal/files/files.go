// Package files reads and writes the files of the user for send_file and
// download_file. It runs in the client process (CLI or stdio MCP server),
// which has the folder access of the user. A file name from a sender is
// untrusted: the package keeps only a safe base name and never overwrites a
// file. A send reads only from the allowed folders.
package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/sanitize"
)

// maxName limits the bytes of a file name; macOS and Linux allow 255.
const maxName = 200

// extensions are the extensions of the common WhatsApp MIME types. The mime
// package gives several extensions for some types (image/jpeg).
var extensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"video/mp4": ".mp4", "video/3gpp": ".3gp", "audio/ogg": ".ogg", "audio/mpeg": ".mp3", "audio/mp4": ".m4a",
	"audio/aac": ".aac", "application/pdf": ".pdf",
}

// Name returns a safe file name: the base name that the sender gave, without
// hidden characters, path separators, ':' and dots at the start. With no
// usable name, it is "whatsapp-<message id>" and the extension of the MIME type.
func Name(given, mimeType, messageID string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':':
			return -1
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, sanitize.Line(given))
	name = strings.TrimLeft(strings.TrimSpace(name), ". ")
	if name == "" {
		// The WhatsApp ID is the part after the last "/" of a message ID.
		id := messageID[strings.LastIndex(messageID, "/")+1:]
		id = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				return r
			}
			return -1
		}, id)
		return "whatsapp-" + id + extension(mimeType)
	}
	if len(name) > maxName {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		stem := name[:maxName-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	return name
}

func extension(mimeType string) string {
	base, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return ""
	}
	if e, ok := extensions[base]; ok {
		return e
	}
	if exts, _ := mime.ExtensionsByType(base); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// candidate returns the name with " (n)" before the extension.
func candidate(name string, n int) string {
	if n == 0 {
		return name
	}
	ext := filepath.Ext(name)
	if ext == name {
		ext = ""
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
}

// DownloadDir returns the default folder of downloads: $XDG_DOWNLOAD_DIR on
// Linux when set, else ~/Downloads.
func DownloadDir() string {
	if d := os.Getenv("XDG_DOWNLOAD_DIR"); d != "" && filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "Downloads"
	}
	return filepath.Join(home, "Downloads")
}

// Saved is a file that Save wrote.
type Saved struct {
	Path   string
	Size   int64
	SHA256 string
}

// Save writes r to a new file in dir with the name, or with "name (1)",
// "name (2)" and so on when the name exists. It never overwrites a file. dir
// must exist. On macOS it sets the quarantine attribute.
func Save(dir, name string, r io.Reader) (Saved, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return Saved{}, api.Errorf(api.InvalidArgument, "the folder %q does not exist", dir)
	}
	tmp, err := os.CreateTemp(dir, ".whatsapp-mcp-*.part")
	if err != nil {
		return Saved{}, denied(dir, err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Saved{}, err
	}
	for i := 0; i < 1000; i++ {
		dst := filepath.Join(dir, candidate(name, i))
		err := os.Link(tmp.Name(), dst)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return Saved{}, denied(dir, err)
		}
		_ = os.Chmod(dst, 0o644)
		if err := quarantine(dst, time.Now()); err != nil {
			return Saved{}, fmt.Errorf("set the quarantine attribute: %w", err)
		}
		return Saved{Path: dst, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
	}
	return Saved{}, fmt.Errorf("%s: 1000 files with this name exist", filepath.Join(dir, name))
}

func denied(dir string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return api.Errorf(api.PathNotAllowed, "this process cannot write to %q (on macOS: give the terminal or the MCP client access in System Settings > Privacy & Security > Files and Folders): %v", dir, err)
	}
	return err
}

// OpenForSend opens a file for send_file. The file must be inside one of the
// allowed folders after all symbolic links are resolved, no name inside the
// allowed folder may start with ".", and it must be a regular file of at
// most api.MaxFileSize bytes.
func OpenForSend(path string, allowed []string) (*os.File, os.FileInfo, error) {
	if !filepath.IsAbs(path) {
		return nil, nil, api.Errorf(api.InvalidArgument, "the path %q is not absolute", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, nil, api.Errorf(api.NotFound, "cannot read %q: %v", path, err)
	}
	inside := false
	for _, a := range allowed {
		root, err := filepath.EvalSymlinks(a)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, real)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		hiddenPart := false
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				hiddenPart = true
			}
		}
		if !hiddenPart {
			inside = true
			break
		}
	}
	if !inside {
		return nil, nil, api.Errorf(api.PathNotAllowed, "%q is not inside an allowed folder (%s), or a name in it starts with \".\": move the file there, or start the daemon with --send-dir", path, strings.Join(allowed, ", "))
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, nil, api.Errorf(api.NotFound, "cannot read %q: %v", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, api.Errorf(api.InvalidArgument, "%q is not a regular file", path)
	}
	if info.Size() > api.MaxFileSize {
		f.Close()
		return nil, nil, api.Errorf(api.TooLarge, "the file has %d bytes; the maximum is %d", info.Size(), api.MaxFileSize)
	}
	return f, info, nil
}
