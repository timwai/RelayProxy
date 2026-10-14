package gui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"relayproxy/agent/fileshare"
)

// This is a management API behind the Agent's existing Web authorization.
// Remote peers MUST NOT be routed to this handler.
func (w *WebServer) fileShares(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := w.bridge.GetFileShares()
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		writeWebJSON(rw, state)
	case http.MethodPut:
		var state fileshare.State
		r.Body = http.MaxBytesReader(rw, r.Body, 256<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&state); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			http.Error(rw, "unexpected trailing JSON", http.StatusBadRequest)
			return
		}
		saved, err := w.bridge.SaveFileShares(state)
		if errors.Is(err, fileshare.ErrConflict) {
			http.Error(rw, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		writeWebJSON(rw, saved)
	default:
		rw.Header().Set("Allow", "GET, PUT")
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
	}
}
