//go:build !linux

package health

import "os"

// Off Linux (development only) the check runs in tests, where none of this matters.

func changeTime(os.FileInfo) int64 { return 0 }

func inode(os.FileInfo) uint64 { return 0 }

func lowerPriority() {}
