//go:build !unix

package sys

import "os"

// FileID is unknown off Unix (the agent runs only on Linux).
func FileID(os.FileInfo) uint64 { return 0 }
