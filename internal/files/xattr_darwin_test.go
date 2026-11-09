//go:build darwin

package files

import "golang.org/x/sys/unix"

func getxattr(path, name string) ([]byte, error) {
	buf := make([]byte, 256)
	n, err := unix.Getxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
