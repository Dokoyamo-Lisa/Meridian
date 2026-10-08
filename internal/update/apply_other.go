//go:build !unix

package update

import "errors"

// Apply installs updates on Linux only (where the panel runs).
func Apply(dataDir, current, arch string) error {
	return errors.New("the panel updates itself on Linux only")
}
