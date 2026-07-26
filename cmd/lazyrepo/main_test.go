package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teamzuzu/lazyrepo/internal/config"
	"github.com/teamzuzu/lazyrepo/internal/github"
	"github.com/teamzuzu/lazyrepo/internal/index"
	"github.com/teamzuzu/lazyrepo/internal/poller"
	"github.com/teamzuzu/lazyrepo/internal/server"
	"github.com/teamzuzu/lazyrepo/internal/testutil"
)

func TestEndToEnd_PackagesAndRedirect(t *testing.T) {
	debBytes := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\nArchitecture: all\n")

	var fakeGitHub *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/rcarmo/pve-microvm/releases", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name":   "v0.3.21",
				"draft":      false,
				"prerelease": false,
				"assets": []map[string]any{
					{
						"name":                 "pve-microvm_0.3.21-1_all.deb",
						"size":                 len(debBytes),
						"browser_download_url": fakeGitHub.URL + "/download/pve-microvm_0.3.21-1_all.deb",
						"digest":               "sha256:testdigest",
					},
				},
			},
		})
	})
	mux.HandleFunc("/download/pve-microvm_0.3.21-1_all.deb", func(w http.ResponseWriter, r *http.Request) {
		w.Write(debBytes)
	})
	fakeGitHub = httptest.NewServer(mux)
	defer fakeGitHub.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		PollInterval: time.Minute,
		CacheFile:    filepath.Join(dir, "index.json"),
		Repos:        []config.RepoConfig{{Repo: "rcarmo/pve-microvm"}},
	}

	gh := github.NewClientWithBaseURL(fakeGitHub.URL)
	idx := index.New()
	p := poller.New(cfg, gh, idx)
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}

	srv := server.New(idx, p)
	repoServer := httptest.NewServer(srv.Handler())
	defer repoServer.Close()

	resp, err := http.Get(repoServer.URL + "/Packages")
	if err != nil {
		t.Fatalf("GET /Packages: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Package: pve-microvm") || !strings.Contains(string(body), "Version: 0.3.21-1") {
		t.Fatalf("Packages output missing expected stanza, got:\n%s", body)
	}

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	redirResp, err := client.Get(repoServer.URL + "/pve-microvm_0.3.21-1_all.deb")
	if err != nil {
		t.Fatalf("GET .deb: %v", err)
	}
	defer redirResp.Body.Close()
	if redirResp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", redirResp.StatusCode)
	}
	wantLocation := fakeGitHub.URL + "/download/pve-microvm_0.3.21-1_all.deb"
	if got := redirResp.Header.Get("Location"); got != wantLocation {
		t.Errorf("Location = %q, want %q", got, wantLocation)
	}
}
