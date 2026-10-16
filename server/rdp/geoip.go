package rdp

import (
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CountryDB is an immutable, in-memory index for the DB-IP Country Lite CSV
// (ip_start,ip_end,ISO-3166-1 alpha-2 country). It does not make per-connection
// HTTP requests or require any external Go module.
type CountryDB struct {
	v4 []countryRange
	v6 []countryRange
	// datV4/V6 may contain overlapping CIDRs from different sources. Prefix
	// maximum ends allow log-time lookups plus a short overlap scan.
	datV4    []countryRange
	datV6    []countryRange
	maxEndV4 []netip.Addr
	maxEndV6 []netip.Addr
}

type countryRange struct {
	start   netip.Addr
	end     netip.Addr
	country string
	bits    int
}

// LoadCountryDB supports V2Ray geoip.dat and DB-IP Country Lite CSV / CSV.GZ. Validate the
// entire file before publishing it to the RDP admission path. Never use a
// partial database to decide on permanent IP bans.
func LoadCountryDB(path string) (*CountryDB, error) {
	if strings.EqualFold(filepath.Ext(path), ".dat") {
		return loadV2RayCountryDB(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open RDP GeoIP country file: %w", err)
	}
	defer f.Close()
	var input io.Reader = f
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("open RDP GeoIP gzip: %w", err)
		}
		defer zr.Close()
		input = zr
	}
	// Even if the compressed input is small, reject unexpectedly huge data.
	r := csv.NewReader(io.LimitReader(input, 128<<20))
	r.FieldsPerRecord = 3
	db := &CountryDB{}
	for rows := 0; ; rows++ {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid RDP GeoIP row %d: %w", rows+1, err)
		}
		if rows >= 2_000_000 {
			return nil, errors.New("RDP GeoIP database has too many ranges")
		}
		first, err1 := netip.ParseAddr(strings.TrimSpace(record[0]))
		last, err2 := netip.ParseAddr(strings.TrimSpace(record[1]))
		country := strings.ToUpper(strings.TrimSpace(record[2]))
		if err1 != nil || err2 != nil || first.Is4() != last.Is4() ||
			first.Compare(last) > 0 || len(country) != 2 ||
			country[0] < 'A' || country[0] > 'Z' ||
			country[1] < 'A' || country[1] > 'Z' {
			return nil, fmt.Errorf("invalid RDP GeoIP range or country at row %d", rows+1)
		}
		item := countryRange{start: first, end: last, country: country}
		if first.Is4() {
			db.v4 = append(db.v4, item)
		} else {
			db.v6 = append(db.v6, item)
		}
	}
	if len(db.v4)+len(db.v6) == 0 {
		return nil, errors.New("RDP GeoIP country file is empty")
	}
	for _, ranges := range [][]countryRange{db.v4, db.v6} {
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].start.Compare(ranges[j].start) < 0 })
		for i := 1; i < len(ranges); i++ {
			if ranges[i-1].end.Compare(ranges[i].start) >= 0 {
				return nil, fmt.Errorf("overlapping RDP GeoIP ranges starting at %s", ranges[i].start)
			}
		}
	}
	return db, nil
}

func (db *CountryDB) Lookup(addr netip.Addr) string {
	if db == nil || !addr.IsValid() {
		return ""
	}
	addr = addr.Unmap()
	if len(db.datV4)+len(db.datV6) != 0 {
		return db.lookupDAT(addr)
	}
	ranges := db.v6
	if addr.Is4() {
		ranges = db.v4
	}
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].start.Compare(addr) > 0 })
	if i == 0 || ranges[i-1].end.Compare(addr) < 0 {
		return ""
	}
	// DB-IP uses ZZ for blocks without a meaningful location. Unknown is
	// denied without creating an irreversible country ban.
	if ranges[i-1].country == "ZZ" {
		return ""
	}
	return ranges[i-1].country
}

// Private, local and special-use addresses must never enter the automatic
// permanent country-ban list. Classification for unknown public IPs fails
// closed (connection rejected), but does not create an irreversible ban.
func isPublicGeoIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	return addr.IsGlobalUnicast() && !addr.IsPrivate() &&
		!addr.IsLoopback() && !addr.IsLinkLocalUnicast() &&
		!addr.IsUnspecified()
}
