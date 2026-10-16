package rdp

import (
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// Construct wire-compatible GeoIPList data without importing V2Ray packages.
func datVarintField(num uint64, n uint64) []byte {
	dst := binary.AppendUvarint(nil, num<<3)
	return binary.AppendUvarint(dst, n)
}
func datByteField(num uint64, b []byte) []byte {
	dst := binary.AppendUvarint(nil, num<<3|2)
	dst = binary.AppendUvarint(dst, uint64(len(b)))
	return append(dst, b...)
}
func datCIDR(ip string, bits uint64) []byte {
	addr := netip.MustParseAddr(ip)
	var raw []byte
	if addr.Is4() {
		a := addr.As4()
		raw = a[:]
	} else {
		a := addr.As16()
		raw = a[:]
	}
	return datByteField(2, append(datByteField(1, raw), datVarintField(2, bits)...))
}
func datCountry(code string, cidrs ...[]byte) []byte {
	out := datByteField(1, []byte(code))
	for _, cidr := range cidrs {
		out = append(out, cidr...)
	}
	return datByteField(1, out)
}
func writeDATFixture(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestV2RayGeoIPChinaAndOtherCountries(t *testing.T) {
	file := writeDATFixture(t, append(
		append(datCountry("US", datCIDR("1.2.0.0", 16), datCIDR("2001:db8::", 32)),
			datCountry("CN", datCIDR("1.2.3.0", 24), datCIDR("240e::", 20))...),
		datCountry("HK", datCIDR("2.2.2.0", 24))...))
	db, err := LoadCountryDB(file)
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]string{
		"1.2.3.10":    "CN", // CN data always wins overlap with US
		"1.2.4.10":    "US",
		"2.2.2.10":    "HK",
		"2001:db8::9": "US",
		"240e::abcd":  "CN",
		"3.3.3.3":     "", // Unknown is rejected without a permanent ban
	} {
		addr := netip.MustParseAddr(ip)
		if got := db.Lookup(addr); got != want {
			t.Fatalf("GeoIP DAT lookup(%s)=%q, want %q", ip, got, want)
		}
	}
}
func TestV2RayGeoIPRejectsBrokenAndMissingChina(t *testing.T) {
	for _, contents := range [][]byte{
		nil,
		{0x0a, 0x20, 0x08}, // truncated len
		datCountry("US", datCIDR("1.1.1.0", 24)),
		datCountry("CN", datCIDR("1.1.1.1", 44)), // invalid prefix length
	} {
		if _, err := LoadCountryDB(writeDATFixture(t, contents)); err == nil {
			t.Fatalf("invalid geoip.dat accepted: %x", contents)
		}
	}
}
func TestV2RayGeoIPPermanentBan(t *testing.T) {
	file := writeDATFixture(t, append(
		datCountry("CN", datCIDR("11.11.0.0", 16)),
		datCountry("US", datCIDR("22.22.0.0", 16))...))
	db := openSecurityTestDB(t)
	manager, err := NewSecurityManagerWithGeoIP(db, file)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := manager.Admit("ing-a", "11.11.1.2", true); !ok {
		t.Fatal("China source denied")
	}
	if ok, reason := manager.Admit("ing-b", "22.22.1.2", false); ok || reason != "GEO_NON_CN:US" {
		t.Fatalf("non-CN source not banned: %s", reason)
	}
	manager.Close()
	bans, err := db.ListActiveRDPSecurityBans()
	if err != nil || len(bans) != 1 || bans[0].ExpiresAt != nil || bans[0].CIDR != "22.22.1.2/32" {
		t.Fatalf("expected persistent global ban: %+v %v", bans, err)
	}
}
