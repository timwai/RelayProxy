package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"relayproxy/internal/config"
	"relayproxy/internal/tunnel"
	"relayproxy/internal/webui"
	"relayproxy/server/repository"
	"relayproxy/server/service"
	"relayproxy/server/session"
	"relayproxy/server/web"
)

type contextKey string

const userContextKey contextKey = "user_id"
const principalContextKey contextKey = "principal"

type Router struct {
	authService                  *service.AuthService
	deviceService                *service.DeviceService
	sessions                     *session.Manager
	db                           *repository.DB
	mux                          *http.ServeMux
	settings                     *config.ServerSettings
	onDeviceRevoked              func(string)
	onDeviceAuthorizationChanged func(string)
	onRDPIngressChanged          func(string)
	onRDPIngressReload           func(string) error
	onRDPIngressStatus           func(string) RDPIngressRuntimeStatus
	rdpIngressEnabled            func() bool
	rdpIngressPortStart          int
	rdpIngressPortEnd            int
	serverInfo                   ServerInfo
}

type RouterOption func(*Router)

// RDPIngressRuntimeStatus describes the sockets currently owned by the
// process. It is deliberately separate from the persisted allocation record:
// an enabled database row is not proof that both listeners were bound.
type RDPIngressRuntimeStatus struct {
	TCPListening bool
	UDPListening bool
	ActiveUDP    int
}

func NewRouter(authService *service.AuthService, deviceService *service.DeviceService, sessions *session.Manager, db *repository.DB, options ...RouterOption) *Router {
	r := &Router{
		authService:         authService,
		deviceService:       deviceService,
		sessions:            sessions,
		db:                  db,
		mux:                 http.NewServeMux(),
		rdpIngressPortStart: 33900,
		rdpIngressPortEnd:   34000,
	}
	for _, option := range options {
		option(r)
	}
	r.registerRoutes()
	return r
}

// WithDeviceRevoked lets runtime services tear down short-lived direct paths
// before the session manager closes the authenticated tunnel.
func WithDeviceRevoked(fn func(string)) RouterOption {
	return func(r *Router) { r.onDeviceRevoked = fn }
}

// WithDeviceAuthorizationChanged lets runtime services tear down direct RDP
// paths and reconcile public ingress after a device's capability grant set is
// replaced. The authenticated tunnel is invalidated by the handler itself.
func WithDeviceAuthorizationChanged(fn func(string)) RouterOption {
	return func(r *Router) { r.onDeviceAuthorizationChanged = fn }
}

func WithRDPIngressChanged(fn func(string)) RouterOption {
	return func(r *Router) { r.onRDPIngressChanged = fn }
}

// WithRDPIngressReload wires the runtime listener reconciliation into the
// administrative allocation APIs. Returning an error is important: a record
// must not be reported as successfully created when its TCP/UDP sockets could
// not be bound.
func WithRDPIngressReload(fn func(string) error) RouterOption {
	return func(r *Router) { r.onRDPIngressReload = fn }
}

// WithRDPIngressStatus exposes live listener state to the admin API without
// coupling the API package to the concrete ingress manager implementation.
func WithRDPIngressStatus(fn func(string) RDPIngressRuntimeStatus) RouterOption {
	return func(r *Router) { r.onRDPIngressStatus = fn }
}

// WithRDPIngressEnabled lets the API reject allocations while the process
// level public-ingress switch is disabled. This avoids persisting an enabled
// allocation that can never have a listener in the current process.
func WithRDPIngressEnabled(fn func() bool) RouterOption {
	return func(r *Router) { r.rdpIngressEnabled = fn }
}

func WithRDPIngressPortRange(start, end int) RouterOption {
	return func(r *Router) { r.rdpIngressPortStart, r.rdpIngressPortEnd = start, end }
}

func (r *Router) refreshRDPIngress(id string) error {
	if r.onRDPIngressReload != nil {
		// A disabled manager has no sockets to reconcile. Disable/delete are
		// still valid administrative operations in that state.
		if r.rdpIngressEnabled != nil && !r.rdpIngressEnabled() {
			return nil
		}
		return r.onRDPIngressReload(id)
	}
	if r.onRDPIngressChanged != nil {
		r.onRDPIngressChanged(id)
	}
	return nil
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	if strings.HasPrefix(req.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions && !validMutationOrigin(req) {
			writeError(w, http.StatusForbidden, "请从当前管理页面提交操作")
			return
		}
	}
	r.mux.ServeHTTP(w, req)
}

// Compare the browser origin with the requested host, including its port.
// Missing Origin is allowed for the native agent and command-line clients.
// Forwarded headers are not trusted to enable TLS cookies on a plain listener.
func validMutationOrigin(req *http.Request) bool {
	if req.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := req.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		(req.TLS == nil || u.Scheme == "https") && strings.EqualFold(u.Host, req.Host) &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func (r *Router) registerRoutes() {
	// Shared Agent/Server visual primitives. Product pages keep their own data
	// behavior while consuming one design system and theme implementation.
	r.mux.Handle("GET /ui/", http.StripPrefix("/ui/", webui.Handler()))

	// Health check
	r.mux.HandleFunc("GET /health", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.mux.HandleFunc("GET /ready", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Auth APIs
	r.mux.HandleFunc("POST /api/v1/auth/login", r.handleLogin)
	r.mux.HandleFunc("POST /api/v1/auth/logout", r.handleLogout)
	r.mux.HandleFunc("GET /api/v1/auth/me", r.requireAuth(r.handleMe))
	r.mux.HandleFunc("PUT /api/v1/auth/password", r.requireAuth(r.handleChangePassword))

	// Device APIs
	r.mux.HandleFunc("GET /api/v1/devices", r.requireAuth(r.handleListDevices))
	r.mux.HandleFunc("GET /api/v1/enrollments", r.requireAuth(r.requireAdmin(r.handleListEnrollments)))
	r.mux.HandleFunc("POST /api/v1/enrollments/{id}/approve", r.requireAuth(r.requireAdmin(r.handleApproveEnrollment)))
	r.mux.HandleFunc("POST /api/v1/enrollments/{id}/reject", r.requireAuth(r.requireAdmin(r.handleRejectEnrollment)))
	r.mux.HandleFunc("PUT /api/v1/devices/{id}/capabilities", r.requireAuth(r.requireAdmin(r.handleUpdateDeviceCapabilities)))
	r.mux.HandleFunc("POST /api/v1/devices/{id}/revoke", r.requireAuth(r.requireAdmin(r.handleRevokeDevice)))
	r.mux.HandleFunc("DELETE /api/v1/devices/{id}", r.requireAuth(r.requireAdmin(r.handleDeleteDevice)))

	// Exit APIs
	r.mux.HandleFunc("GET /api/v1/exits", r.requireAuth(r.handleListExits))
	// RDP APIs. The controller/target matrix is server-owned; this endpoint
	// only exposes targets already granted to the authenticated user's devices.
	r.mux.HandleFunc("GET /api/v1/rdp/targets", r.requireAuth(r.handleListRDPTargets))
	r.mux.HandleFunc("GET /api/v1/rdp/ingress", r.requireAuth(r.handleListRDPIngress))
	r.mux.HandleFunc("POST /api/v1/rdp/ingress", r.requireAuth(r.requireAdmin(r.handleCreateRDPIngress)))
	r.mux.HandleFunc("POST /api/v1/rdp/ingress/{id}/enable", r.requireAuth(r.requireAdmin(r.handleEnableRDPIngress)))
	r.mux.HandleFunc("POST /api/v1/rdp/ingress/{id}/disable", r.requireAuth(r.requireAdmin(r.handleDisableRDPIngress)))
	r.mux.HandleFunc("DELETE /api/v1/rdp/ingress/{id}", r.requireAuth(r.requireAdmin(r.handleDeleteRDPIngress)))

	// Dashboard & Sessions
	r.mux.HandleFunc("GET /api/v1/dashboard", r.requireAuth(r.handleDashboard))
	r.mux.HandleFunc("GET /api/v1/sessions/active", r.requireAuth(r.handleActiveSessions))
	r.mux.HandleFunc("GET /api/v1/server/config", r.requireAuth(r.requireAdmin(r.handleGetServerConfig)))
	r.mux.HandleFunc("PUT /api/v1/server/config", r.requireAuth(r.requireAdmin(r.handleSaveServerConfig)))

	// Static Web UI Handler
	webHandler := web.Handler()
	r.mux.HandleFunc("GET /", func(w http.ResponseWriter, req *http.Request) {
		// If requesting an API prefix that doesn't exist, return 404 json
		if strings.HasPrefix(req.URL.Path, "/api/") {
			http.NotFound(w, req)
			return
		}
		webHandler.ServeHTTP(w, req)
	})
}

// Auth Middleware
func (r *Router) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		cookie, err := requestSessionCookie(req)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		userID, ok := r.authService.Authenticate(cookie.Value)
		if !ok || userID == "" {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}

		user, err := r.db.GetUserByID(userID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && user.Status != "active") {
			r.authService.Logout(cookie.Value)
			writeError(w, http.StatusUnauthorized, "account is not active")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load account")
			return
		}
		if user.Role != "admin" && user.Role != "user" {
			writeError(w, http.StatusForbidden, "account role is not authorized")
			return
		}
		ctx := context.WithValue(req.Context(), userContextKey, userID)
		ctx = context.WithValue(ctx, principalContextKey, user)
		next(w, req.WithContext(ctx))
	}
}

func (r *Router) handleLogin(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	clientIP := remoteIP(req)
	token, user, err := r.authService.LoginFrom(body.Username, body.Password, clientIP)
	if err != nil {
		if errors.Is(err, service.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if errors.Is(err, service.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "登录失败，请稍后重试")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(req),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   req.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 7,
	})

	// Do not return the session token in the JSON body (N10) — Cookie-only.
	writeJSON(w, http.StatusOK, map[string]any{
		"user": user,
	})
}

func (r *Router) handleLogout(w http.ResponseWriter, req *http.Request) {
	for _, name := range []string{"relay_session", "relay_session_http"} {
		if cookie, err := req.Cookie(name); err == nil {
			r.authService.Logout(cookie.Value)
		}
	}
	clearSessionCookies(w, req)
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

func clearSessionCookies(w http.ResponseWriter, req *http.Request) {
	for _, name := range []string{"relay_session", "relay_session_http"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true,
			Secure: req.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
}

func sessionCookieName(req *http.Request) string {
	if req.TLS != nil {
		return "relay_session"
	}
	// A previous HTTPS Secure cookie cannot be overwritten from HTTP.
	return "relay_session_http"
}

func requestSessionCookie(req *http.Request) (*http.Cookie, error) {
	cookie, err := req.Cookie(sessionCookieName(req))
	if err != nil && req.TLS == nil {
		return req.Cookie("relay_session") // compatibility with existing API clients
	}
	return cookie, err
}

func (r *Router) handleMe(w http.ResponseWriter, req *http.Request) {
	user, _ := req.Context().Value(principalContextKey).(*repository.User)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// requestOwner is only used behind requireAuth; the empty scope is admin-only.
func requestOwner(req *http.Request) string {
	user := req.Context().Value(principalContextKey).(*repository.User)
	if user.Role == "admin" {
		return ""
	}
	return user.ID
}

func (r *Router) requireDeviceAccess(w http.ResponseWriter, req *http.Request) bool {
	owner, err := r.db.GetDeviceOwnerUserID(req.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && requestOwner(req) != "" && owner != requestOwner(req)) {
		// Do not reveal whether another user's device exists.
		writeError(w, http.StatusNotFound, "device not found")
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize device")
		return false
	}
	return true
}

func (r *Router) visibleSessions(req *http.Request) ([]*session.DeviceSession, error) {
	all := r.sessions.List()
	owner := requestOwner(req)
	if owner == "" {
		return all, nil
	}
	devices, err := r.db.ListDevicesForOwner(owner)
	if err != nil {
		return nil, err
	}
	owned := make(map[string]bool, len(devices))
	for _, device := range devices {
		owned[device.ID] = true
	}
	visible := make([]*session.DeviceSession, 0, len(devices))
	for _, sess := range all {
		if owned[sess.DeviceID] {
			visible = append(visible, sess)
		}
	}
	return visible, nil
}

func (r *Router) handleDashboard(w http.ResponseWriter, req *http.Request) {
	devices, err := r.visibleSessions(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load devices")
		return
	}
	var exits []*session.DeviceSession

	var totalConns int64
	for _, d := range devices {
		totalConns += d.ActiveStreams.Load()
		if d.IsExit() {
			exits = append(exits, d)
		}
	}

	// Today traffic from audit table (N7), not in-memory counters
	todayUp, todayDown, err := r.db.SumTodayTrafficForOwner(requestOwner(req))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	exitInfos := make([]map[string]any, 0, len(exits))
	for _, e := range exits {
		exitInfos = append(exitInfos, map[string]any{
			"deviceId":      e.DeviceID,
			"deviceName":    e.DeviceName,
			"transport":     string(e.Transport),
			"activeStreams": e.ActiveStreams.Load(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"onlineDevices":     len(devices),
		"onlineExits":       len(exits),
		"activeConnections": totalConns,
		"todayUpload":       todayUp,
		"todayDownload":     todayDown,
		"exitNodes":         exitInfos,
	})
}

func (r *Router) handleListDevices(w http.ResponseWriter, req *http.Request) {
	dbDevices, err := r.db.ListDevicesForOwner(requestOwner(req))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	res := make([]map[string]any, 0, len(dbDevices))
	for _, d := range dbDevices {
		online := false
		var transport string
		rdpUDPReady := false
		if sess, ok := r.sessions.Get(d.ID); ok {
			online = true
			transport = string(sess.Transport)
			rdpUDPReady = tunnel.PeerSupportsDatagrams(sess.Tunnel)
		}
		status := "offline"
		if online {
			status = "online"
		}

		res = append(res, map[string]any{
			"id":                    d.ID,
			"name":                  d.Name,
			"deviceMode":            d.DeviceMode,
			"platform":              d.Platform,
			"arch":                  d.Arch,
			"clientVersion":         d.ClientVersion,
			"status":                status,
			"approvalState":         d.ApprovalState,
			"requestedCapabilities": d.RequestedCapabilities,
			"approvedCapabilities":  d.ApprovedCapabilities,
			"transport":             transport,
			"rdpUdpReady":           rdpUDPReady,
			"lastSeenAt":            d.LastSeenAt,
		})
	}

	writeJSON(w, http.StatusOK, res)
}

func (r *Router) handleListEnrollments(w http.ResponseWriter, req *http.Request) {
	state := strings.TrimSpace(req.URL.Query().Get("state"))
	if state == "" {
		state = repository.EnrollmentPending
	}
	requests, err := r.db.ListEnrollmentRequests(state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list enrollment requests")
		return
	}
	writeJSON(w, http.StatusOK, requests)
}

func (r *Router) handleApproveEnrollment(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if req.ContentLength != 0 {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	userID, _ := req.Context().Value(userContextKey).(string)
	device, err := r.db.ApproveEnrollment(req.PathValue("id"), userID, body.Capabilities)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "pending enrollment not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func (r *Router) handleRejectEnrollment(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if req.ContentLength != 0 {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	userID, _ := req.Context().Value(userContextKey).(string)
	if err := r.db.RejectEnrollment(req.PathValue("id"), userID, strings.TrimSpace(body.Reason)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "pending enrollment not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "state": repository.EnrollmentRejected})
}

func (r *Router) handleRevokeDevice(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if req.ContentLength != 0 {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	userID, _ := req.Context().Value(userContextKey).(string)
	id := req.PathValue("id")
	err := r.sessions.ChangeDeviceAuthorization(id, true, func() error {
		return r.db.RevokeDevice(id, userID, strings.TrimSpace(body.Reason))
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Runtime teardown can perform network I/O and listener reconciliation.
	// Keep it outside the session authorization gate so a slow peer cannot
	// block unrelated authenticated registrations or admin mutations.
	if r.onDeviceRevoked != nil {
		r.onDeviceRevoked(id)
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": repository.EnrollmentRevoked})
}

func (r *Router) handleDeleteDevice(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	actor, _ := req.Context().Value(userContextKey).(string)
	err := r.sessions.ChangeDeviceAuthorization(id, true, func() error {
		return r.db.DeleteDevice(id, actor)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "device not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// Reconcile runtime services after the authorization snapshot has been
	// invalidated. Delete has the same teardown needs as revoke, plus database
	// cascades remove any RDP grants/services owned by this device.
	if r.onDeviceRevoked != nil {
		r.onDeviceRevoked(id)
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": "deleted"})
}

func (r *Router) handleUpdateDeviceCapabilities(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	id := req.PathValue("id")
	var device *repository.Device
	err := r.sessions.ChangeDeviceAuthorization(id, true, func() error {
		var err error
		device, err = r.db.UpdateDeviceCapabilities(id, actor, body.Capabilities)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "approved device not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// The old authenticated session has already been invalidated atomically.
	// Reconcile direct paths and public ingress after releasing the gate.
	if r.onDeviceAuthorizationChanged != nil {
		r.onDeviceAuthorizationChanged(id)
	}
	writeJSON(w, http.StatusOK, device)
}

func (r *Router) handleListExits(w http.ResponseWriter, req *http.Request) {
	exits, err := r.visibleSessions(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load exits")
		return
	}
	res := make([]map[string]any, 0, len(exits))
	for _, e := range exits {
		if !e.IsExit() {
			continue
		}
		res = append(res, map[string]any{
			"deviceId":      e.DeviceID,
			"deviceName":    e.DeviceName,
			"transport":     string(e.Transport),
			"activeStreams": e.ActiveStreams.Load(),
			"online":        true,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

func (r *Router) handleListRDPTargets(w http.ResponseWriter, req *http.Request) {
	controllerID := strings.TrimSpace(req.URL.Query().Get("controllerId"))
	if controllerID != "" {
		if !r.deviceVisibleToRequest(req, controllerID) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		devices, err := r.db.ListDevicesForOwner(requestOwner(req))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load controller")
			return
		}
		controllerApproved := false
		for _, device := range devices {
			if device.ID == controllerID && device.ApprovalState == repository.EnrollmentApproved && containsString(device.ApprovedCapabilities, "rdp.controller") {
				controllerApproved = true
				break
			}
		}
		if !controllerApproved {
			writeError(w, http.StatusNotFound, "RDP controller not found")
			return
		}
		targets, err := r.db.ListRDPTargetsForController(controllerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list RDP targets")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"controllerId": controllerID, "targets": targets})
		return
	}

	devices, err := r.db.ListDevicesForOwner(requestOwner(req))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP controllers")
		return
	}
	result := make([]map[string]any, 0)
	for _, device := range devices {
		if !containsString(device.ApprovedCapabilities, "rdp.controller") {
			continue
		}
		targets, err := r.db.ListRDPTargetsForController(device.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list RDP targets")
			return
		}
		result = append(result, map[string]any{"controllerId": device.ID, "controllerName": device.Name, "targets": targets})
	}
	writeJSON(w, http.StatusOK, result)
}

func (r *Router) handleListRDPIngress(w http.ResponseWriter, req *http.Request) {
	items, err := r.db.ListRDPIngress(requestOwner(req))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP ingress")
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		runtime := RDPIngressRuntimeStatus{}
		if r.onRDPIngressStatus != nil {
			runtime = r.onRDPIngressStatus(item.ID)
		}
		targetOnline, targetTransport := false, ""
		udpReady := false
		if r.sessions != nil {
			if target, ok := r.sessions.Get(item.TargetDeviceID); ok && target != nil {
				targetOnline = true
				targetTransport = string(target.Transport)
				udpReady = tunnel.PeerSupportsDatagrams(target.Tunnel)
			}
		}
		udpEnabled := item.Status == "enabled" && runtime.UDPListening && udpReady
		udpReason := "入口已停用"
		if item.Status == "enabled" {
			switch {
			case !runtime.TCPListening || !runtime.UDPListening:
				udpReason = "监听器未建立"
			case !targetOnline:
				udpReason = "目标设备离线"
			case !udpReady:
				udpReason = "目标隧道不是 QUIC Datagram"
			default:
				udpReason = "UDP 已启用"
			}
		}
		result = append(result, map[string]any{
			"id": item.ID, "serviceId": item.ServiceID, "targetDeviceId": item.TargetDeviceID,
			"targetName": item.TargetName, "listenPort": item.ListenPort, "status": item.Status,
			"sourceCidrs": item.SourceCIDRs, "expiresAt": item.ExpiresAt,
			"rateLimitPerMinute": item.RateLimitPerMin, "createdBy": item.CreatedBy,
			"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
			"targetOnline": targetOnline, "targetTransport": targetTransport,
			"tcpListening": runtime.TCPListening, "udpListening": runtime.UDPListening,
			"udpReady": udpReady, "udpEnabled": udpEnabled, "udpReason": udpReason,
			"activeUdp": runtime.ActiveUDP,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (r *Router) handleCreateRDPIngress(w http.ResponseWriter, req *http.Request) {
	var body struct {
		TargetDeviceID string   `json:"targetDeviceId"`
		ListenPort     int      `json:"listenPort"`
		SourceCIDRs    []string `json:"sourceCidrs"`
		ExpiresAt      string   `json:"expiresAt"`
		RateLimit      int      `json:"rateLimitPerMinute"`
		Enabled        *bool    `json:"enabled"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if r.rdpIngressEnabled != nil && !r.rdpIngressEnabled() {
		writeError(w, http.StatusConflict, "RDP 公网入口服务未启用，请设置 rdp.ingress.enabled=true 并重启服务")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	var expires *time.Time
	if strings.TrimSpace(body.ExpiresAt) != "" {
		value, err := time.Parse(time.RFC3339, strings.TrimSpace(body.ExpiresAt))
		if err != nil || !value.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "expiresAt must be a future RFC3339 timestamp")
			return
		}
		expires = &value
	}
	cleanCIDRs := make([]string, 0, len(body.SourceCIDRs))
	for _, raw := range body.SourceCIDRs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(raw); err != nil {
			writeError(w, http.StatusBadRequest, "sourceCidrs 包含无效 CIDR")
			return
		}
		cleanCIDRs = append(cleanCIDRs, raw)
	}
	body.SourceCIDRs = cleanCIDRs
	if r.rdpIngressPortStart == 0 {
		r.rdpIngressPortStart, r.rdpIngressPortEnd = 33900, 34000
	}
	item, err := r.db.CreateRDPIngress(body.TargetDeviceID, actor, body.ListenPort, body.SourceCIDRs, expires, body.RateLimit,
		r.rdpIngressPortStart, r.rdpIngressPortEnd)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "RDP target not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if body.Enabled != nil && !*body.Enabled {
		if err := r.db.SetRDPIngressStatus(item.ID, requestOwner(req), false); err != nil {
			_ = r.db.DeleteRDPIngress(item.ID, "")
			writeError(w, http.StatusInternalServerError, "创建 RDP 公网入口后无法设置初始状态")
			return
		}
		item.Status = "disabled"
	}
	if item.Status == "enabled" {
		if err := r.refreshRDPIngress(item.ID); err != nil {
			rollbackErr := r.db.DeleteRDPIngress(item.ID, "")
			// A reload can have opened this allocation before another existing
			// allocation failed. Reconcile once more so rollback also closes any
			// transient listener immediately.
			_ = r.refreshRDPIngress(item.ID)
			message := "RDP 公网入口监听失败: " + err.Error()
			if rollbackErr != nil {
				message += "; 入口记录回滚失败，请手动删除: " + rollbackErr.Error()
			}
			writeError(w, http.StatusServiceUnavailable, message)
			return
		}
	}
	writeJSON(w, http.StatusCreated, item)
}

func (r *Router) handleEnableRDPIngress(w http.ResponseWriter, req *http.Request) {
	if r.rdpIngressEnabled != nil && !r.rdpIngressEnabled() {
		writeError(w, http.StatusConflict, "RDP 公网入口服务未启用，请设置 rdp.ingress.enabled=true 并重启服务")
		return
	}
	if err := r.db.SetRDPIngressStatus(req.PathValue("id"), requestOwner(req), true); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "RDP ingress not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.refreshRDPIngress(req.PathValue("id")); err != nil {
		_ = r.db.SetRDPIngressStatus(req.PathValue("id"), requestOwner(req), false)
		_ = r.refreshRDPIngress(req.PathValue("id"))
		writeError(w, http.StatusServiceUnavailable, "RDP 公网入口监听失败，入口已恢复为停用: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "status": "enabled"})
}

func (r *Router) handleDisableRDPIngress(w http.ResponseWriter, req *http.Request) {
	if err := r.db.SetRDPIngressStatus(req.PathValue("id"), requestOwner(req), false); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "RDP ingress not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.refreshRDPIngress(req.PathValue("id")); err != nil {
		writeError(w, http.StatusServiceUnavailable, "RDP 公网入口停止监听失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "status": "disabled"})
}

func (r *Router) handleDeleteRDPIngress(w http.ResponseWriter, req *http.Request) {
	if err := r.db.DeleteRDPIngress(req.PathValue("id"), requestOwner(req)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "RDP ingress not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.refreshRDPIngress(req.PathValue("id")); err != nil {
		writeError(w, http.StatusServiceUnavailable, "RDP 公网入口删除后刷新监听失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "status": "deleted"})
}

func (r *Router) deviceVisibleToRequest(req *http.Request, deviceID string) bool {
	owner, err := r.db.GetDeviceOwnerUserID(deviceID)
	if err != nil {
		return false
	}
	requester := requestOwner(req)
	return requester == "" || requester == owner
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (r *Router) handleActiveSessions(w http.ResponseWriter, req *http.Request) {
	devices, err := r.visibleSessions(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load sessions")
		return
	}
	res := make([]map[string]any, 0)
	for _, d := range devices {
		if d.ActiveStreams.Load() > 0 {
			exitID := ""
			if ptr := d.ActiveExitID.Load(); ptr != nil {
				exitID = *ptr
			}
			res = append(res, map[string]any{
				"clientDeviceId":   d.DeviceID,
				"clientDeviceName": d.DeviceName,
				"exitDeviceId":     exitID,
				"mode":             d.Mode,
				"transport":        string(d.Transport),
				"activeStreams":    d.ActiveStreams.Load(),
				"bytesUp":          d.BytesUp.Load(),
				"bytesDown":        d.BytesDown.Load(),
			})
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, val any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(val)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{
		"error": msg,
		"time":  time.Now().Format(time.RFC3339),
	})
}

// remoteIP returns the direct peer IP. X-Forwarded-For is intentionally ignored
// unless a trusted-proxy layer is configured (P2-6).
func remoteIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}
