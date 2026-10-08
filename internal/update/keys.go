package update

// trusted are the public keys (ed25519, base64) whose signature a release's SHA256SUMS must carry.
// The private key stays with Meridian's maintainers; make release signs with it (cmd/meridian-sign).
var trusted = []string{
	"oYMEyv1vx5hEZT2qN7ZA+/TnAPPq7rIL23EQA3BK9hU=",
}

// testKey may be set at build time for an end-to-end test of updates
// (-ldflags "-X meridian/internal/update.testKey=..."); release builds leave it empty.
var testKey string
