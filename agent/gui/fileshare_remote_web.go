package gui

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

func (w *WebServer) resumeFileDownload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	if err := w.bridge.ResumeFileDownload(r.PathValue("id")); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, map[string]bool{"ok": true})
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

func (w *WebServer) fileDirectStatus(rw http.ResponseWriter, _ *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	writeWebJSON(rw, w.bridge.GetFileDirectStatus())
}

// A browser can export a finished, hash-verified default-root download.
// This endpoint never accepts a filesystem path and never serves a partial.
func (w *WebServer) serveCompletedFileDownload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileShareRemoteAccessAllowed(rw) {
		return
	}
	file, name, err := w.bridge.OpenCompletedFileDownload(r.PathValue("id"))
	if err != nil {
		http.Error(rw, "completed download unavailable", http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(rw, "download not a regular file", http.StatusNotFound)
		return
	}
	rw.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	rw.Header().Set("Content-Type", "application/octet-stream")
	rw.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	rw.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(rw, file)
}

// System dialogs affect the HOST filesystem, so disallow invoking them from
// LAN/external browsers even when the management API has a valid bearer token.
// Browser Save As through /api/file-transfers/{id}/file is client-side.
func (w *WebServer) fileDialogLocalOnly(rw http.ResponseWriter, r *http.Request) bool {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if !w.loopback || err != nil || !webLoopbackHost(remote) {
		http.Error(rw, "native file picker requires the Agent's loopback UI", http.StatusForbidden)
		return false
	}
	return true
}

func (w *WebServer) chooseLocalShareFolder(rw http.ResponseWriter, r *http.Request) {
	if !w.fileDialogLocalOnly(rw, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	selected, err := nativeChooseShareFolder(ctx)
	if err != nil {
		http.Error(rw, "system directory dialog unavailable", http.StatusNotImplemented)
		return
	}
	writeWebJSON(rw, map[string]string{"path": selected})
}

func (w *WebServer) chooseLocalFileDownload(rw http.ResponseWriter, r *http.Request) {
	if !w.fileDialogLocalOnly(rw, r) {
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
		http.Error(rw, "invalid download request", http.StatusBadRequest)
		return
	}
	if request.DeviceID == "" || request.ShareID == "" || request.Path == "" ||
		request.Path == "." || strings.HasPrefix(request.Path, "/") ||
		strings.ContainsAny(request.Path, "\\:") || !fs.ValidPath(request.Path) {
		http.Error(rw, "invalid remote file selection", http.StatusBadRequest)
		return
	}
	name := path.Base(request.Path)
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	destination, err := nativeChooseDownloadPath(ctx, name)
	if err != nil {
		http.Error(rw, "system Save As dialog unavailable", http.StatusNotImplemented)
		return
	}
	if destination == "" {
		writeWebJSON(rw, map[string]string{"cancelled": "true"})
		return
	}
	if !filepath.IsAbs(destination) {
		http.Error(rw, "invalid save path", http.StatusBadRequest)
		return
	}
	transfer, err := w.bridge.StartFileDownloadTo(request.DeviceID, request.ShareID, request.Path, destination)
	if err != nil {
		http.Error(rw, "unable to start file transfer", http.StatusBadRequest)
		return
	}
	writeWebJSON(rw, transfer)
}
