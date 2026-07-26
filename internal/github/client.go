package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Asset is a single GitHub release asset relevant to lazyrepo.
type Asset struct {
	Name        string
	Size        int64
	SHA256      string // empty if GitHub did not report a digest for this asset
	DownloadURL string
}

// Release is a single GitHub release, with draft/prerelease status and its assets.
type Release struct {
	TagName    string
	Draft      bool
	Prerelease bool
	Assets     []Asset
}

// Client talks to the GitHub REST API.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient returns a Client pointed at the real GitHub API.
func NewClient() *Client {
	return &Client{httpClient: http.DefaultClient, baseURL: "https://api.github.com"}
}

// NewClientWithBaseURL returns a Client pointed at an arbitrary base URL,
// for tests to inject an httptest.Server in place of the real GitHub API.
func NewClientWithBaseURL(baseURL string) *Client {
	return &Client{httpClient: http.DefaultClient, baseURL: baseURL}
}

type apiAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type apiRelease struct {
	TagName    string     `json:"tag_name"`
	Draft      bool       `json:"draft"`
	Prerelease bool       `json:"prerelease"`
	Assets     []apiAsset `json:"assets"`
}

// ListReleases returns every release (all pages) for owner/repo, in the
// order returned by the GitHub API (newest first).
func (c *Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error) {
	var releases []Release
	for pageNum := 1; ; pageNum++ {
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d", c.baseURL, owner, repo, pageNum)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "lazyrepo")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching releases for %s/%s: %w", owner, repo, err)
		}

		var apiPage []apiRelease
		decodeErr := json.NewDecoder(resp.Body).Decode(&apiPage)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub API returned status %d for %s/%s", resp.StatusCode, owner, repo)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decoding releases for %s/%s: %w", owner, repo, decodeErr)
		}
		if len(apiPage) == 0 {
			break
		}

		for _, ar := range apiPage {
			releases = append(releases, Release{
				TagName:    ar.TagName,
				Draft:      ar.Draft,
				Prerelease: ar.Prerelease,
				Assets:     convertAssets(ar.Assets),
			})
		}
		if len(apiPage) < 100 {
			break
		}
	}
	return releases, nil
}

func convertAssets(in []apiAsset) []Asset {
	out := make([]Asset, len(in))
	for i, a := range in {
		out[i] = Asset{
			Name:        a.Name,
			Size:        a.Size,
			SHA256:      strings.TrimPrefix(a.Digest, "sha256:"),
			DownloadURL: a.BrowserDownloadURL,
		}
	}
	return out
}
