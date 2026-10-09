package panel

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestMain uses the cheapest bcrypt cost: the tests check behaviour, not hashing speed.
func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	dummyHash, _ = bcrypt.GenerateFromPassword([]byte("meridian-dummy-password"), bcryptCost)
	restartHook.Store(func() {}) // a restore in a test never stops the test binary (backups.go)
	os.Exit(m.Run())
}
