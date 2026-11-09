//go:build !darwin

package files

import "time"

// quarantine does nothing outside macOS.
func quarantine(string, time.Time) error { return nil }
