package server

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/teamzuzu/lazyrepo/internal/index"
)

// HealthChecker reports overall poller health for the /healthz endpoint.
type HealthChecker interface {
	Healthy() bool
}

// Server serves the apt-facing HTTP endpoints: Packages metadata, .deb
// redirects to GitHub, and a health check.
type Server struct {
	idx       *index.Index
	health    HealthChecker
	mu        sync.Mutex
	hashes    map[string][]byte
	hashOrder []string
}

// New returns a Server backed by idx for package data and health for the
// /healthz endpoint.
func New(idx *index.Index, health HealthChecker) *Server {
	return &Server{idx: idx, health: health, hashes: make(map[string][]byte)}
}

// Handler returns the http.Handler for the whole service.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/Packages", s.handlePackages)
	mux.HandleFunc("/Packages.gz", s.handlePackagesGz)
	mux.HandleFunc("/Release", s.handleRelease)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/by-hash/SHA256/", s.handleByHash)
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

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	packages, compressed, data, err := s.idx.RenderSnapshot()
	if err != nil {
		http.Error(w, "failed to render Release", http.StatusInternalServerError)
		return
	}
	// Retain immutable files before publishing the Release that references them.
	s.mu.Lock()
	for _, file := range [][]byte{packages, compressed} {
		hash := fmt.Sprintf("%x", sha256.Sum256(file))
		if _, ok := s.hashes[hash]; !ok {
			s.hashes[hash] = file
			s.hashOrder = append(s.hashOrder, hash)
		}
	}
	// Bound memory while allowing clients to finish older Release downloads.
	for len(s.hashOrder) > 64 {
		delete(s.hashes, s.hashOrder[0])
		s.hashOrder = s.hashOrder[1:]
	}
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
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

// handleByHash serves the exact bytes advertised by a previously fetched Release.
func (s *Server) handleByHash(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/by-hash/SHA256/")
	s.mu.Lock()
	data, ok := s.hashes[hash]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable, no-transform")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data)
}
