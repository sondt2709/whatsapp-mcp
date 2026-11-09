//go:build !darwin

package files

// QuarantineAttr is only used by the macOS test.
const QuarantineAttr = ""

func getxattr(string, string) ([]byte, error) { return nil, nil }
