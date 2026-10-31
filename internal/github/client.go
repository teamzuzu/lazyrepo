package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
	return &Client{httpClient: &http.Client{Timeout: 30 * time.Second}, baseURL: "https://api.github.com"}
}

// NewClientWithBaseURL returns a Client pointed at an arbitrary base URL,
// for tests to inject an httptest.Server in place of the real GitHub API.
func NewClientWithBaseURL(baseURL string) *Client {
	return &Client{httpClient: &http.Client{Timeout: 30 * time.Second}, baseURL: baseURL}
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

// ListReleases returns up to 1,000 recent releases, newest first. Small pages
// avoid expensive GitHub responses on repositories with many release assets.
// On failure it returns completed pages alongside the error.
func (c *Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error) {
	var releases []Release
	for pageNum := 1; pageNum <= 100; pageNum++ {
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=10&page=%d", c.baseURL, owner, repo, pageNum)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "lazyrepo")

		apiPage, err := c.fetchPage(req, owner, repo, pageNum)
		if err != nil {
			return releases, err
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
		if len(apiPage) < 10 {
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

// fetchPage retries only transient responses; validation and auth errors are final.
func (c *Client) fetchPage(req *http.Request, owner, repo string, page int) ([]apiRelease, error) {
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching releases for %s/%s page %d: %w", owner, repo, page, err)
		}
		if resp.StatusCode == http.StatusOK {
			var result []apiRelease
			err := json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("decoding releases for %s/%s page %d: %w", owner, repo, page, err)
			}
			return result, nil
		}
		// Report GitHub's JSON message, not arbitrary HTML or response headers.
		var detail struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&detail)
		resp.Body.Close()
		status := resp.StatusCode
		retry := status == 502 || status == 503 || status == 504
		if !retry || attempt == 2 {
			return nil, fmt.Errorf("GitHub API returned status %d for %s/%s page %d: %q", status, owner, repo, page, detail.Message)
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}
