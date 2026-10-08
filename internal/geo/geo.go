// Package geo locates IP addresses with the DB-IP Lite databases (CC BY 4.0). Lookups are local;
// the panel downloads the free databases once a month.
package geo

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

type Info struct {
	Country string  `json:"country"`
	City    string  `json:"city"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	ASN     uint    `json:"asn"`
	Org     string  `json:"org"`
}

type DB struct {
	dir string

	mu         sync.RWMutex
	city       *maxminddb.Reader
	asn        *maxminddb.Reader
	country    *maxminddb.Reader
	countrySrc string

	cc countryCache

	cmu   sync.Mutex
	cache map[string]*Info

	// OnLoad is called after a download put new databases in place (country rules can be built then)
	OnLoad func()
}

// Static answers lookups from a fixed table only - for tests.
func Static(known map[string]*Info) *DB {
	d := &DB{cache: map[string]*Info{}}
	for ip, info := range known {
		d.cache[ip] = info
	}
	return d
}

func Open(dataDir string) *DB {
	d := &DB{dir: filepath.Join(dataDir, "geo"), cache: map[string]*Info{}}
	d.load()
	return d
}

func (d *DB) load() {
	city := latest(d.dir, "dbip-city-lite-")
	asn := latest(d.dir, "dbip-asn-lite-")
	country := latest(d.dir, "dbip-country-lite-")
	d.mu.Lock()
	defer d.mu.Unlock()
	if country != "" && country != d.countrySrc {
		if r, err := maxminddb.Open(country); err == nil {
			if d.country != nil {
				d.country.Close()
			}
			d.country, d.countrySrc = r, country
		} else {
			slog.Warn("geo country db", "err", err)
		}
	}
	if city != "" {
		if r, err := maxminddb.Open(city); err == nil {
			if d.city != nil {
				d.city.Close()
			}
			d.city = r
		} else {
			slog.Warn("geo city db", "err", err)
		}
	}
	if asn != "" {
		if r, err := maxminddb.Open(asn); err == nil {
			if d.asn != nil {
				d.asn.Close()
			}
			d.asn = r
		} else {
			slog.Warn("geo asn db", "err", err)
		}
	}
	d.cmu.Lock()
	d.cache = map[string]*Info{}
	d.cmu.Unlock()
}

func latest(dir, prefix string) string {
	m, _ := filepath.Glob(filepath.Join(dir, prefix+"*.mmdb"))
	if len(m) == 0 {
		return ""
	}
	sort.Strings(m)
	return m[len(m)-1]
}

type cityRecord struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

type asnRecord struct {
	Number uint   `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// Lookup returns what is known about ip, or nil. The city database gives the place; without it
// (still downloading, or a download failed) the country database still names the country.
func (d *DB) Lookup(ip string) *Info {
	if d == nil || ip == "" {
		return nil
	}
	d.cmu.Lock()
	if v, ok := d.cache[ip]; ok {
		d.cmu.Unlock()
		return v
	}
	d.cmu.Unlock()
	addr, err := netip.ParseAddr(strings.Trim(ip, "[]"))
	if err != nil {
		return nil
	}
	addr = addr.Unmap()
	d.mu.RLock()
	city, asn, country := d.city, d.asn, d.country
	var out *Info
	if city != nil || asn != nil || country != nil {
		out = &Info{}
		if city != nil {
			var rec cityRecord
			if err := city.Lookup(addr).Decode(&rec); err == nil {
				out.Country, out.City = rec.Country.ISOCode, rec.City.Names["en"]
				out.Lat, out.Lon = rec.Location.Latitude, rec.Location.Longitude
			}
		}
		if out.Country == "" && country != nil {
			_ = country.Lookup(addr).DecodePath(&out.Country, "country", "iso_code")
		}
		if asn != nil {
			var rec asnRecord
			if err := asn.Lookup(addr).Decode(&rec); err == nil {
				out.ASN, out.Org = rec.Number, rec.Org
			}
		}
		if out.Country == "" && out.ASN == 0 {
			out = nil
		}
	}
	d.mu.RUnlock()
	if city == nil && asn == nil && country == nil {
		return nil // not loaded yet: do not cache the miss
	}
	d.cmu.Lock()
	if len(d.cache) > 50000 {
		d.cache = map[string]*Info{}
	}
	d.cache[ip] = out
	d.cmu.Unlock()
	return out
}

// Maintain downloads this month's databases when missing and reloads them.
func (d *DB) Maintain(ctx context.Context) {
	if os.Getenv("MERIDIAN_NO_GEO_DOWNLOAD") != "" || d.dir == "" { // d.dir is empty for a Static table
		return
	}
	for {
		month := time.Now().UTC().Format("2006-01")
		changed := false
		for _, kind := range []string{"city", "asn", "country"} {
			name := fmt.Sprintf("dbip-%s-lite-%s.mmdb", kind, month)
			path := filepath.Join(d.dir, name)
			if _, err := os.Stat(path); err == nil {
				continue
			}
			if err := download(ctx, "https://download.db-ip.com/free/"+name+".gz", path); err != nil {
				slog.Warn("geo download", "db", name, "err", err)
				continue
			}
			changed = true
			// keep only the newest file of each kind
			old, _ := filepath.Glob(filepath.Join(d.dir, fmt.Sprintf("dbip-%s-lite-*.mmdb", kind)))
			for _, f := range old {
				if f != path {
					os.Remove(f)
				}
			}
		}
		if changed {
			d.load()
			if d.OnLoad != nil {
				d.OnLoad()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(12 * time.Hour):
		}
	}
}

func download(ctx context.Context, url, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, gz); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
