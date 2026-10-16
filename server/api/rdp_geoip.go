package api

import (
	"net/http"

	serverrdp "relayproxy/server/rdp"
)

// GeoIP administration is restricted to administrator accounts and is
// deliberately separate from the live RDP connection audit.
func (r *Router) handleRDPGeoIPStatus(w http.ResponseWriter, req *http.Request) {
	if r.geoIPUpdater == nil {
		writeJSON(w, http.StatusOK, serverrdp.GeoIPUpdateStatus{})
		return
	}
	writeJSON(w, http.StatusOK, r.geoIPUpdater.Status())
}

func (r *Router) handleRDPGeoIPUpdate(w http.ResponseWriter, req *http.Request) {
	if r.geoIPUpdater == nil {
		writeError(w, http.StatusConflict, "GeoIP 当前未启用；请先开启功能并重启 Server")
		return
	}
	if err := r.geoIPUpdater.Refresh(req.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "GeoIP 规则更新失败，已保留旧规则: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, r.geoIPUpdater.Status())
}
