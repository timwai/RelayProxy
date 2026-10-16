package rdp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// This is a narrow decoder for V2Ray's routercommon.GeoIPList protobuf.
// GeoIPList.entry=1; GeoIP.country_code=1, GeoIP.cidr=2;
// CIDR.ip=1 (4 or 16 raw bytes), CIDR.prefix=2.
// Other fields are safely skipped so that this package does not need a V2Ray
// runtime dependency. Source: v2fly/v2ray-core/app/router/routercommon/common.proto.

const maxGeoDATBytes = 128 << 20
const maxGeoCIDRs = 2_000_000

type wireField struct {
	num   uint64
	wire  uint64
	value []byte
	n     uint64
}

// eachGeoField rejects truncated, overlong, or unsupported protobuf wire
// records rather than admitting RDP traffic from a partially decoded file.
func eachGeoField(data []byte, fn func(wireField) error) error {
	for len(data) != 0 {
		tag, width := binary.Uvarint(data)
		if width <= 0 || tag < 8 {
			return errors.New("invalid GeoIP protobuf tag")
		}
		data = data[width:]
		field := wireField{num: tag >> 3, wire: tag & 7}
		switch field.wire {
		case 0:
			value, n := binary.Uvarint(data)
			if n <= 0 {
				return errors.New("invalid GeoIP protobuf varint")
			}
			field.n = value
			data = data[n:]
		case 1:
			if len(data) < 8 {
				return errors.New("truncated GeoIP protobuf fixed64")
			}
			data = data[8:]
		case 2:
			size, n := binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data)-max(n, 0)) {
				return errors.New("invalid GeoIP protobuf length")
			}
			data = data[n:]
			field.value = data[:int(size)]
			data = data[int(size):]
		case 5:
			if len(data) < 4 {
				return errors.New("truncated GeoIP protobuf fixed32")
			}
			data = data[4:]
		default:
			return fmt.Errorf("unsupported GeoIP protobuf wire type %d", field.wire)
		}
		if err := fn(field); err != nil {
			return err
		}
	}
	return nil
}

func parseGeoCIDR(data []byte) (netip.Prefix, error) {
	var addr netip.Addr
	var bits uint64
	var hasIP, hasBits bool
	err := eachGeoField(data, func(f wireField) error {
		switch {
		case f.num == 1 && f.wire == 2:
			if len(f.value) == 4 {
				var raw [4]byte
				copy(raw[:], f.value)
				addr = netip.AddrFrom4(raw)
			} else if len(f.value) == 16 {
				var raw [16]byte
				copy(raw[:], f.value)
				addr = netip.AddrFrom16(raw)
			} else {
				return errors.New("GeoIP CIDR IP length must be 4 or 16")
			}
			hasIP = true
		case f.num == 2 && f.wire == 0:
			bits, hasBits = f.n, true
		}
		return nil
	})
	if err != nil {
		return netip.Prefix{}, err
	}
	if !hasIP || !hasBits || !addr.IsValid() || bits > uint64(addr.BitLen()) {
		return netip.Prefix{}, errors.New("invalid GeoIP CIDR")
	}
	return netip.PrefixFrom(addr, int(bits)).Masked(), nil
}

func rangeFromPrefix(p netip.Prefix, country string) countryRange {
	start := p.Addr()
	bits := p.Bits()
	if start.Is4() {
		raw := start.As4()
		for j := bits; j < 32; j++ {
			raw[j/8] |= 1 << uint(7-j%8)
		}
		return countryRange{start: start, end: netip.AddrFrom4(raw), country: country, bits: bits}
	}
	raw := start.As16()
	for j := bits; j < 128; j++ {
		raw[j/8] |= 1 << uint(7-j%8)
	}
	return countryRange{start: start, end: netip.AddrFrom16(raw), country: country, bits: bits}
}

func twoLetterCountry(country string) bool {
	if len(country) != 2 || country == "ZZ" {
		return false
	}
	for _, c := range country {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func loadV2RayCountryDB(path string) (*CountryDB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open V2Ray GeoIP dat: %w", err)
	}
	defer f.Close()
	all, err := io.ReadAll(io.LimitReader(f, maxGeoDATBytes+1))
	if err != nil {
		return nil, err
	}
	if len(all) > maxGeoDATBytes {
		return nil, errors.New("V2Ray geoip.dat exceeds maximum size")
	}
	db := &CountryDB{}
	seenCN := false
	entries := 0
	err = eachGeoField(all, func(outer wireField) error {
		if outer.num != 1 || outer.wire != 2 {
			return nil
		}
		var country string
		var inverse bool
		var ranges []netip.Prefix
		if err := eachGeoField(outer.value, func(f wireField) error {
			switch {
			case f.num == 1 && f.wire == 2:
				country = strings.ToUpper(string(f.value))
			case f.num == 2 && f.wire == 2:
				if entries+len(ranges) > maxGeoCIDRs {
					return errors.New("V2Ray GeoIP dat has too many CIDRs")
				}
				p, err := parseGeoCIDR(f.value)
				if err != nil {
					return err
				}
				ranges = append(ranges, p)
			case f.num == 3 && f.wire == 0:
				inverse = f.n != 0
			}
			return nil
		}); err != nil {
			return err
		}
		// Ignore special categories (CLOUDFLARE, PRIVATE, etc.).
		if !twoLetterCountry(country) || inverse {
			return nil
		}
		if country == "CN" && len(ranges) > 0 {
			seenCN = true
		}
		for _, p := range ranges {
			entry := rangeFromPrefix(p, country)
			if entry.start.Is4() {
				db.datV4 = append(db.datV4, entry)
			} else {
				db.datV6 = append(db.datV6, entry)
			}
		}
		entries += len(ranges)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("decode V2Ray GeoIP dat: %w", err)
	}
	if !seenCN || entries == 0 {
		return nil, errors.New("V2Ray GeoIP dat missing CN country category")
	}
	db.maxEndV4 = buildGeoRangeIndex(db.datV4)
	db.maxEndV6 = buildGeoRangeIndex(db.datV6)
	return db, nil
}

func buildGeoRangeIndex(ranges []countryRange) []netip.Addr {
	sort.Slice(ranges, func(i, j int) bool {
		if x := ranges[i].start.Compare(ranges[j].start); x != 0 {
			return x < 0
		}
		return ranges[i].bits > ranges[j].bits
	})
	maxEnd := make([]netip.Addr, len(ranges))
	var current netip.Addr
	for i, item := range ranges {
		if !current.IsValid() || item.end.Compare(current) > 0 {
			current = item.end
		}
		maxEnd[i] = current
	}
	return maxEnd
}

func (db *CountryDB) lookupDAT(addr netip.Addr) string {
	ranges, ends := db.datV6, db.maxEndV6
	if addr.Is4() {
		ranges, ends = db.datV4, db.maxEndV4
	}
	firstGreater := sort.Search(len(ranges), func(i int) bool {
		return ranges[i].start.Compare(addr) > 0
	})
	bestCountry, bestBits := "", -1
	for i := firstGreater - 1; i >= 0 && ends[i].Compare(addr) >= 0; i-- {
		r := ranges[i]
		if r.end.Compare(addr) < 0 {
			continue
		}
		// CN source records may overlap GeoLite2 country entries; protect
		// mainland sources from false country bans by giving CN priority.
		if r.country == "CN" {
			return "CN"
		}
		if r.bits > bestBits {
			bestBits, bestCountry = r.bits, r.country
		}
	}
	return bestCountry
}
