package gui

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (w *WebServer) fileShareRemoteAccessAllowed(rw http.ResponseWriter) bool {
	if !w.loopback && w.token == "" {
		http.Error(rw, "file sharing requires Web authentication on LAN listeners", http.StatusForbidden)
		return false
	}
	return true
}

func (w *WebServer) fileSharePeers(rw http.ResponseWriter, _ *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	peers, err := w.bridge.GetFileSharePeers()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	writeWebJSON(rw, peers)
}

func (w *WebServer) fileShareRemoteShares(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	shares, err := w.bridge.GetRemoteFileShares(r.URL.Query().Get("deviceId"))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	writeWebJSON(rw, shares)
}

func (w *WebServer) fileShareRemoteEntries(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	entries, err := w.bridge.GetRemoteFileEntries(
		r.URL.Query().Get("deviceId"), r.URL.Query().Get("shareId"),
		r.URL.Query().Get("path"),
	)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	writeWebJSON(rw, entries)
}

func (w *WebServer) startFileDownload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	var request struct {
		DeviceID string `json:"deviceId"`
		ShareID  string `json:"shareId"`
		Path     string `json:"path"`
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(rw, "invalid transfer request", http.StatusBadRequest)
		return
	}
	transfer, err := w.bridge.StartFileDownload(
		strings.TrimSpace(request.DeviceID), strings.TrimSpace(request.ShareID), request.Path,
	)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, transfer)
}

func (w *WebServer) listFileDownloads(rw http.ResponseWriter, _ *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	writeWebJSON(rw, w.bridge.GetFileTransfers())
}

func (w *WebServer) cancelFileDownload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	if err := w.bridge.CancelFileDownload(r.PathValue("id")); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, map[string]bool{"ok": true})
}

type stagedFileUploadRequest struct {
	DeviceID string `json:"deviceId"`
	ShareID  string `json:"shareId"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
}

func (w *WebServer) beginFileUpload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	var v stagedFileUploadRequest
	r.Body = http.MaxBytesReader(rw, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		http.Error(rw, "invalid upload request", http.StatusBadRequest)
		return
	}
	result, err := w.bridge.BeginFileUpload(v.DeviceID, v.ShareID, v.Path, v.Size)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, result)
}
func (w *WebServer) appendFileUpload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	var v struct {
		Chunk string `json:"chunk"`
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 384<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		http.Error(rw, "invalid upload chunk", http.StatusBadRequest)
		return
	}
	result, err := w.bridge.AppendFileUploadChunk(r.PathValue("id"), v.Chunk)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, result)
}
func (w *WebServer) commitFileUpload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	result, err := w.bridge.CommitFileUpload(r.PathValue("id"))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, result)
}
func (w *WebServer) discardFileUpload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	if err := w.bridge.DiscardFileUpload(r.PathValue("id")); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, map[string]bool{"ok": true})
}

func (w *WebServer) fileShareCandidates(rw http.ResponseWriter, _ *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	devices, err := w.bridge.GetFileShareCandidates()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	writeWebJSON(rw, devices)
}
