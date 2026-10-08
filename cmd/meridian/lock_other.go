//go:build !unix

package main

import "errors"

var errLocked = errors.New("locked")

// lockData is a no-op off Unix (the panel itself runs only on Linux).
func lockData(string) (func(), error) { return func() {}, nil }

// chownLike is a no-op off Unix.
func chownLike(string, string) error { return nil }
