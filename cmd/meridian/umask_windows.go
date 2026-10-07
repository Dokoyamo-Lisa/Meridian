//go:build windows

package main

// privateUmask is a no-op on Windows; the backup file's permissions are set afterwards.
func privateUmask() func() { return func() {} }
