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
	os.Exit(m.Run())
}
