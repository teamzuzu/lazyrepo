package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	httpClient   *http.Client
	baseURL      string
	mu           sync.Mutex
	pages        map[string]cachedPage
	histories    map[string][]Release
	blockedUntil time.Time
	now          func() time.Time
}

type cachedPage struct {
	etag     string
	releases []apiRelease
}

func (c *Client) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pages == nil {
		c.pages = make(map[string]cachedPage)
		c.histories = make(map[string][]Release)
	}
	historyKey := owner + "/" + repo
	var releases []Release
	for pageNum := 1; pageNum <= 100; pageNum++ {
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=10&page=%d", c.baseURL, owner, repo, pageNum)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "lazyrepo")

		cached := c.pages[u]
		if cached.etag != "" {
			req.Header.Set("If-None-Match", cached.etag)
		}
		apiPage, unchanged, err := c.fetchPage(req, owner, repo, pageNum)
		if err != nil {
			return releases, err
		}

		// Only skip the tail after a complete previous scan. Partial failures must
		// be resumed even when the newest page has not changed.
		if pageNum == 1 && unchanged {
			if history, ok := c.histories[historyKey]; ok {
				return history, nil
			}
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
	c.histories[historyKey] = releases
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
func (c *Client) fetchPage(req *http.Request, owner, repo string, page int) ([]apiRelease, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if c.currentTime().Before(c.blockedUntil) {
			return nil, false, fmt.Errorf("GitHub API requests paused until %s (rate limit)", c.blockedUntil.UTC().Format(time.RFC3339))
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, false, fmt.Errorf("fetching releases for %s/%s page %d: %w", owner, repo, page, err)
		}
		c.observeRateLimit(resp)
		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			cached, ok := c.pages[req.URL.String()]
			if !ok {
				return nil, false, fmt.Errorf("GitHub returned 304 without a cached page")
			}
			return cached.releases, true, nil
		}
		if resp.StatusCode == http.StatusOK {
			var result []apiRelease
			err := json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if err != nil {
				return nil, false, fmt.Errorf("decoding releases for %s/%s page %d: %w", owner, repo, page, err)
			}
			c.pages[req.URL.String()] = cachedPage{etag: resp.Header.Get("ETag"), releases: result}
			return result, false, nil
		}
		// Report GitHub's JSON message, not arbitrary HTML or response headers.
		var detail struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&detail)
		resp.Body.Close()
		status := resp.StatusCode
		if (status == 403 || status == 429) && strings.Contains(strings.ToLower(detail.Message), "rate limit") && !c.blockedUntil.After(c.currentTime()) {
			c.blockedUntil = c.currentTime().Add(time.Minute)
		}
		retry := status == 502 || status == 503 || status == 504
		if !retry || attempt == 2 {
			return nil, false, fmt.Errorf("GitHub API returned status %d for %s/%s page %d: %q", status, owner, repo, page, detail.Message)
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, false, req.Context().Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

// observeRateLimit shares cooldown across repositories using the same API quota.
func (c *Client) observeRateLimit(resp *http.Response) {
	now := c.currentTime()
	limited := resp.StatusCode == 429 || (resp.StatusCode == 403 && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""))
	exhausted := resp.Header.Get("X-RateLimit-Remaining") == "0"
	if !limited && !exhausted {
		return
	}
	until := now
	if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		until = time.Unix(reset, 0).Add(time.Second)
	}
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		var retryTime time.Time
		if seconds, err := strconv.Atoi(retry); err == nil && seconds >= 0 {
			retryTime = now.Add(time.Duration(seconds) * time.Second)
		} else {
			retryTime, _ = http.ParseTime(retry)
		}
		if retryTime.After(until) {
			until = retryTime
		}
	}
	if !until.After(now) {
		until = now.Add(time.Minute)
	}
	if until.After(c.blockedUntil) {
		c.blockedUntil = until
	}
}
