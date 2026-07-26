package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teamzuzu/lazyrepo/internal/index"
)

type fakeHealth struct{ healthy bool }

func (f fakeHealth) Healthy() bool { return f.healthy }

func newTestIndex() *index.Index {
	idx := index.New()
	idx.Add(index.PackageEntry{
		Key:          "sha256:abc",
		Package:      "pve-microvm",
		Version:      "0.3.21-1",
		Architecture: "all",
		Filename:     "pve-microvm_0.3.21-1_all.deb",
		Size:         13035136,
		SHA256:       "abc",
		DownloadURL:  "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/pve-microvm_0.3.21-1_all.deb",
	})
	return idx
}

func TestHandlePackages(t *testing.T) {
	s := New(newTestIndex(), fakeHealth{healthy: true})
	req := httptest.NewRequest(http.MethodGet, "/Packages", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Package: pve-microvm\n") {
		t.Errorf("body missing expected stanza, got:\n%s", rec.Body.String())
	}
}

func TestHandlePackagesGz(t *testing.T) {
	s := New(newTestIndex(), fakeHealth{healthy: true})
	req := httptest.NewRequest(http.MethodGet, "/Packages.gz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	r, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading decompressed body: %v", err)
	}
	if !strings.Contains(string(body), "Package: pve-microvm\n") {
		t.Errorf("decompressed body missing expected stanza, got:\n%s", body)
	}
}

func TestHandleDebRedirect_Found(t *testing.T) {
	s := New(newTestIndex(), fakeHealth{healthy: true})
	req := httptest.NewRequest(http.MethodGet, "/pve-microvm_0.3.21-1_all.deb", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	want := "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/pve-microvm_0.3.21-1_all.deb"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleDebRedirect_DotPrefixedFilename exercises the exact request path
// a real apt client issues: RenderPackages emits Filename: ./<name> (a path
// relative to the repo root), so apt requests /./<name>.deb, not /<name>.deb.
// Go's http.ServeMux path-cleans that to /<name>.deb and issues a 301 before
// handleDebRedirect ever sees the bare name; this test follows that 301 hop
// (but not the subsequent 302 to GitHub) to confirm the whole round trip
// behaves as apt would observe it.
func TestHandleDebRedirect_DotPrefixedFilename(t *testing.T) {
	s := New(newTestIndex(), fakeHealth{healthy: true})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow the mux's 301 path-cleaning redirect (via has length 1
			// at that point), but stop before following the subsequent 302
			// to GitHub (via has length 2 at that point) so we can assert
			// on it directly instead of actually fetching from GitHub.
			if len(via) >= 2 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	resp, err := client.Get(ts.URL + "/./pve-microvm_0.3.21-1_all.deb")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 (after following the mux's path-cleaning 301)", resp.StatusCode)
	}
	want := "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/pve-microvm_0.3.21-1_all.deb"
	if got := resp.Header.Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestHandleDebRedirect_NotFound(t *testing.T) {
	s := New(newTestIndex(), fakeHealth{healthy: true})
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist.deb", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleHealthz(t *testing.T) {
	healthy := New(newTestIndex(), fakeHealth{healthy: true})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthy.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when healthy", rec.Code)
	}

	unhealthy := New(newTestIndex(), fakeHealth{healthy: false})
	req2 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec2 := httptest.NewRecorder()
	unhealthy.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when unhealthy", rec2.Code)
	}
}
