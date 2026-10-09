//go:build devfetch

package panel

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// A development build only (go build -tags devfetch; never in a release): subscription links on
// https://<name>.test/ are read from the files in $MERIDIAN_DEV_SOURCES (<name>.txt), so the whole
// flow can be tried on a laptop with no provider. Every other address is fetched as always.
func init() {
	dir := os.Getenv("MERIDIAN_DEV_SOURCES")
	if dir == "" {
		return
	}
	real := fetchSource
	fetchSource = func(ctx context.Context, rawURL, client string) (string, *sourceUsage, error) {
		u, err := url.Parse(rawURL)
		if err != nil || !strings.HasSuffix(u.Hostname(), ".test") {
			return real(ctx, rawURL, client)
		}
		name := filepath.Base(strings.TrimSuffix(u.Hostname(), ".test"))
		b, err := os.ReadFile(filepath.Join(dir, name+".txt"))
		if err != nil {
			return "", nil, errStatus(502, "the address answered 404 - check it in a browser")
		}
		return string(b), parseUserinfo("upload=1073741824; download=4294967296; total=107374182400; expire=1798761600"), nil
	}
}
