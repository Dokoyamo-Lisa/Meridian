//go:build !unix

package main

import "os"

// fileOwner is unknown off Unix (the panel itself runs only on Linux).
func fileOwner(os.FileInfo) (uint32, bool) { return 0, false }
