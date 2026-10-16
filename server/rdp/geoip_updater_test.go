package rdp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relayproxy/server/repository"
)

func TestGeoIPUpdaterVerifiedCacheAndRollback(t *testing.T) {
	store := openSecurityTestDB(t)
	m, err := NewSecurityManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	oldData := append(datCountry("CN", datCIDR("11.0.0.0", 8)), datCountry("US", datCIDR("22.0.0.0", 8))...)
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, oldData, 0600); err != nil {
		t.Fatal(err)
	}
	u, err := NewGeoIPUpdater(m, path, "https://example.com/geoip.dat", 12)
	if err != nil {
		t.Fatal(err)
	}
	initial := u.Status()
	if !initial.Loaded || !initial.Enabled || initial.UpdateHours != 12 || initial.LastUpdatedAt == nil {
		t.Fatalf("initial cache status: %+v", initial)
	}
	if ok, _ := m.Admit("a", "11.0.0.1", false); !ok {
		t.Fatal("valid CN cache failed to load")
	}
	u.fetch = func(context.Context, string) ([]byte, error) { return []byte("not protobuf"), nil }
	if err := u.Refresh(context.Background()); err == nil {
		t.Fatal("malformed rules accepted")
	}
	disk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(disk, oldData) {
		t.Fatal("bad update replaced previous verified cache")
	}
	if u.Status().LastError == "" {
		t.Fatal("failure not exposed in runtime status")
	}
	if ok, _ := m.Admit("a", "11.0.0.2", false); !ok {
		t.Fatal("failed refresh changed active rules")
	}
	newData := append(datCountry("CN", datCIDR("33.0.0.0", 8)), datCountry("US", datCIDR("44.0.0.0", 8))...)
	u.fetch = func(context.Context, string) ([]byte, error) { return newData, nil }
	if err := u.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.Status().LastError != "" || u.Status().NextUpdateAt == nil {
		t.Fatalf("successful status not refreshed: %+v", u.Status())
	}
	disk, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(disk, newData) {
		t.Fatal("new policy not atomically persisted")
	}
	if ok, why := m.Admit("a", "33.0.0.1", false); !ok {
		t.Fatalf("new CN range denied: %s", why)
	}
	if ok, why := m.Admit("a", "11.0.0.1", false); ok || why != "GEO_COUNTRY_UNKNOWN" {
		t.Fatalf("stale country range remained active: %s", why)
	}
}

func TestGeoIPUpdaterNetworkFailurePreservesRules(t *testing.T) {
	store := openSecurityTestDB(t)
	m, err := NewSecurityManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	data := datCountry("CN", datCIDR("11.0.0.0", 8))
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	u, err := NewGeoIPUpdater(m, path, "https://example.com/geoip.dat", 1)
	if err != nil {
		t.Fatal(err)
	}
	u.fetch = func(context.Context, string) ([]byte, error) { return nil, errors.New("offline") }
	if err := u.Refresh(context.Background()); err == nil {
		t.Fatal("expected download error")
	}
	if !u.Status().Loaded || u.Status().LastError != "offline" {
		t.Fatal("existing good rules were lost on download failure")
	}
}

func TestGeoIPSourceURLRejectsLocalAndUnsafeURLs(t *testing.T) {
	for _, bad := range []string{
		"http://example.com/geoip.dat",
		"https://127.0.0.1/geoip.dat",
		"https://192.168.1.1/geoip.dat",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/x", "https://example.com:443/geoip.dat",
		"https://name:password@example.com/x",
		"https://example.com/rules#fragment",
	} {
		if err := ValidateGeoIPSourceURL(bad); err == nil {
			t.Errorf("unsafe source URL accepted: %s", bad)
		}
	}
	if err := ValidateGeoIPSourceURL("https://cdn.jsdelivr.net/gh/Loyalsoldier/v2ray-rules-dat@release/geoip.dat"); err != nil {
		t.Fatal(err)
	}
}

func TestGeoIPDisabledPreservesExistingAutoBanForFutureButDoesNotEnforce(t *testing.T) {
	db := openSecurityTestDB(t)
	_, err := db.CreateRDPSecurityBan("55.55.55.5", "", "auto", "GEO_NON_CN:US", "system", nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSecurityManager(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ok, why := s.Admit("ing", "55.55.55.5", false); !ok {
		t.Fatalf("disabled GeoIP restriction still active: %s", why)
	}
	normal, err := db.CreateRDPSecurityBan("66.66.66.6", "", "auto", "high_frequency", "system", nil)
	if err != nil || normal.ID == "" {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Admit("ing", "66.66.66.6", false); ok {
		t.Fatal("disabled GeoIP wrongly disabled regular rate-limit bans")
	}
}

func TestGeoIPUpdateContextCancellation(t *testing.T) {
	store := openSecurityTestDB(t)
	m, err := NewSecurityManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	data := datCountry("CN", datCIDR("11.0.0.0", 8))
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	u, err := NewGeoIPUpdater(m, path, "https://example.com/geoip.dat", 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u.fetch = func(ctx context.Context, _ string) ([]byte, error) { return nil, ctx.Err() }
	start := time.Now()
	u.Run(ctx)
	if time.Since(start) > time.Second {
		t.Fatal("cancelled updater did not stop promptly")
	}
}
