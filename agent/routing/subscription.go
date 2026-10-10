package routing

// Routing subscriptions are local agent policy inputs.  Server coordination is
// not involved: the agent fetches, parses and evaluates the list on its own.
import (
 "bufio"
 "context"
 "crypto/sha256"
 "encoding/base64"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net"
 "net/http"
 "net/netip"
 "net/url"
 "os"
 "path/filepath"
 "strings"
 "time"
)

// Subscription is an ordered external rule set. It is evaluated after the
// editable rules and before default_action, only in "rule" routing mode.
type Subscription struct {
 Name string `yaml:"name" json:"name"`
 URL string `yaml:"url" json:"url"`
 Enabled bool `yaml:"enabled" json:"enabled"`
 Action Action `yaml:"action" json:"action"`
 ExitID string `yaml:"exit_id,omitempty" json:"exit_id,omitempty"`
}

type SubscriptionStatus struct {
 Name string `json:"name"`
 URL string `json:"url"`
 Enabled bool `json:"enabled"`
 Rules int `json:"rules"`
 UpdatedAt time.Time `json:"updated_at,omitempty"`
 Error string `json:"error,omitempty"`
 Skipped int `json:"skipped"`
}

const (
 subscriptionLimit = 32
 subscriptionMaxBytes = 8 << 20
 subscriptionRefreshInterval = 6 * time.Hour
)

func validateSubscriptionURL(raw string) error {
 if len(raw) > 2048 || strings.TrimSpace(raw) != raw {
  return errors.New("subscription URL must be at most 2048 characters and have no surrounding whitespace")
 }
 u, err := url.Parse(raw)
 if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
  return errors.New("subscription URL must be an absolute HTTPS URL without credentials or fragment")
 }
 if u.Port() != "" && u.Port() != "443" {
  return errors.New("subscription URL must use HTTPS port 443")
 }
 host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
 if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
  return errors.New("subscription URL requires a public DNS hostname")
 }
 if ip, err := netip.ParseAddr(host); err == nil {
  return fmt.Errorf("subscription URL must use a DNS hostname, not %s", ip)
 }
 return nil
}

func validSubscriptionAddress(ip netip.Addr) bool {
 ip = ip.Unmap()
 return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
  !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}

// Resolve anew for every outbound connection, and dial the vetted IP rather
// than the original name to prevent DNS rebinding between checking and dialing.
func subscriptionDial(ctx context.Context, network, address string) (net.Conn, error) {
 host, port, err := net.SplitHostPort(address)
 if err != nil || port != "443" { return nil, errors.New("subscription connection must use TCP/443") }
 if _, err := netip.ParseAddr(host); err == nil { return nil, errors.New("subscription connection cannot use an IP URL") }
 ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
 if err != nil { return nil, err }
 var last error
 dialer := net.Dialer{Timeout: 8 * time.Second}
 for _, ip := range ips {
  if !validSubscriptionAddress(ip) { continue }
  conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
  if err == nil { return conn, nil }
  last = err
 }
 if last != nil { return nil, last }
 return nil, errors.New("subscription DNS returned no public addresses")
}

func fetchSubscription(ctx context.Context, raw string) ([]string, []string, int, error) {
 if err := validateSubscriptionURL(raw); err != nil { return nil, nil, 0, err }
 transport := &http.Transport{DialContext: subscriptionDial, TLSHandshakeTimeout: 8 * time.Second,
  ResponseHeaderTimeout: 10 * time.Second, DisableKeepAlives: true}
 defer transport.CloseIdleConnections()
 client := &http.Client{Transport: transport, Timeout: 25 * time.Second,
  CheckRedirect: func(req *http.Request, via []*http.Request) error {
   if len(via) >= 5 { return errors.New("too many subscription redirects") }
   return validateSubscriptionURL(req.URL.String())
  }}
 req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
 if err != nil { return nil, nil, 0, err }
 req.Header.Set("Accept", "text/plain, */*")
 resp, err := client.Do(req)
 if err != nil { return nil, nil, 0, err }
 defer resp.Body.Close()
 if resp.StatusCode != http.StatusOK { return nil, nil, 0, fmt.Errorf("subscription HTTP status %d", resp.StatusCode) }
 if resp.ContentLength > subscriptionMaxBytes { return nil, nil, 0, errors.New("subscription exceeds 8 MiB") }
 payload, err := io.ReadAll(io.LimitReader(resp.Body, subscriptionMaxBytes+1))
 if err != nil { return nil, nil, 0, err }
 if len(payload) > subscriptionMaxBytes { return nil, nil, 0, errors.New("subscription exceeds 8 MiB") }
 return parseSubscription(payload)
}

// GFWList is usually a single Base64-encoded Adblock Plus text file.
// We intentionally support only host-wide Adblock selectors, plain domains and
// IP/CIDR. URL paths, regex, options and substring filters cannot be faithfully
// represented by RelayProxy's host-based routing and are skipped, not widened.
func parseSubscription(payload []byte) ([]string, []string, int, error) {
 raw := strings.TrimSpace(string(payload))
 compact := strings.Join(strings.Fields(raw), "")
 if len(compact) > 32 && !strings.ContainsAny(compact, "!:/.^|*[]#@") {
  if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil && strings.Contains(string(decoded), "\n") {
   raw = string(decoded)
  } else if decoded, err := base64.RawStdEncoding.DecodeString(compact); err == nil && strings.Contains(string(decoded), "\n") {
   raw = string(decoded)
  }
 }
 var targets, exceptions []string
 seen, ignored := make(map[string]bool), 0
 scanner := bufio.NewScanner(strings.NewReader(raw))
 scanner.Buffer(make([]byte, 4096), 1024*1024)
 for scanner.Scan() {
  line := strings.TrimSpace(scanner.Text())
  if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") ||
   strings.HasPrefix(line, "[") || strings.HasPrefix(line, "//") { continue }
  except := strings.HasPrefix(line, "@@")
  if except { line = strings.TrimPrefix(line, "@@") }
  target, ok := subscriptionTarget(line)
  if !ok { ignored++; continue }
  key := target
  if except { key = "@@" + target }
  if seen[key] { continue }
  seen[key] = true
  if except { exceptions = append(exceptions, target) } else { targets = append(targets, target) }
  if len(targets)+len(exceptions) > 100000 { return nil, nil, ignored, errors.New("subscription exceeds 100000 selectors") }
 }
 if err := scanner.Err(); err != nil { return nil, nil, ignored, err }
 if len(targets) == 0 { return nil, nil, ignored, errors.New("subscription has no supported domain or IP rules") }
 return targets, exceptions, ignored, nil
}

func subscriptionTarget(line string) (string, bool) {
 if line == "" || strings.ContainsAny(line, "$ \t\r\n?[]") { return "", false }
 // Anchored host-based Adblock patterns: ||example.org^ and ||example.org
 if strings.HasPrefix(line, "||") {
  line = strings.TrimPrefix(line, "||")
  line = strings.TrimSuffix(line, "^")
  if strings.ContainsAny(line, "^/|") { return "", false }
 } else if strings.HasPrefix(line, "|") || strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
  line = strings.TrimPrefix(line, "|")
  u, err := url.Parse(line)
  if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
   u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() != "" {
   return "", false
  }
  line = u.Hostname()
 } else if strings.HasPrefix(line, "*.") {
  line = strings.TrimPrefix(line, "*.")
 } else if strings.HasPrefix(line, ".") {
  line = strings.TrimPrefix(line, ".")
 }
 // Reject unanchored ABP wildcards/paths: assuming whole-domain coverage
 // here would silently widen a path-specific filter.
 if strings.ContainsAny(line, "*/^|:@") { return "", false }
 if prefix, err := netip.ParsePrefix(line); err == nil && prefix.IsValid() {
  return prefix.Masked().String(), true
 }
 if ip, err := netip.ParseAddr(line); err == nil && ip.Zone() == "" {
  return ip.Unmap().String(), true
 }
 line = strings.ToLower(strings.TrimSuffix(line, "."))
 labels := strings.Split(line, ".")
 if len(labels) < 2 || len(line) > 253 { return "", false }
 for _, label := range labels {
  if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' { return "", false }
  for _, r := range label {
   if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') { return "", false }
  }
 }
 return "." + line, true // suffix matching includes the apex and subdomains
}

type subscriptionMatcher struct {
 domains map[string]struct{}
 ips map[netip.Addr]struct{}
 ranges []netip.Prefix
}
func compileSubscriptionMatcher(entries []string) subscriptionMatcher {
 m := subscriptionMatcher{domains: make(map[string]struct{}), ips: make(map[netip.Addr]struct{})}
 for _, target := range entries {
  if strings.HasPrefix(target, ".") { m.domains[strings.TrimPrefix(target, ".")] = struct{}{}; continue }
  if p, err := netip.ParsePrefix(target); err == nil { m.ranges = append(m.ranges, p.Masked()); continue }
  if ip, err := netip.ParseAddr(target); err == nil { m.ips[ip.Unmap()] = struct{}{} }
 }
 return m
}
func (m subscriptionMatcher) matches(f Flow) bool {
 host := strings.ToLower(strings.TrimSuffix(f.Host, "."))
 for host != "" {
  if _, ok := m.domains[host]; ok { return true }
  dot := strings.IndexByte(host, '.')
  if dot < 0 { break }
  host = host[dot+1:]
 }
 rawIP := f.IP
 if rawIP == "" { rawIP = f.Host }
 ip, err := netip.ParseAddr(rawIP)
 if err != nil { return false }
 ip = ip.Unmap()
 if _, ok := m.ips[ip]; ok { return true }
 for _, p := range m.ranges { if p.Contains(ip) { return true } }
 return false
}

type compiledSubscription struct {
 config Subscription
 included subscriptionMatcher
 excluded subscriptionMatcher
 status SubscriptionStatus
}
type subscriptionCache struct {
 URL string `json:"url"`
 Targets []string `json:"targets"`
 Exceptions []string `json:"exceptions"`
 Skipped int `json:"skipped"`
 UpdatedAt time.Time `json:"updated_at"`
}
func subscriptionCachePath(raw string) (string, error) {
 base, err := os.UserCacheDir()
 if err != nil { return "", err }
 sum := sha256.Sum256([]byte(raw))
 return filepath.Join(base, "RelayProxy", "routing-subscriptions", hex.EncodeToString(sum[:])+".json"), nil
}
func cachedSubscription(sub Subscription) compiledSubscription {
 state := compiledSubscription{config: sub, status: SubscriptionStatus{Name:sub.Name, URL:sub.URL, Enabled:sub.Enabled}}
 file, err := subscriptionCachePath(sub.URL)
 if err != nil { return state }
 data, err := os.ReadFile(file)
 if err != nil || len(data) > subscriptionMaxBytes*4 { return state }
 var cached subscriptionCache
 if json.Unmarshal(data, &cached) != nil || cached.URL != sub.URL || len(cached.Targets) == 0 { return state }
 state.included = compileSubscriptionMatcher(cached.Targets)
 state.excluded = compileSubscriptionMatcher(cached.Exceptions)
 state.status.Rules = len(cached.Targets)
 state.status.UpdatedAt = cached.UpdatedAt
 state.status.Skipped = cached.Skipped
 return state
}
func storeSubscription(raw string, targets, exceptions []string, skipped int) time.Time {
 now := time.Now().UTC()
 path, err := subscriptionCachePath(raw)
 if err != nil { return now }
 if os.MkdirAll(filepath.Dir(path), 0700) != nil { return now }
 body, err := json.Marshal(subscriptionCache{URL:raw, Targets:targets, Exceptions:exceptions, UpdatedAt:now, Skipped:skipped})
 if err != nil { return now }
 tmp, err := os.CreateTemp(filepath.Dir(path), ".subscription-*")
 if err != nil { return now }
 defer os.Remove(tmp.Name())
 if tmp.Chmod(0600) != nil || func() error { _, err = tmp.Write(body); return err }() != nil || tmp.Close() != nil { return now }
 _ = os.Rename(tmp.Name(), path) // Last-good snapshot remains on failed rename.
 return now
}

func (e *Engine) SubscriptionStatuses() []SubscriptionStatus {
 e.mu.RLock()
 defer e.mu.RUnlock()
 out := make([]SubscriptionStatus, len(e.subscriptions))
 for i, item := range e.subscriptions { out[i] = item.status }
 return out
}

func (e *Engine) startSubscriptionUpdates() {
 e.mu.Lock()
 if e.subscriptionCancel != nil { e.subscriptionCancel() }
 if len(e.config.Subscriptions) == 0 { e.subscriptionCancel = nil; e.mu.Unlock(); return }
 ctx, cancel := context.WithCancel(context.Background())
 e.subscriptionCancel = cancel
 e.mu.Unlock()
 go func() {
  e.updateSubscriptions(ctx)
  ticker := time.NewTicker(subscriptionRefreshInterval)
  defer ticker.Stop()
  for {
   select {
   case <-ctx.Done(): return
   case <-ticker.C: e.updateSubscriptions(ctx)
   }
  }
 }()
}
func (e *Engine) updateSubscriptions(ctx context.Context) {
 e.mu.RLock()
 subs := append([]Subscription(nil), e.config.Subscriptions...)
 e.mu.RUnlock()
 for index, sub := range subs {
  if !sub.Enabled { continue }
  select { case <-ctx.Done(): return; default: }
  targets, exceptions, skipped, err := fetchSubscription(ctx, sub.URL)
  updated := time.Time{}
  var inclusion, exclusion subscriptionMatcher
  if err == nil {
   inclusion, exclusion = compileSubscriptionMatcher(targets), compileSubscriptionMatcher(exceptions)
   updated = storeSubscription(sub.URL, targets, exceptions, skipped)
  }
  e.mu.Lock()
  if ctx.Err() == nil && index < len(e.subscriptions) && e.subscriptions[index].config == sub {
   current := &e.subscriptions[index]
   if err != nil {
    current.status.Error = err.Error() // Preserve the last good snapshot.
   } else {
    current.included, current.excluded = inclusion, exclusion
    current.status.Rules, current.status.Skipped, current.status.UpdatedAt = len(targets), skipped, updated
    current.status.Error = ""
   }
  }
  e.mu.Unlock()
 }
}

// Close cancels subscription refreshes (existing decisions remain usable).
func (e *Engine) Close() {
 e.mu.Lock()
 if e.subscriptionCancel != nil { e.subscriptionCancel(); e.subscriptionCancel = nil }
 e.mu.Unlock()
}
