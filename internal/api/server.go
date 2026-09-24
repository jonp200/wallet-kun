package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type Server struct {
	store walletStore
	mux   *http.ServeMux
}

type walletStore interface {
	Debit(string, int64) error
}

func New(st walletStore) *Server {
	server := &Server{store: st, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("POST /accounts/{id}/debits", server.debits)

	return server
}

func (s *Server) Handler() http.Handler {
	return logging(s.mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) debits(w http.ResponseWriter, r *http.Request) {
	panic("implement me")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write JSON response", "error", err)
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
