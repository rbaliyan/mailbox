// Package admin provides an HTTP management surface for mailbox service operations.
// Mount Handler at any path prefix in your HTTP mux:
//
//	mux.Handle("/admin/", http.StripPrefix("/admin", admin.NewHandler(svc)))
package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rbaliyan/mailbox"
)

// Handler exposes mailbox service management operations over HTTP.
type Handler struct {
	svc  mailbox.Service
	opts options
	mux  *http.ServeMux
}

// NewHandler creates a new Handler for the given service.
// Returns an error if svc is nil.
func NewHandler(svc mailbox.Service, opts ...Option) (*Handler, error) {
	if svc == nil {
		return nil, errors.New("admin: service must not be nil")
	}

	o := options{
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(&o)
	}

	h := &Handler{
		svc:  svc,
		opts: o,
		mux:  http.NewServeMux(),
	}

	h.mux.HandleFunc("GET /health", h.handleHealth)
	h.mux.HandleFunc("POST /cleanup/trash", h.handleCleanupTrash)
	h.mux.HandleFunc("POST /cleanup/expired", h.handleCleanupExpired)
	h.mux.HandleFunc("POST /quotas/enforce", h.handleEnforceQuotas)

	return h, nil
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.opts.authFunc != nil && !h.opts.authFunc(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	connected := h.svc.IsConnected()
	status := "ok"
	if !connected {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    status,
		"connected": connected,
	})
}

func (h *Handler) handleCleanupTrash(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.CleanupTrash(r.Context())
	if err != nil {
		h.opts.logger.Error("cleanup trash failed", "error", err)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleCleanupExpired(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.CleanupExpiredMessages(r.Context())
	if err != nil {
		h.opts.logger.Error("cleanup expired messages failed", "error", err)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleEnforceQuotas(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.opts.logger.Error("enforce quotas: decode request body failed", "error", err)
		writeError(w, err)
		return
	}

	result, err := h.svc.EnforceQuotas(r.Context(), body.UserIDs)
	if err != nil {
		h.opts.logger.Error("enforce quotas failed", "error", err)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
