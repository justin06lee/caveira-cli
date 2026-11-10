//go:build !windows

package private

import "os"

// WriteFile writes data to path for its owner alone.
func WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
