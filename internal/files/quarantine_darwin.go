//go:build darwin

package files

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// QuarantineAttr is the extended attribute that macOS reads before it opens
// a downloaded file.
const QuarantineAttr = "com.apple.quarantine"

// quarantine marks the file as downloaded from the internet, so that macOS
// asks before it opens a program from the file.
func quarantine(path string, now time.Time) error {
	v := fmt.Sprintf("0081;%x;whatsapp-mcp;", now.Unix())
	return unix.Setxattr(path, QuarantineAttr, []byte(v), 0)
}
