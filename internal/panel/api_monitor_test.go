package panel

import "testing"

// TestFmtSize: a configured quota or bandwidth limit reads as it was entered - 1500 GB is not
// rounded to "1.5 TB" (or, as the panel once showed it, "1 TB") - while what does not fit two
// decimals in any unit falls back to fmtBytes.
func TestFmtSize(t *testing.T) {
	const (
		mb = int64(1) << 20
		gb = int64(1) << 30
		tb = int64(1) << 40
	)
	for _, c := range []struct {
		b    int64
		want string
	}{
		{1500 * gb, "1500 GB"},
		{tb + tb/2, "1.5 TB"},
		{2 * tb, "2 TB"},
		{200 * gb, "200 GB"},
		{gb + gb/4, "1.25 GB"},
		{512 * mb, "512 MB"},
		{1, "1 B"},
		{gb + 1, fmtBytes(gb + 1)},
	} {
		if got := fmtSize(c.b); got != c.want {
			t.Errorf("fmtSize(%d) = %q, want %q", c.b, got, c.want)
		}
	}
}
