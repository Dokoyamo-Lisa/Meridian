package main

import "crypto/rand"

func randRead(b []byte) (int, error) { return rand.Read(b) }
