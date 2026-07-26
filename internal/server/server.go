package server

import (
	"net/http"
	"strings"

	"github.com/teamzuzu/lazyrepo/internal/index"
)

// HealthChecker reports overall poller health for the /healthz endpoint.
type HealthChecker interface {
	Healthy() bool
}

// Server serves the apt-facing HTTP endpoints: Packages metadata, .deb
// redirects to GitHub, and a health check.
type Server struct {
	idx    *index.Index
	health HealthChecker
}

// New returns a Server backed by idx for package data and health for the
// /healthz endpoint.
func New(idx *index.Index, health HealthChecker) *Server {
	return &Server{idx: idx, health: health}
}

// Handler returns the http.Handler for the whole service.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/Packages", s.handlePackages)
	mux.HandleFunc("/Packages.gz", s.handlePackagesGz)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleDebRedirect)
	return mux
}

func (s *Server) handlePackages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(s.idx.RenderPackages())
}

func (s *Server) handlePackagesGz(w http.ResponseWriter, r *http.Request) {
	data, err := s.idx.RenderPackagesGz()
	if err != nil {
		http.Error(w, "failed to render Packages.gz", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Write(data)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !s.health.Healthy() {
		http.Error(w, "unhealthy", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleDebRedirect(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimPrefix(r.URL.Path, "/")
	entry, ok := s.idx.Lookup(filename)
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, entry.DownloadURL, http.StatusFound)
}
