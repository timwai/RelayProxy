// Package traffic keeps bounded, in-memory connection telemetry. Reading a
// snapshot never resets counters or rates, so independent viewers agree.
package traffic

import (
	"net/netip"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	bucketWidth = 250 * time.Millisecond
	bucketCount = 8
)

type Metadata struct {
	ProcessID    uint32 `json:"pid"`
	Process      string `json:"process"`
	ProcessName  string `json:"process_name"`
	Source       string `json:"source"`
	Host         string `json:"host"`
	DomainSource string `json:"domain_source"` // requested, dns, or unknown
	IP           string `json:"ip"`
	Port         uint16 `json:"port"`
	Protocol     string `json:"protocol"`
	Entry        string `json:"entry"`
	Action       string `json:"action"`
	Rule         string `json:"rule"`
	ExitID       string `json:"exit_id"`
	Accounting   string `json:"accounting"` // stream payload or observed packet payload
}

type Connection struct {
	Metadata
	ID           uint64     `json:"id"`
	State        string     `json:"state"`
	Error        string     `json:"error,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	Duration     float64    `json:"duration"`
	Upload       uint64     `json:"upload"`
	Download     uint64     `json:"download"`
	UploadRate   float64    `json:"upload_rate"`
	DownloadRate float64    `json:"download_rate"`
}

type Snapshot struct {
	Connections  []Connection `json:"connections"`
	Active       int          `json:"active"`
	Total        uint64       `json:"total"`
	Omitted      uint64       `json:"omitted"`
	Upload       uint64       `json:"upload"`
	Download     uint64       `json:"download"`
	UploadRate   float64      `json:"upload_rate"`
	DownloadRate float64      `json:"download_rate"`
	SampledAt    time.Time    `json:"sampled_at"`
	RateWindow   float64      `json:"rate_window"`
}

type bucket struct {
	tick     int64
	up, down uint64
}
type meter struct {
	up, down uint64
	buckets  [bucketCount]bucket
}

func (m *meter) add(now time.Time, up, down uint64) {
	m.up += up
	m.down += down
	tick := now.UnixNano() / int64(bucketWidth)
	b := &m.buckets[tick%bucketCount]
	if b.tick != tick {
		*b = bucket{tick: tick}
	}
	b.up += up
	b.down += down
}

func (m *meter) rates(now time.Time) (float64, float64) {
	tick := now.UnixNano() / int64(bucketWidth)
	var up, down uint64
	for _, b := range m.buckets {
		if b.tick <= tick && tick-b.tick < bucketCount {
			up += b.up
			down += b.down
		}
	}
	seconds := (bucketWidth * bucketCount).Seconds()
	return float64(up) / seconds, float64(down) / seconds
}

type Registry struct {
	mu                   sync.Mutex
	now                  func() time.Time
	maxActive, maxRecent int
	next, total, omitted uint64
	active               map[uint64]*Record
	unlistedActive       int
	recent               []*Record
	meter                meter
}

type Record struct {
	registry   *Registry
	connection Connection
	meter      meter
	finished   bool
	listed     bool
}

func NewRegistry(maxActive, maxRecent int) *Registry {
	if maxActive <= 0 {
		maxActive = 8192
	}
	if maxRecent <= 0 {
		maxRecent = 512
	}
	return &Registry{now: time.Now, maxActive: maxActive, maxRecent: maxRecent, active: make(map[uint64]*Record)}
}

func (r *Registry) Start(meta Metadata) *Record {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total++
	listed := len(r.active) < r.maxActive
	r.next++
	if meta.Process != "" {
		meta.ProcessName = path.Base(strings.ReplaceAll(meta.Process, "\\", "/"))
	}
	if addr, err := netip.ParseAddr(meta.Host); err == nil {
		if meta.IP == "" {
			meta.IP = addr.Unmap().String()
		}
		meta.Host = ""
	}
	if meta.Host == "" {
		meta.DomainSource = "unknown"
	}
	if meta.Accounting == "" {
		meta.Accounting = "stream"
	}
	record := &Record{registry: r, listed: listed, connection: Connection{Metadata: meta, ID: r.next, State: "connecting", StartedAt: r.now()}}
	if listed {
		r.active[r.next] = record
	} else {
		r.omitted++
		r.unlistedActive++
	}
	return record
}

func (r *Record) Activate() {
	if r == nil {
		return
	}
	r.registry.mu.Lock()
	defer r.registry.mu.Unlock()
	if !r.finished {
		r.connection.State = "active"
	}
}

// SetIP is only used for an actual peer address. A relay transport's RemoteAddr
// must never be reported as the original destination.
func (r *Record) SetIP(ip string) {
	if r == nil {
		return
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return
	}
	r.registry.mu.Lock()
	r.connection.IP = addr.Unmap().String()
	r.registry.mu.Unlock()
}

func (r *Record) AddUpload(n int)   { r.add(n, 0) }
func (r *Record) AddDownload(n int) { r.add(0, n) }
func (r *Record) add(up, down int) {
	if r == nil || (up <= 0 && down <= 0) {
		return
	}
	registry := r.registry
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	r.meter.add(now, uint64(max(up, 0)), uint64(max(down, 0)))
	registry.meter.add(now, uint64(max(up, 0)), uint64(max(down, 0)))
}

func (r *Record) Finish(state string, err error) {
	if r == nil {
		return
	}
	registry := r.registry
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	if state == "" {
		state = "closed"
	}
	r.connection.State = state
	if err != nil {
		r.connection.Error = err.Error()
	}
	now := registry.now()
	r.connection.EndedAt = &now
	if !r.listed {
		registry.unlistedActive--
		return
	}
	delete(registry.active, r.connection.ID)
	registry.recent = append(registry.recent, r)
	if len(registry.recent) > registry.maxRecent {
		copy(registry.recent, registry.recent[1:])
		registry.recent[len(registry.recent)-1] = nil
		registry.recent = registry.recent[:registry.maxRecent]
	}
}

func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{Connections: []Connection{}, RateWindow: 2, SampledAt: time.Now()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	s := Snapshot{Connections: make([]Connection, 0, len(r.active)+len(r.recent)), Active: len(r.active) + r.unlistedActive,
		Total: r.total, Omitted: r.omitted, Upload: r.meter.up, Download: r.meter.down, SampledAt: now, RateWindow: 2}
	s.UploadRate, s.DownloadRate = r.meter.rates(now)
	appendRecord := func(record *Record) {
		c := record.connection
		end := now
		if c.EndedAt != nil {
			end = *c.EndedAt
			ended := end
			c.EndedAt = &ended
		}
		c.Duration = max(0, end.Sub(c.StartedAt).Seconds())
		c.Upload, c.Download = record.meter.up, record.meter.down
		if !record.finished {
			c.UploadRate, c.DownloadRate = record.meter.rates(now)
		}
		s.Connections = append(s.Connections, c)
	}
	for _, record := range r.active {
		appendRecord(record)
	}
	for _, record := range r.recent {
		appendRecord(record)
	}
	sort.Slice(s.Connections, func(i, j int) bool { return s.Connections[i].ID > s.Connections[j].ID })
	return s
}
