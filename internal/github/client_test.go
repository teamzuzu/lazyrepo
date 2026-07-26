package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
