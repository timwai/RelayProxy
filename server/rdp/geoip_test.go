package rdp

import (
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relayproxy/server/repository"
)

const geoFixture = "198.51.100.0,198.51.100.255,US\n203.0.113.0,203.0.113.255,CN\n192.0.2.0,192.0.2.255,ZZ\n2001:db8::,2001:db8::ffff,HK\n"

func writeGeoFixture(t *testing.T, zipped bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "country.csv")
	if zipped {
		path += ".gz"
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if zipped {
		z := gzip.NewWriter(f)
		if _, err := z.Write([]byte(geoFixture)); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	} else if _, err := f.WriteString(geoFixture); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGeoCountryCSVAndGzip(t *testing.T) {
	for _, zipped := range []bool{false, true} {
		db, err := LoadCountryDB(writeGeoFixture(t, zipped))
		if err != nil {
			t.Fatal(err)
		}
		for ip, want := range map[string]string{
			"198.51.100.20": "US", "203.0.113.55": "CN", "2001:db8::1234": "HK",
			"203.0.114.1": "", "192.0.2.1": "",
		} {
			a, _ := netip.ParseAddr(ip)
			if got := db.Lookup(a); got != want {
				t.Errorf("lookup %s = %q, want %q", ip, got, want)
			}
		}
	}
}

func TestGeoCountryPolicyPermanentBanAcrossIngressAndRestart(t *testing.T) {
	db := openSecurityTestDB(t)
	file := writeGeoFixture(t, false)
	s, err := NewSecurityManagerWithGeoIP(db, file)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := s.Admit("ing_a", "203.0.113.1", true); !ok {
		t.Fatalf("CN denied: %s", why)
	}
	if ok, why := s.Admit("ing_a", "198.51.100.9", true); ok || why != "GEO_NON_CN:US" {
		t.Fatalf("US admitted: %s", why)
	}
	if ok, why := s.Admit("ing_b", "198.51.100.9", false); ok || why != "GEO_NON_CN:US" {
		t.Fatalf("UDP or other ingress admitted: %s", why)
	}
	if _, err := db.CreateRDPSecurityBan("198.51.100.9", "", "allow", "operator", "admin", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Admit("ing_a", "198.51.100.9", true); ok {
		t.Fatal("allowlist overrode country ban")
	}
	for i := 0; i < 3; i++ {
		s.Admit("ing_a", "198.51.100.9", true)
	}
	s.Close()
	bans, err := db.ListActiveRDPSecurityBans()
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, b := range bans {
		if strings.HasPrefix(b.Reason, "GEO_NON_CN:") {
			found++
			if b.ExpiresAt != nil || b.IngressID != "" || b.CIDR != "198.51.100.9/32" {
				t.Fatalf("country ban must be global permanent: %+v", b)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one geo ban, got %d", found)
	}
	s, err = NewSecurityManagerWithGeoIP(db, file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ok, _ := s.Admit("ing_b", "198.51.100.9", false); ok {
		t.Fatal("restart lost country ban")
	}
}

func TestGeoCountryUnknownNoPermanentBanAndPrivateIP(t *testing.T) {
	db := openSecurityTestDB(t)
	s, err := NewSecurityManagerWithGeoIP(db, writeGeoFixture(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ok, why := s.Admit("ing", "192.0.2.4", false); ok || why != "GEO_COUNTRY_UNKNOWN" {
		t.Fatalf("unknown IP: %v %s", ok, why)
	}
	if ok, why := s.Admit("ing", "192.168.1.9", true); !ok {
		t.Fatalf("private IP should not be country banned: %s", why)
	}
	bans, err := db.ListActiveRDPSecurityBans()
	if err != nil {
		t.Fatal(err)
	}
	if len(bans) != 0 {
		t.Fatalf("unknown/private IP permanently banned: %+v", bans)
	}
}

func TestGeoCountryRejectsInvalidOrOverlappingFile(t *testing.T) {
	for _, bad := range []string{"", "1.1.1.2,1.1.1.1,US\n", "1.1.1.0,1.1.1.9,US\n1.1.1.8,1.1.1.20,CN\n", "1.1.1.0,1.1.1.2,USA\n"} {
		p := filepath.Join(t.TempDir(), "bad.csv")
		if err := os.WriteFile(p, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCountryDB(p); err == nil {
			t.Fatalf("invalid file accepted: %q", bad)
		}
	}
}

func TestGeoCountryAppearsInAuditSummariesAndDetails(t *testing.T) {
	db := openSecurityTestDB(t)
	s, err := NewSecurityManagerWithGeoIP(db, writeGeoFixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.Record(repository.RDPSecurityLog{
		IngressID: "entry", TargetDeviceID: "target",
		SourceIP: "198.51.100.88", Transport: "tcp",
		Result: "REJECTED", Reason: "GEO_NON_CN:US",
		StartedAt: now, EndedAt: now,
	})
	s.Close()
	page, err := db.ListRDPSecurityLogPage("198.51.100.88", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceCountry != "US" {
		t.Fatalf("missing country from RDP detail: %+v", page.Items)
	}
	groups, err := db.ListRDPSecuritySourceGroups("198.51.100.88", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups.Items) != 1 || groups.Items[0].SourceCountry != "US" {
		t.Fatalf("missing country from source IP group: %+v", groups.Items)
	}
}
