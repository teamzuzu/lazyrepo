package poller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamzuzu/lazyrepo/internal/config"
	"github.com/teamzuzu/lazyrepo/internal/github"
	"github.com/teamzuzu/lazyrepo/internal/index"
	"github.com/teamzuzu/lazyrepo/internal/testutil"
)

func TestPollOnce_IndexesNewDebAndPersistsCache(t *testing.T) {
	debBytes := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\nArchitecture: all\nInstalled-Size: 13206\n")

	var ts *httptest.Server
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
						"browser_download_url": ts.URL + "/download/pve-microvm_0.3.21-1_all.deb",
						"digest":               "sha256:testdigest",
					},
				},
			},
		})
	})
	mux.HandleFunc("/download/pve-microvm_0.3.21-1_all.deb", func(w http.ResponseWriter, r *http.Request) {
		w.Write(debBytes)
	})
	ts = httptest.NewServer(mux)
	defer ts.Close()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "index.json")
	cfg := &config.Config{
		PollInterval: time.Minute,
		CacheFile:    cachePath,
		Repos:        []config.RepoConfig{{Repo: "rcarmo/pve-microvm"}},
	}

	gh := github.NewClientWithBaseURL(ts.URL)
	idx := index.New()
	p := New(cfg, gh, idx)

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}

	entry, ok := idx.Lookup("pve-microvm_0.3.21-1_all.deb")
	if !ok {
		t.Fatal("expected package to be indexed after PollOnce")
	}
	if entry.Version != "0.3.21-1" {
		t.Errorf("Version = %q, want %q", entry.Version, "0.3.21-1")
	}
	if entry.InstalledSize != 13206 {
		t.Errorf("InstalledSize = %d, want %d", entry.InstalledSize, 13206)
	}
	if !p.Healthy() {
		t.Error("expected Healthy() to be true after a successful poll")
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("expected cache file to be written: %v", err)
	}
}

func TestPollOnce_ReparsesEntryMissingInstalledSize(t *testing.T) {
	debBytes := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\nArchitecture: all\nInstalled-Size: 13206\n")

	var downloadCount int
	var ts *httptest.Server
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
						"browser_download_url": ts.URL + "/download/pve-microvm_0.3.21-1_all.deb",
						"digest":               "sha256:testdigest",
					},
				},
			},
		})
	})
	mux.HandleFunc("/download/pve-microvm_0.3.21-1_all.deb", func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		w.Write(debBytes)
	})
	ts = httptest.NewServer(mux)
	defer ts.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		PollInterval: time.Minute,
		CacheFile:    filepath.Join(dir, "index.json"),
		Repos:        []config.RepoConfig{{Repo: "rcarmo/pve-microvm"}},
	}

	gh := github.NewClientWithBaseURL(ts.URL)
	idx := index.New()
	// Simulate a cache populated by a pre-InstalledSize lazyrepo version:
	// same dedup key, but InstalledSize never got recorded.
	idx.Add(index.PackageEntry{
		Key:         "sha256:testdigest",
		Package:     "pve-microvm",
		Version:     "0.3.21-1",
		Filename:    "pve-microvm_0.3.21-1_all.deb",
		DownloadURL: ts.URL + "/download/pve-microvm_0.3.21-1_all.deb",
	})
	p := New(cfg, gh, idx)

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}

	if downloadCount != 1 {
		t.Errorf("downloadCount = %d, want 1 (should re-download to backfill InstalledSize)", downloadCount)
	}
	entry, ok := idx.Lookup("pve-microvm_0.3.21-1_all.deb")
	if !ok {
		t.Fatal("expected package to still be indexed after PollOnce")
	}
	if entry.InstalledSize != 13206 {
		t.Errorf("InstalledSize = %d, want %d (backfilled by re-parse)", entry.InstalledSize, 13206)
	}
}

func TestPollOnce_SkipsAssetAlreadyIndexedWithInstalledSize(t *testing.T) {
	debBytes := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\nArchitecture: all\nInstalled-Size: 13206\n")

	var downloadCount int
	var ts *httptest.Server
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
						"browser_download_url": ts.URL + "/download/pve-microvm_0.3.21-1_all.deb",
						"digest":               "sha256:testdigest",
					},
				},
			},
		})
	})
	mux.HandleFunc("/download/pve-microvm_0.3.21-1_all.deb", func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		w.Write(debBytes)
	})
	ts = httptest.NewServer(mux)
	defer ts.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		PollInterval: time.Minute,
		CacheFile:    filepath.Join(dir, "index.json"),
		Repos:        []config.RepoConfig{{Repo: "rcarmo/pve-microvm"}},
	}

	gh := github.NewClientWithBaseURL(ts.URL)
	idx := index.New()
	idx.Add(index.PackageEntry{
		Key:           "sha256:testdigest",
		Package:       "pve-microvm",
		Version:       "0.3.21-1",
		Filename:      "pve-microvm_0.3.21-1_all.deb",
		DownloadURL:   ts.URL + "/download/pve-microvm_0.3.21-1_all.deb",
		InstalledSize: 13206,
	})
	p := New(cfg, gh, idx)

	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce returned error: %v", err)
	}

	if downloadCount != 0 {
		t.Errorf("downloadCount = %d, want 0 (already-complete entry should not be re-downloaded)", downloadCount)
	}
}

func TestPollOnce_RepoErrorMarksUnhealthyButDoesNotPanic(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		PollInterval: time.Minute,
		CacheFile:    filepath.Join(dir, "index.json"),
		Repos:        []config.RepoConfig{{Repo: "owner/repo"}},
	}

	gh := github.NewClientWithBaseURL(ts.URL)
	p := New(cfg, gh, index.New())

	if err := p.PollOnce(context.Background()); err == nil {
		t.Fatal("expected PollOnce to return an error when GitHub API fails")
	}
	if p.Healthy() {
		t.Error("expected Healthy() to be false after a failed poll")
	}
}
