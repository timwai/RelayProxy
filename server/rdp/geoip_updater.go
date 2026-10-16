package rdp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GeoIPUpdateStatus reports the effective runtime state, not a pending YAML edit.
type GeoIPUpdateStatus struct {
	Enabled       bool       `json:"enabled"`
	Loaded        bool       `json:"loaded"`
	SourceURL     string     `json:"sourceUrl"`
	UpdateHours   int        `json:"updateHours"`
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	NextUpdateAt  *time.Time `json:"nextUpdateAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
}

type GeoIPUpdater struct {
	mu       sync.Mutex
	manager  *SecurityManager
	file     string
	source   string
	interval time.Duration
	status   GeoIPUpdateStatus
	fetch    func(context.Context, string) ([]byte, error)
}

func NewGeoIPUpdater(manager *SecurityManager, cachePath, sourceURL string, hours int) (*GeoIPUpdater, error) {
	if manager == nil || hours < 1 || hours > 720 {
		return nil, errors.New("invalid GeoIP updater configuration")
	}
	if err := ValidateGeoIPSourceURL(sourceURL); err != nil {
		return nil, err
	}
	if cachePath == "" {
		cachePath = filepath.Join("data", "geoip.dat")
	}
	u := &GeoIPUpdater{
		manager: manager, file: cachePath, source: sourceURL,
		interval: time.Duration(hours) * time.Hour,
		status: GeoIPUpdateStatus{Enabled: true, SourceURL: sourceURL, UpdateHours: hours},
		fetch: downloadGeoIP,
	}
	if db, err := LoadCountryDB(cachePath); err == nil {
		manager.SetCountryDB(db)
		u.status.Loaded = true
		if stat, err := os.Stat(cachePath); err == nil {
			t := stat.ModTime().UTC()
			u.status.LastUpdatedAt = &t
		}
	} else if err := u.Refresh(context.Background()); err != nil {
		return nil, fmt.Errorf("GeoIP enabled but cache invalid and source unavailable: %w", err)
	}
	return u, nil
}

// Run refreshes a stale cache in the background and retries every interval.
func (u *GeoIPUpdater) Run(ctx context.Context) {
	s := u.Status()
	if s.LastUpdatedAt == nil || time.Since(*s.LastUpdatedAt) >= u.interval {
		if err := u.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[RDP GeoIP] update failed; last verified database retained: %v", err)
		}
	}
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := u.Refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[RDP GeoIP] update failed; last verified database retained: %v", err)
			}
		}
	}
}

func (u *GeoIPUpdater) Status() GeoIPUpdateStatus {
	if u == nil {
		return GeoIPUpdateStatus{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.status
	if s.LastAttemptAt != nil {
		t := s.LastAttemptAt.Add(u.interval)
		s.NextUpdateAt = &t
	} else if s.LastUpdatedAt != nil {
		t := s.LastUpdatedAt.Add(u.interval)
		s.NextUpdateAt = &t
	}
	return s
}

func (u *GeoIPUpdater) Refresh(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now().UTC()
	u.status.LastAttemptAt = &now
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	bytes, err := u.fetch(ctx, u.source)
	if err == nil {
		err = u.install(bytes)
	}
	if err != nil {
		u.status.LastError = err.Error()
		return err
	}
	t := time.Now().UTC()
	u.status.LastError = ""
	u.status.Loaded = true
	u.status.LastUpdatedAt = &t
	return nil
}

func (u *GeoIPUpdater) install(data []byte) error {
	if len(data) == 0 || len(data) > maxGeoDATBytes {
		return errors.New("GeoIP download empty or exceeds 128 MiB")
	}
	suffix := ".dat"
	lower := strings.ToLower(u.file)
	if strings.HasSuffix(lower, ".csv.gz") {
		suffix = ".csv.gz"
	} else if strings.HasSuffix(lower, ".csv") {
		suffix = ".csv"
	} else if !strings.HasSuffix(lower, ".dat") {
		return errors.New("GeoIP cache needs .dat / .csv / .csv.gz extension")
	}
	if err := os.MkdirAll(filepath.Dir(u.file), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(u.file), ".geoip-stage-*"+suffix)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	db, err := LoadCountryDB(f.Name())
	if err != nil {
		return fmt.Errorf("downloaded GeoIP invalid: %w", err)
	}
	if err := os.Rename(f.Name(), u.file); err != nil {
		return err
	}
	u.manager.SetCountryDB(db)
	log.Printf("[RDP GeoIP] verified new rules installed: %s", u.file)
	return nil
}

// Source URLs are public HTTPS URLs only. Redirects are checked again and
// DNS targets are checked at dial time, preventing SSRF through DNS rebinding.
func ValidateGeoIPSourceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.Fragment != "" || u.Port() != "" || u.Opaque != "" ||
		strings.ContainsAny(u.Hostname(), " \t\r\n\\") {
		return errors.New("GeoIP source must be a public HTTPS URL (without credentials or custom port)")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !allowedGeoIPTarget(netip.MustParseAddr(ip.String())) {
		return errors.New("GeoIP source targets a reserved/private IP")
	}
	return nil
}

func allowedGeoIPTarget(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, raw := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"240.0.0.0/4", "2001:db8::/32",
	} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

func downloadGeoIP(ctx context.Context, raw string) ([]byte, error) {
	if err := ValidateGeoIPSourceURL(raw); err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			var d net.Dialer
			d.Timeout = 8 * time.Second
			for _, ip := range ips {
				if !allowedGeoIPTarget(ip) {
					continue
				}
				conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
			}
			return nil, errors.New("GeoIP source has no reachable public IP")
		},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many GeoIP redirects")
			}
			return ValidateGeoIPSourceURL(req.URL.String())
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GeoIP download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGeoDATBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxGeoDATBytes {
		return nil, errors.New("GeoIP download size invalid")
	}
	return data, nil
}
