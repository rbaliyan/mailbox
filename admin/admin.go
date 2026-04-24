// Package admin provides an HTTP management surface for mailbox service operations.
// Mount Handler at any path prefix in your HTTP mux:
//
//	mux.Handle("/admin/", http.StripPrefix("/admin", admin.NewHandler(svc, admin.WithAuthFunc(myAuth))))
//
// Authentication is required: provide WithAuthFunc or WithAllowAll (for trusted environments).
package admin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/store"
)

// Handler exposes mailbox service management operations over HTTP.
type Handler struct {
	svc  mailbox.Service
	opts options
	mux  *http.ServeMux
}

// NewHandler creates a new Handler for the given service.
// Returns an error if svc is nil, or if neither WithAuthFunc nor WithAllowAll is provided.
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

	if o.authFunc == nil && !o.allowAll {
		return nil, errors.New("admin: authentication is required; provide WithAuthFunc or WithAllowAll")
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
	result, err := h.svc.RunQuotaEnforcement(r.Context())
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
	code := errorCode(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// errorCode maps sentinel errors to appropriate HTTP status codes.
func errorCode(err error) int {
	switch {
	case errors.Is(err, mailbox.ErrNotConnected):
		return http.StatusServiceUnavailable // 503
	case errors.Is(err, mailbox.ErrQuotaUserListerNotConfigured):
		return http.StatusNotImplemented // 501
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound // 404
	default:
		return http.StatusInternalServerError // 500
	}
}
