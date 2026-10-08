// Command meridian-sign signs a release's SHA256SUMS with Meridian's release key, so panels install
// only releases its maintainers made (see internal/update). It is for maintainers and is not part of
// a release. The key file holds the ed25519 key's 32-byte seed in base64 and must be readable by its
// owner only; keep it off this repository and backed up.
//
//	meridian-sign -genkey -key ~/.config/meridian/release-signing.key   # prints the public key
//	meridian-sign -key ~/.config/meridian/release-signing.key dist/release/SHA256SUMS
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	keyPath := flag.String("key", os.Getenv("MERIDIAN_SIGNING_KEY"), "the release key file")
	gen := flag.Bool("genkey", false, "create a new key file and print its public key")
	flag.Parse()
	if *keyPath == "" {
		die("give the key file with -key (or MERIDIAN_SIGNING_KEY)")
	}
	if *gen {
		if _, err := os.Stat(*keyPath); err == nil {
			die(*keyPath + " exists already - a new key would make releases signed with it uninstallable for existing panels")
		}
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			die(err.Error())
		}
		if err := os.MkdirAll(filepath.Dir(*keyPath), 0o700); err != nil {
			die(err.Error())
		}
		if err := os.WriteFile(*keyPath, []byte(base64.StdEncoding.EncodeToString(seed)+"\n"), 0o600); err != nil {
			die(err.Error())
		}
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return
	}
	if flag.NArg() != 1 {
		die("usage: meridian-sign -key KEYFILE SHA256SUMS")
	}
	st, err := os.Stat(*keyPath)
	if err != nil {
		die(fmt.Sprintf("the release key %s is missing: %v", *keyPath, err))
	}
	if st.Mode().Perm()&0o077 != 0 {
		die(*keyPath + " can be read by others - chmod 600 it")
	}
	raw, err := os.ReadFile(*keyPath)
	if err != nil {
		die(err.Error())
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		die(*keyPath + " is not a release key")
	}
	sums, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		die(err.Error())
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), sums)
	out := flag.Arg(0) + ".sig"
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		die(err.Error())
	}
	fmt.Println("signed:", out)
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "meridian-sign:", msg)
	os.Exit(1)
}
