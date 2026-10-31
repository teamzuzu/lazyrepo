package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListReleases_SinglePage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name":   "v0.3.21",
				"draft":      false,
				"prerelease": false,
				"assets": []map[string]any{
					{
						"name":                 "pve-microvm_0.3.21-1_all.deb",
						"size":                 13035136,
						"browser_download_url": "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/pve-microvm_0.3.21-1_all.deb",
						"digest":               "sha256:88824abed329a8f1eeb329a8e7bad4a3a7d831854f49df18f29710bbf449d458",
					},
					{
						"name":                 "initrd-microvm",
						"size":                 1108159,
						"browser_download_url": "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/initrd-microvm",
						"digest":               "",
					},
				},
			},
		})
	}))
	defer ts.Close()

	c := NewClientWithBaseURL(ts.URL)
	releases, err := c.ListReleases(context.Background(), "rcarmo", "pve-microvm")
	if err != nil {
		t.Fatalf("ListReleases returned error: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("len(releases) = %d, want 1", len(releases))
	}
	rel := releases[0]
	if rel.TagName != "v0.3.21" {
		t.Errorf("TagName = %q, want %q", rel.TagName, "v0.3.21")
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("len(Assets) = %d, want 2", len(rel.Assets))
	}
	deb := rel.Assets[0]
	if deb.Name != "pve-microvm_0.3.21-1_all.deb" {
		t.Errorf("Assets[0].Name = %q, want %q", deb.Name, "pve-microvm_0.3.21-1_all.deb")
	}
	if deb.SHA256 != "88824abed329a8f1eeb329a8e7bad4a3a7d831854f49df18f29710bbf449d458" {
		t.Errorf("Assets[0].SHA256 = %q, sha256: prefix not stripped correctly", deb.SHA256)
	}
	if deb.Size != 13035136 {
		t.Errorf("Assets[0].Size = %d, want 13035136", deb.Size)
	}
	if rel.Assets[1].SHA256 != "" {
		t.Errorf("Assets[1].SHA256 = %q, want empty for asset with no digest", rel.Assets[1].SHA256)
	}
}

func TestListReleases_StopsOnEmptyPage(t *testing.T) {
	var requestedPages []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPages = append(requestedPages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	c := NewClientWithBaseURL(ts.URL)
	releases, err := c.ListReleases(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("ListReleases returned error: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("len(releases) = %d, want 0", len(releases))
	}
	if len(requestedPages) != 1 || requestedPages[0] != "1" {
		t.Fatalf("requestedPages = %v, want a single request for page 1", requestedPages)
	}
}

func TestListReleases_ErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	c := NewClientWithBaseURL(ts.URL)
	if _, err := c.ListReleases(context.Background(), "owner", "repo"); err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
}

func TestListReleases_PreservesPagesAndExplains422(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("per_page") != "10" {
			t.Error("expected smaller pages")
		}
		if r.URL.Query().Get("page") == "1" {
			page := make([]apiRelease, 10)
			page[0].TagName = "v1"
			json.NewEncoder(w).Encode(page)
			return
		}
		w.WriteHeader(422)
		w.Write([]byte(`{"message":"Pagination limit exceeded"}`))
	}))
	defer ts.Close()
	releases, err := NewClientWithBaseURL(ts.URL).ListReleases(context.Background(), "openai", "codex")
	if len(releases) != 10 || releases[0].TagName != "v1" {
		t.Fatal("lost completed page")
	}
	if err == nil || !strings.Contains(err.Error(), "page 2") || !strings.Contains(err.Error(), "Pagination limit exceeded") {
		t.Fatalf("missing error detail: %v", err)
	}
	if calls != 2 {
		t.Fatalf("validation error retried: %d requests", calls)
	}
}

func TestListReleases_RetriesGatewayError(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(504)
			return
		}
		w.Write([]byte(`[{"tag_name":"v1"}]`))
	}))
	defer ts.Close()
	releases, err := NewClientWithBaseURL(ts.URL).ListReleases(context.Background(), "o", "r")
	if err != nil || len(releases) != 1 || calls != 2 {
		t.Fatalf("retry failed: %v, calls %d", err, calls)
	}
}

func TestListReleases_BoundsHistory(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(make([]apiRelease, 10))
	}))
	defer ts.Close()
	releases, err := NewClientWithBaseURL(ts.URL).ListReleases(context.Background(), "o", "r")
	if err != nil || len(releases) != 1000 || calls != 100 {
		t.Fatalf("unbounded history: %d calls, %d releases, %v", calls, len(releases), err)
	}
}

func TestListReleases_CancellationStopsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cancel()
		w.WriteHeader(503)
	}))
	defer ts.Close()
	_, err := NewClientWithBaseURL(ts.URL).ListReleases(ctx, "o", "r")
	if err == nil || calls != 1 {
		t.Fatalf("cancellation ignored: %v, calls %d", err, calls)
	}
}
