package geo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"
)

// CountryList is the address space of a set of countries, as ranges ("a-b") and prefixes ("a/n"),
// in the form the agents' nftables sets take. Hash identifies it exactly.
type CountryList struct {
	Countries []string `json:"countries"`
	Source    string   `json:"source"` // database file it came from
	V4        []string `json:"v4"`
	V6        []string `json:"v6"`
	Hash      string   `json:"-"`
	Body      []byte   `json:"-"` // the JSON document agents fetch
}

// Codes are the ISO 3166-1 alpha-2 country codes (and XK, Kosovo, which the databases use).
var Codes = map[string]bool{
	"AD": true, "AE": true, "AF": true, "AG": true, "AI": true, "AL": true, "AM": true, "AO": true, "AQ": true,
	"AR": true, "AS": true, "AT": true, "AU": true, "AW": true, "AX": true, "AZ": true, "BA": true, "BB": true,
	"BD": true, "BE": true, "BF": true, "BG": true, "BH": true, "BI": true, "BJ": true, "BL": true, "BM": true,
	"BN": true, "BO": true, "BQ": true, "BR": true, "BS": true, "BT": true, "BV": true, "BW": true, "BY": true,
	"BZ": true, "CA": true, "CC": true, "CD": true, "CF": true, "CG": true, "CH": true, "CI": true, "CK": true,
	"CL": true, "CM": true, "CN": true, "CO": true, "CR": true, "CU": true, "CV": true, "CW": true, "CX": true,
	"CY": true, "CZ": true, "DE": true, "DJ": true, "DK": true, "DM": true, "DO": true, "DZ": true, "EC": true,
	"EE": true, "EG": true, "EH": true, "ER": true, "ES": true, "ET": true, "FI": true, "FJ": true, "FK": true,
	"FM": true, "FO": true, "FR": true, "GA": true, "GB": true, "GD": true, "GE": true, "GF": true, "GG": true,
	"GH": true, "GI": true, "GL": true, "GM": true, "GN": true, "GP": true, "GQ": true, "GR": true, "GS": true,
	"GT": true, "GU": true, "GW": true, "GY": true, "HK": true, "HM": true, "HN": true, "HR": true, "HT": true,
	"HU": true, "ID": true, "IE": true, "IL": true, "IM": true, "IN": true, "IO": true, "IQ": true, "IR": true,
	"IS": true, "IT": true, "JE": true, "JM": true, "JO": true, "JP": true, "KE": true, "KG": true, "KH": true,
	"KI": true, "KM": true, "KN": true, "KP": true, "KR": true, "KW": true, "KY": true, "KZ": true, "LA": true,
	"LB": true, "LC": true, "LI": true, "LK": true, "LR": true, "LS": true, "LT": true, "LU": true, "LV": true,
	"LY": true, "MA": true, "MC": true, "MD": true, "ME": true, "MF": true, "MG": true, "MH": true, "MK": true,
	"ML": true, "MM": true, "MN": true, "MO": true, "MP": true, "MQ": true, "MR": true, "MS": true, "MT": true,
	"MU": true, "MV": true, "MW": true, "MX": true, "MY": true, "MZ": true, "NA": true, "NC": true, "NE": true,
	"NF": true, "NG": true, "NI": true, "NL": true, "NO": true, "NP": true, "NR": true, "NU": true, "NZ": true,
	"OM": true, "PA": true, "PE": true, "PF": true, "PG": true, "PH": true, "PK": true, "PL": true, "PM": true,
	"PN": true, "PR": true, "PS": true, "PT": true, "PW": true, "PY": true, "QA": true, "RE": true, "RO": true,
	"RS": true, "RU": true, "RW": true, "SA": true, "SB": true, "SC": true, "SD": true, "SE": true, "SG": true,
	"SH": true, "SI": true, "SJ": true, "SK": true, "SL": true, "SM": true, "SN": true, "SO": true, "SR": true,
	"SS": true, "ST": true, "SV": true, "SX": true, "SY": true, "SZ": true, "TC": true, "TD": true, "TF": true,
	"TG": true, "TH": true, "TJ": true, "TK": true, "TL": true, "TM": true, "TN": true, "TO": true, "TR": true,
	"TT": true, "TV": true, "TW": true, "TZ": true, "UA": true, "UG": true, "UM": true, "US": true, "UY": true,
	"UZ": true, "VA": true, "VC": true, "VE": true, "VG": true, "VI": true, "VN": true, "VU": true, "WF": true,
	"WS": true, "XK": true, "YE": true, "YT": true, "ZA": true, "ZM": true, "ZW": true,
}

// ErrNoCountryDB means the country database has not been downloaded yet.
var ErrNoCountryDB = errors.New("the country database is not downloaded yet - it arrives within a few minutes of the panel's start (needs internet access to download.db-ip.com)")

type countryCache struct {
	mu   sync.Mutex
	key  string
	list *CountryList
}

// Countries returns the merged address ranges of the given countries. It reads the country database
// once per call set and keeps the latest result.
func (d *DB) Countries(ccs []string) (*CountryList, error) {
	if d == nil {
		return nil, ErrNoCountryDB
	}
	var codes []string
	seen := map[string]bool{}
	for _, c := range ccs {
		c = strings.ToUpper(strings.TrimSpace(c))
		if Codes[c] && !seen[c] {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	d.cc.mu.Lock()
	defer d.cc.mu.Unlock()
	// hold the read lock while walking the database, so a monthly reload cannot close it under us
	d.mu.RLock()
	defer d.mu.RUnlock()
	r, src := d.country, d.countrySrc
	if r == nil {
		return nil, ErrNoCountryDB
	}
	key := src + "|" + strings.Join(codes, ",")
	if d.cc.key == key && d.cc.list != nil {
		return d.cc.list, nil
	}
	l, err := buildCountryList(r, codes)
	if err != nil {
		return nil, err
	}
	l.Source = filepath.Base(src)
	body, _ := json.Marshal(l)
	sum := sha256.Sum256(body)
	l.Body, l.Hash = body, hex.EncodeToString(sum[:])
	d.cc.key, d.cc.list = key, l
	return l, nil
}

type rangeAcc struct {
	from, to netip.Addr
	open     bool
}

// buildCountryList walks the database once, keeping the networks of the wanted countries and
// merging neighbours into ranges.
func buildCountryList(r *maxminddb.Reader, codes []string) (*CountryList, error) {
	want := map[string]bool{}
	for _, c := range codes {
		want[c] = true
	}
	l := &CountryList{Countries: codes, V4: []string{}, V6: []string{}}
	if len(codes) == 0 {
		return l, nil
	}
	var acc4, acc6 rangeAcc
	flush := func(a *rangeAcc, out *[]string) {
		if !a.open {
			return
		}
		if a.from == a.to {
			*out = append(*out, a.from.String())
		} else {
			*out = append(*out, a.from.String()+"-"+a.to.String())
		}
		a.open = false
	}
	add := func(a *rangeAcc, out *[]string, p netip.Prefix) {
		first, last := p.Masked().Addr(), lastAddr(p)
		if a.open && a.to.Next() == first {
			a.to = last
			return
		}
		flush(a, out)
		a.from, a.to, a.open = first, last, true
	}
	for res := range r.Networks() {
		if err := res.Err(); err != nil {
			return nil, err
		}
		var cc string
		if err := res.DecodePath(&cc, "country", "iso_code"); err != nil || !want[cc] {
			continue
		}
		p := res.Prefix()
		if p.Addr().Is4In6() {
			continue // an alias of the IPv4 tree
		}
		if p.Addr().Is4() {
			add(&acc4, &l.V4, p)
		} else {
			add(&acc6, &l.V6, p)
		}
	}
	flush(&acc4, &l.V4)
	flush(&acc6, &l.V6)
	return l, nil
}

// lastAddr is the highest address in p.
func lastAddr(p netip.Prefix) netip.Addr {
	p = p.Masked()
	b := p.Addr().AsSlice()
	bits := p.Bits()
	for i := range b {
		for j := 0; j < 8; j++ {
			if i*8+j >= bits {
				b[i] |= 0x80 >> j
			}
		}
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// CountryOf is the two-letter country of an address ("" when unknown). It asks the country
// database - the same one the servers' rules are built from - and falls back to the city one.
func (d *DB) CountryOf(ip string) string {
	if d == nil {
		return ""
	}
	if a, err := netip.ParseAddr(strings.Trim(ip, "[]")); err == nil {
		d.mu.RLock()
		r := d.country
		var cc string
		if r != nil {
			_ = r.Lookup(a.Unmap()).DecodePath(&cc, "country", "iso_code")
		}
		d.mu.RUnlock()
		if cc != "" {
			return cc
		}
	}
	if g := d.Lookup(ip); g != nil {
		return g.Country
	}
	return ""
}

// Ready says whether lookups can tell countries.
func (d *DB) Ready() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.city != nil || d.country != nil
}

// CountryReady says whether country address lists can be built.
func (d *DB) CountryReady() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.country != nil
}
