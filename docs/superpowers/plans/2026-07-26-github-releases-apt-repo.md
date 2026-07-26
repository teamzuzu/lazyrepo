# lazyrepo: GitHub-Releases-Backed apt Repository Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a small Go service that turns any number of GitHub repos publishing `.deb` release assets (e.g. `rcarmo/pve-microvm`) into a normal apt repository, so `apt update`/`apt upgrade` picks up new releases automatically.

**Architecture:** A background poller periodically lists releases for each configured `owner/repo` via the GitHub REST API, downloads and parses any new `.deb` asset's control file to build package metadata, and keeps an in-memory (disk-cached) index. An HTTP server renders that index as a flat-format apt `Packages`/`Packages.gz`, and 302-redirects `.deb` requests straight to the real GitHub asset URL — no package bytes are mirrored.

**Tech Stack:** Go 1.22, `gopkg.in/yaml.v3` (config), `github.com/ulikunitz/xz` + `github.com/klauspost/compress/zstd` (control.tar decompression), Go standard library for everything else (`net/http`, `archive/tar`, `compress/gzip`, `encoding/json`). Module path: `github.com/teamzuzu/lazyrepo`. Ships as a Docker container.

## Global Constraints

- Flat apt repository format only: a `Packages`/`Packages.gz` file at the server root, no `dists/`/`pool/` hierarchy, no `Release`/`InRelease` file.
- No GPG signing. Clients add the repo with `[trusted=yes]`.
- No local mirroring of `.deb` bytes. The server only ever stores/serves metadata; `.deb` downloads are 302 redirects to GitHub's `browser_download_url`.
- Unauthenticated GitHub REST API access only — no PAT support in this version.
- Every non-draft, non-prerelease GitHub release is indexed (not just the latest), so specific versions remain installable.
- Config (`config.yaml`) is loaded once at process startup; no hot-reload.
- A failure polling one repo, or parsing one asset, must not stop polling of other repos/assets, and must not clear previously-indexed data.

---

### Task 1: Project scaffolding & config package

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.RepoConfig{Repo, AssetFilter string}` with methods `Owner() string`, `Name() string`, `Matches(name string) bool`; `config.Config{Listen string, PollInterval time.Duration, CacheFile string, Repos []RepoConfig}`; `config.Load(path string) (*Config, error)`. Every later task that needs configuration consumes these exact names.

- [ ] **Step 1: Initialize the Go module**

Run: `cd /home/simonc/GIT/lazyrepo && go mod init github.com/teamzuzu/lazyrepo`
Expected: creates `go.mod` with `module github.com/teamzuzu/lazyrepo` and a `go` version line.

- [ ] **Step 2: Add `.gitignore`**

```
/index.json
/lazyrepo
/dist/
```

- [ ] **Step 3: Write the failing config tests**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

func TestLoad_Defaults(t *testing.T) {
	path := writeTempConfig(t, `
repos:
  - repo: rcarmo/pve-microvm
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":8080")
	}
	if cfg.PollInterval != 15*time.Minute {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 15*time.Minute)
	}
	if cfg.CacheFile != "./index.json" {
		t.Errorf("CacheFile = %q, want %q", cfg.CacheFile, "./index.json")
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].Repo != "rcarmo/pve-microvm" {
		t.Fatalf("Repos = %+v, want one entry rcarmo/pve-microvm", cfg.Repos)
	}
}

func TestLoad_ExplicitValues(t *testing.T) {
	path := writeTempConfig(t, `
listen: ":9090"
poll_interval: 5m
cache_file: /data/index.json
repos:
  - repo: rcarmo/pve-microvm
  - repo: someorg/multi-deb-project
    asset_filter: 'agent-.*_amd64\.deb'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Listen != ":9090" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":9090")
	}
	if cfg.PollInterval != 5*time.Minute {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 5*time.Minute)
	}
	if cfg.CacheFile != "/data/index.json" {
		t.Errorf("CacheFile = %q, want %q", cfg.CacheFile, "/data/index.json")
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(cfg.Repos))
	}
}

func TestLoad_MissingRepos(t *testing.T) {
	path := writeTempConfig(t, `listen: ":8080"`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for config with no repos, got nil")
	}
}

func TestLoad_MalformedRepoString(t *testing.T) {
	path := writeTempConfig(t, `
repos:
  - repo: not-a-valid-repo-string
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for malformed repo string, got nil")
	}
}

func TestLoad_InvalidAssetFilterRegex(t *testing.T) {
	path := writeTempConfig(t, `
repos:
  - repo: rcarmo/pve-microvm
    asset_filter: '['
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid asset_filter regex, got nil")
	}
}

func TestRepoConfig_OwnerName(t *testing.T) {
	rc := RepoConfig{Repo: "rcarmo/pve-microvm"}
	if rc.Owner() != "rcarmo" {
		t.Errorf("Owner() = %q, want %q", rc.Owner(), "rcarmo")
	}
	if rc.Name() != "pve-microvm" {
		t.Errorf("Name() = %q, want %q", rc.Name(), "pve-microvm")
	}
}

func TestRepoConfig_Matches_DefaultSuffix(t *testing.T) {
	path := writeTempConfig(t, `
repos:
  - repo: rcarmo/pve-microvm
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	rc := cfg.Repos[0]
	if !rc.Matches("pve-microvm_0.3.21-1_all.deb") {
		t.Error("expected .deb-suffixed asset to match with no filter configured")
	}
	if rc.Matches("initrd-microvm") {
		t.Error("expected non-.deb asset not to match with no filter configured")
	}
}

func TestRepoConfig_Matches_CustomFilter(t *testing.T) {
	path := writeTempConfig(t, `
repos:
  - repo: someorg/multi-deb-project
    asset_filter: 'agent-.*_amd64\.deb'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	rc := cfg.Repos[0]
	if !rc.Matches("agent-1.0_amd64.deb") {
		t.Error("expected asset matching custom filter to match")
	}
	if rc.Matches("agent-1.0_arm64.deb") {
		t.Error("expected asset not matching custom filter to not match")
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/config/...`
Expected: FAIL — `package config is not in std` / `undefined: Load` (package doesn't exist yet).

- [ ] **Step 5: Implement the config package**

Create `internal/config/config.go`:

```go
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RepoConfig describes one GitHub repo to track for .deb release assets.
type RepoConfig struct {
	Repo        string `yaml:"repo"`
	AssetFilter string `yaml:"asset_filter,omitempty"`

	filterRe *regexp.Regexp
}

// Owner returns the "owner" half of a "owner/name" Repo string.
func (r RepoConfig) Owner() string {
	parts := strings.SplitN(r.Repo, "/", 2)
	return parts[0]
}

// Name returns the "name" half of a "owner/name" Repo string.
func (r RepoConfig) Name() string {
	parts := strings.SplitN(r.Repo, "/", 2)
	return parts[1]
}

// Matches reports whether the given release asset name should be treated
// as a Debian package for this repo. With no AssetFilter configured, any
// name ending in ".deb" matches.
func (r RepoConfig) Matches(name string) bool {
	if r.filterRe == nil {
		return strings.HasSuffix(name, ".deb")
	}
	return r.filterRe.MatchString(name)
}

// Config is the top-level lazyrepo configuration.
type Config struct {
	Listen       string
	PollInterval time.Duration
	CacheFile    string
	Repos        []RepoConfig
}

type rawConfig struct {
	Listen       string       `yaml:"listen"`
	PollInterval string       `yaml:"poll_interval"`
	CacheFile    string       `yaml:"cache_file"`
	Repos        []RepoConfig `yaml:"repos"`
}

// Load reads and validates a lazyrepo config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	cfg := &Config{
		Listen:    raw.Listen,
		CacheFile: raw.CacheFile,
		Repos:     raw.Repos,
	}

	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.CacheFile == "" {
		cfg.CacheFile = "./index.json"
	}
	if raw.PollInterval == "" {
		cfg.PollInterval = 15 * time.Minute
	} else {
		d, err := time.ParseDuration(raw.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("parsing poll_interval: %w", err)
		}
		cfg.PollInterval = d
	}

	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("config must list at least one repo under 'repos'")
	}

	for i := range cfg.Repos {
		rc := &cfg.Repos[i]
		if strings.Count(rc.Repo, "/") != 1 || strings.HasPrefix(rc.Repo, "/") || strings.HasSuffix(rc.Repo, "/") {
			return nil, fmt.Errorf("repo %q is not a valid \"owner/name\" string", rc.Repo)
		}
		if rc.AssetFilter != "" {
			re, err := regexp.Compile(rc.AssetFilter)
			if err != nil {
				return nil, fmt.Errorf("repo %q has invalid asset_filter: %w", rc.Repo, err)
			}
			rc.filterRe = re
		}
	}

	return cfg, nil
}
```

- [ ] **Step 6: Add the yaml dependency**

Run: `go get gopkg.in/yaml.v3`
Expected: adds a `require gopkg.in/yaml.v3 ...` line to `go.mod` and creates `go.sum`.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS (all 8 tests).

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum .gitignore internal/config
git commit -m "feat: add config loading for lazyrepo"
```

---

### Task 2: GitHub releases API client

**Files:**
- Create: `internal/github/client.go`
- Test: `internal/github/client_test.go`

**Interfaces:**
- Produces: `github.Asset{Name string, Size int64, SHA256 string, DownloadURL string}`; `github.Release{TagName string, Draft bool, Prerelease bool, Assets []Asset}`; `github.Client`; `github.NewClient() *Client` (real GitHub API); `github.NewClientWithBaseURL(baseURL string) *Client` (test injection point); `(*Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error)`.

- [ ] **Step 1: Write the failing client tests**

Create `internal/github/client_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/github/...`
Expected: FAIL — `undefined: NewClientWithBaseURL` (package doesn't exist yet).

- [ ] **Step 3: Implement the client**

Create `internal/github/client.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/github/... -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/github
git commit -m "feat: add GitHub releases API client"
```

---

### Task 3: Debian `.deb` control-file parser + shared test fixture helper

**Files:**
- Create: `internal/testutil/deb.go`
- Create: `internal/debpkg/ar.go`
- Create: `internal/debpkg/parser.go`
- Test: `internal/debpkg/parser_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `testutil.BuildAr(t testing.TB, entries map[string][]byte) []byte` and `testutil.BuildDeb(t testing.TB, control string) []byte` (used by this task's tests, and by Tasks 5 and 7). `debpkg.ControlFields{Package, Version, Architecture, Depends, Description string}`; `debpkg.ParseControl(debBytes []byte) (*ControlFields, error)`.

- [ ] **Step 1: Create the shared test fixture helper**

Create `internal/testutil/deb.go`:

```go
package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"
)

// BuildAr constructs a minimal Unix ar archive ("!<arch>\n" plus one
// 60-byte header per entry) from a name->contents map, for tests that need
// a synthetic .deb-shaped byte blob without depending on a real dpkg-deb
// binary or a vendored fixture file.
func BuildAr(t testing.TB, entries map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	for name, data := range entries {
		header := make([]byte, 60)
		for i := range header {
			header[i] = ' '
		}
		copy(header[0:16], []byte(name))
		copy(header[48:58], []byte(fmt.Sprintf("%d", len(data))))
		header[58] = '`'
		header[59] = '\n'
		buf.Write(header)
		buf.Write(data)
		if len(data)%2 == 1 {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

// BuildDeb constructs a minimal .deb-shaped ar archive containing just a
// control.tar.gz member with the given control file contents. Real .debs
// also contain a debian-binary and data.tar member, but ParseControl only
// ever looks at control.tar*, so those are omitted.
func BuildDeb(t testing.TB, control string) []byte {
	t.Helper()

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(control))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("writing tar header: %v", err)
	}
	if _, err := tw.Write([]byte(control)); err != nil {
		t.Fatalf("writing tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("writing gzip content: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}

	return BuildAr(t, map[string][]byte{"control.tar.gz": gzBuf.Bytes()})
}
```

- [ ] **Step 2: Write the failing parser tests**

Create `internal/debpkg/parser_test.go`:

```go
package debpkg

import (
	"testing"

	"github.com/teamzuzu/lazyrepo/internal/testutil"
)

func TestParseControl_Basic(t *testing.T) {
	deb := testutil.BuildDeb(t, "Package: pve-microvm\n"+
		"Version: 0.3.21-1\n"+
		"Architecture: all\n"+
		"Depends: qemu-utils\n"+
		"Description: MicroVM support for Proxmox VE\n"+
		" Longer description continuation line.\n")

	fields, err := ParseControl(deb)
	if err != nil {
		t.Fatalf("ParseControl returned error: %v", err)
	}
	if fields.Package != "pve-microvm" {
		t.Errorf("Package = %q, want %q", fields.Package, "pve-microvm")
	}
	if fields.Version != "0.3.21-1" {
		t.Errorf("Version = %q, want %q", fields.Version, "0.3.21-1")
	}
	if fields.Architecture != "all" {
		t.Errorf("Architecture = %q, want %q", fields.Architecture, "all")
	}
	if fields.Depends != "qemu-utils" {
		t.Errorf("Depends = %q, want %q", fields.Depends, "qemu-utils")
	}
	if fields.Description == "" {
		t.Error("Description is empty, want non-empty")
	}
}

func TestParseControl_MissingRequiredField(t *testing.T) {
	deb := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\n")
	if _, err := ParseControl(deb); err == nil {
		t.Fatal("expected error for control file missing Architecture, got nil")
	}
}

func TestParseControl_NotAnArArchive(t *testing.T) {
	if _, err := ParseControl([]byte("not an ar archive")); err == nil {
		t.Fatal("expected error for non-ar input, got nil")
	}
}

func TestParseControl_NoControlMember(t *testing.T) {
	deb := testutil.BuildAr(t, map[string][]byte{"debian-binary": []byte("2.0\n")})
	if _, err := ParseControl(deb); err == nil {
		t.Fatal("expected error when no control.tar member is present, got nil")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/debpkg/...`
Expected: FAIL — `undefined: ParseControl` (package doesn't exist yet).

- [ ] **Step 4: Implement the ar archive reader**

Create `internal/debpkg/ar.go`:

```go
package debpkg

import (
	"fmt"
	"strconv"
	"strings"
)

const arMagic = "!<arch>\n"

type arEntry struct {
	name string
	data []byte
}

// readAr parses the contents of a Unix ar archive (the outer container
// format .deb files use) into its member entries.
func readAr(data []byte) ([]arEntry, error) {
	if len(data) < len(arMagic) || string(data[:len(arMagic)]) != arMagic {
		return nil, fmt.Errorf("not an ar archive: missing %q magic", arMagic)
	}

	var entries []arEntry
	pos := len(arMagic)
	for pos+60 <= len(data) {
		header := data[pos : pos+60]
		name := strings.TrimSuffix(strings.TrimRight(string(header[0:16]), " "), "/")
		sizeStr := strings.TrimSpace(string(header[48:58]))
		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			return nil, fmt.Errorf("ar entry %q has invalid size field %q: %w", name, sizeStr, err)
		}
		pos += 60
		if pos+size > len(data) {
			return nil, fmt.Errorf("ar entry %q is truncated", name)
		}
		entries = append(entries, arEntry{name: name, data: data[pos : pos+size]})
		pos += size
		if size%2 == 1 {
			pos++
		}
	}
	return entries, nil
}
```

- [ ] **Step 5: Implement the control-file parser**

Create `internal/debpkg/parser.go`:

```go
package debpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ControlFields is the subset of a .deb's control file that lazyrepo needs
// to render an apt Packages stanza.
type ControlFields struct {
	Package      string
	Version      string
	Architecture string
	Depends      string
	Description  string
}

// ParseControl extracts control file fields from the raw bytes of a .deb
// package (a Unix ar archive containing a control.tar.{gz,xz,zst} member).
func ParseControl(debBytes []byte) (*ControlFields, error) {
	entries, err := readAr(debBytes)
	if err != nil {
		return nil, err
	}

	var controlEntry *arEntry
	for i := range entries {
		if strings.HasPrefix(entries[i].name, "control.tar") {
			controlEntry = &entries[i]
			break
		}
	}
	if controlEntry == nil {
		return nil, fmt.Errorf("no control.tar member found in package")
	}

	tarBytes, err := decompressControlTar(controlEntry.name, controlEntry.data)
	if err != nil {
		return nil, fmt.Errorf("decompressing %s: %w", controlEntry.name, err)
	}

	text, err := extractControlFile(tarBytes)
	if err != nil {
		return nil, err
	}

	return parseControlText(text)
}

func decompressControlTar(name string, data []byte) ([]byte, error) {
	switch {
	case name == "control.tar":
		return data, nil
	case strings.HasSuffix(name, ".gz"):
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	case strings.HasSuffix(name, ".xz"):
		r, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	case strings.HasSuffix(name, ".zst"):
		r, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	default:
		return nil, fmt.Errorf("unsupported control.tar compression for member %q", name)
	}
}

func extractControlFile(tarBytes []byte) (string, error) {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading control.tar: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "control" {
			b, err := io.ReadAll(tr)
			if err != nil {
				return "", fmt.Errorf("reading control file: %w", err)
			}
			return string(b), nil
		}
	}
	return "", fmt.Errorf("control file not found inside control.tar")
}

func parseControlText(text string) (*ControlFields, error) {
	fields := map[string]string{}
	var currentKey string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			currentKey = ""
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && currentKey != "" {
			fields[currentKey] += "\n" + line
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		fields[key] = val
		currentKey = key
	}

	cf := &ControlFields{
		Package:      fields["Package"],
		Version:      fields["Version"],
		Architecture: fields["Architecture"],
		Depends:      fields["Depends"],
		Description:  fields["Description"],
	}
	if cf.Package == "" || cf.Version == "" || cf.Architecture == "" {
		return nil, fmt.Errorf("control file missing one of required fields Package/Version/Architecture")
	}
	return cf, nil
}
```

- [ ] **Step 6: Add the compression dependencies**

Run: `go get github.com/klauspost/compress github.com/ulikunitz/xz`
Expected: adds both `require` lines to `go.mod` and updates `go.sum`.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/debpkg/... -v`
Expected: PASS (4 tests).

- [ ] **Step 8: Commit**

```bash
git add internal/testutil internal/debpkg go.mod go.sum
git commit -m "feat: add .deb control-file parser"
```

---

### Task 4: Package index (in-memory + Packages rendering + disk cache)

**Files:**
- Create: `internal/index/index.go`
- Test: `internal/index/index_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `index.PackageEntry{Key, Package, Version, Architecture, Description, Depends, Filename string, Size int64, SHA256, DownloadURL string}`; `index.Index`; `index.New() *Index`; `(*Index) Has(key string) bool`; `(*Index) Add(e PackageEntry)`; `(*Index) All() []PackageEntry`; `(*Index) Lookup(filename string) (PackageEntry, bool)`; `(*Index) RenderPackages() []byte`; `(*Index) RenderPackagesGz() ([]byte, error)`; `index.LoadCache(path string) (*Index, error)`; `(*Index) SaveCache(path string) error`.

- [ ] **Step 1: Write the failing index tests**

Create `internal/index/index_test.go`:

```go
package index

import (
	"bytes"
	"compress/gzip"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func sampleEntry() PackageEntry {
	return PackageEntry{
		Key:          "sha256:abc123",
		Package:      "pve-microvm",
		Version:      "0.3.21-1",
		Architecture: "all",
		Description:  "MicroVM support for Proxmox VE",
		Depends:      "qemu-utils",
		Filename:     "pve-microvm_0.3.21-1_all.deb",
		Size:         13035136,
		SHA256:       "abc123",
		DownloadURL:  "https://github.com/rcarmo/pve-microvm/releases/download/v0.3.21/pve-microvm_0.3.21-1_all.deb",
	}
}

func TestIndex_HasAdd(t *testing.T) {
	idx := New()
	if idx.Has("sha256:abc123") {
		t.Fatal("empty index should not have any key")
	}
	idx.Add(sampleEntry())
	if !idx.Has("sha256:abc123") {
		t.Fatal("expected key to be present after Add")
	}
}

func TestIndex_All_SortedByPackageThenVersion(t *testing.T) {
	idx := New()
	idx.Add(PackageEntry{Key: "b", Package: "zeta", Version: "1.0"})
	idx.Add(PackageEntry{Key: "a", Package: "alpha", Version: "2.0"})
	idx.Add(PackageEntry{Key: "c", Package: "alpha", Version: "1.0"})

	all := idx.All()
	if len(all) != 3 {
		t.Fatalf("len(All()) = %d, want 3", len(all))
	}
	want := []string{"alpha 1.0", "alpha 2.0", "zeta 1.0"}
	for i, e := range all {
		got := e.Package + " " + e.Version
		if got != want[i] {
			t.Errorf("All()[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestIndex_Lookup(t *testing.T) {
	idx := New()
	idx.Add(sampleEntry())

	e, ok := idx.Lookup("pve-microvm_0.3.21-1_all.deb")
	if !ok {
		t.Fatal("expected Lookup to find entry by Filename")
	}
	if e.DownloadURL != sampleEntry().DownloadURL {
		t.Errorf("DownloadURL = %q, want %q", e.DownloadURL, sampleEntry().DownloadURL)
	}

	if _, ok := idx.Lookup("does-not-exist.deb"); ok {
		t.Fatal("expected Lookup to return false for unknown filename")
	}
}

func TestIndex_RenderPackages(t *testing.T) {
	idx := New()
	idx.Add(sampleEntry())

	out := string(idx.RenderPackages())
	for _, want := range []string{
		"Package: pve-microvm\n",
		"Version: 0.3.21-1\n",
		"Architecture: all\n",
		"Filename: ./pve-microvm_0.3.21-1_all.deb\n",
		"Size: 13035136\n",
		"SHA256: abc123\n",
		"Depends: qemu-utils\n",
		"Description: MicroVM support for Proxmox VE\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderPackages() missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Error("expected stanza to end with a blank line separator")
	}
}

func TestIndex_RenderPackagesGz_RoundTrips(t *testing.T) {
	idx := New()
	idx.Add(sampleEntry())

	gz, err := idx.RenderPackagesGz()
	if err != nil {
		t.Fatalf("RenderPackagesGz returned error: %v", err)
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading decompressed data: %v", err)
	}
	if string(decompressed) != string(idx.RenderPackages()) {
		t.Error("RenderPackagesGz content does not match RenderPackages after decompression")
	}
}

func TestSaveCache_LoadCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	idx := New()
	idx.Add(sampleEntry())
	if err := idx.SaveCache(path); err != nil {
		t.Fatalf("SaveCache returned error: %v", err)
	}

	loaded, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache returned error: %v", err)
	}
	if !loaded.Has("sha256:abc123") {
		t.Fatal("expected loaded index to have the saved entry")
	}
	e, _ := loaded.Lookup("pve-microvm_0.3.21-1_all.deb")
	if e.Version != "0.3.21-1" {
		t.Errorf("Version = %q, want %q", e.Version, "0.3.21-1")
	}
}

func TestLoadCache_MissingFile_ReturnsEmptyIndex(t *testing.T) {
	dir := t.TempDir()
	idx, err := LoadCache(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadCache returned error for missing file: %v", err)
	}
	if len(idx.All()) != 0 {
		t.Errorf("len(All()) = %d, want 0 for missing cache file", len(idx.All()))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/index/...`
Expected: FAIL — `undefined: New` (package doesn't exist yet).

- [ ] **Step 3: Implement the index package**

Create `internal/index/index.go`:

```go
package index

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// PackageEntry is one Debian package known to lazyrepo, derived from a
// single GitHub release asset.
type PackageEntry struct {
	Key          string // dedup identity, e.g. "sha256:<digest>"
	Package      string
	Version      string
	Architecture string
	Description  string
	Depends      string
	Filename     string // bare asset name, e.g. "pve-microvm_0.3.21-1_all.deb"
	Size         int64
	SHA256       string
	DownloadURL  string
}

// Index is the in-memory set of known packages, safe for concurrent use.
type Index struct {
	mu      sync.RWMutex
	entries map[string]PackageEntry
}

// New returns an empty Index.
func New() *Index {
	return &Index{entries: make(map[string]PackageEntry)}
}

// Has reports whether an entry with the given Key is already indexed.
func (idx *Index) Has(key string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	_, ok := idx.entries[key]
	return ok
}

// Add inserts or replaces the entry for e.Key.
func (idx *Index) Add(e PackageEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.entries[e.Key] = e
}

// All returns every entry, sorted by Package then Version for stable output.
func (idx *Index) All() []PackageEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]PackageEntry, 0, len(idx.entries))
	for _, e := range idx.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// Lookup finds the entry whose Filename matches the given bare filename
// (as extracted from an incoming HTTP request path).
func (idx *Index) Lookup(filename string) (PackageEntry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for _, e := range idx.entries {
		if e.Filename == filename {
			return e, true
		}
	}
	return PackageEntry{}, false
}

// RenderPackages renders every entry as a Debian "Packages" file.
func (idx *Index) RenderPackages() []byte {
	var buf bytes.Buffer
	for _, e := range idx.All() {
		fmt.Fprintf(&buf, "Package: %s\n", e.Package)
		fmt.Fprintf(&buf, "Version: %s\n", e.Version)
		fmt.Fprintf(&buf, "Architecture: %s\n", e.Architecture)
		fmt.Fprintf(&buf, "Filename: ./%s\n", e.Filename)
		fmt.Fprintf(&buf, "Size: %d\n", e.Size)
		if e.SHA256 != "" {
			fmt.Fprintf(&buf, "SHA256: %s\n", e.SHA256)
		}
		if e.Depends != "" {
			fmt.Fprintf(&buf, "Depends: %s\n", e.Depends)
		}
		if e.Description != "" {
			fmt.Fprintf(&buf, "Description: %s\n", e.Description)
		}
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

// RenderPackagesGz renders the same content as RenderPackages, gzip-compressed.
func (idx *Index) RenderPackagesGz() ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(idx.RenderPackages()); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// LoadCache reads a previously-saved Index from path. A missing file is
// not an error - it simply returns an empty Index, as on first run.
func LoadCache(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading cache file: %w", err)
	}

	var entries []PackageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing cache file: %w", err)
	}

	idx := New()
	for _, e := range entries {
		idx.entries[e.Key] = e
	}
	return idx, nil
}

// SaveCache writes the Index to path atomically (write to a temp file in
// the same directory, then rename over the destination).
func (idx *Index) SaveCache(path string) error {
	idx.mu.RLock()
	entries := make([]PackageEntry, 0, len(idx.entries))
	for _, e := range idx.entries {
		entries = append(entries, e)
	}
	idx.mu.RUnlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling cache: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".index-*.json.tmp")
	if err != nil {
		return fmt.Errorf("creating temp cache file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp cache file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming temp cache file into place: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/index/... -v`
Expected: PASS (7 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/index
git commit -m "feat: add package index with Packages rendering and disk cache"
```

---

### Task 5: Poller (GitHub → index orchestration)

**Files:**
- Create: `internal/poller/poller.go`
- Test: `internal/poller/poller_test.go`

**Interfaces:**
- Consumes: `config.Config`/`config.RepoConfig` (Task 1), `github.Client`/`github.NewClientWithBaseURL`/`github.Asset`/`github.Release` (Task 2), `debpkg.ParseControl` (Task 3), `index.Index`/`index.New`/`index.PackageEntry` (Task 4), `testutil.BuildDeb` (Task 3).
- Produces: `poller.Poller`; `poller.New(cfg *config.Config, gh *github.Client, idx *index.Index) *Poller`; `(*Poller) PollOnce(ctx context.Context) error`; `(*Poller) Run(ctx context.Context)`; `(*Poller) Healthy() bool`. `Healthy()` is the exact method Task 6's `server.HealthChecker` interface expects.

- [ ] **Step 1: Write the failing poller tests**

Create `internal/poller/poller_test.go`:

```go
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
	debBytes := testutil.BuildDeb(t, "Package: pve-microvm\nVersion: 0.3.21-1\nArchitecture: all\n")

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
	if !p.Healthy() {
		t.Error("expected Healthy() to be true after a successful poll")
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("expected cache file to be written: %v", err)
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/poller/...`
Expected: FAIL — `undefined: New` (package doesn't exist yet).

- [ ] **Step 3: Implement the poller**

Create `internal/poller/poller.go`:

```go
package poller

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/teamzuzu/lazyrepo/internal/config"
	"github.com/teamzuzu/lazyrepo/internal/debpkg"
	"github.com/teamzuzu/lazyrepo/internal/github"
	"github.com/teamzuzu/lazyrepo/internal/index"
)

// Poller periodically checks configured GitHub repos for new .deb release
// assets and adds them to an Index.
type Poller struct {
	cfg *config.Config
	gh  *github.Client
	idx *index.Index

	httpClient *http.Client
	healthy    atomic.Bool
}

// New returns a Poller wired to cfg, gh, and idx. idx should already be
// loaded (e.g. via index.LoadCache) before the first PollOnce call.
func New(cfg *config.Config, gh *github.Client, idx *index.Index) *Poller {
	p := &Poller{cfg: cfg, gh: gh, idx: idx, httpClient: http.DefaultClient}
	p.healthy.Store(true) // no poll has run yet at startup
	return p
}

// Healthy reports whether the most recent PollOnce call completed without
// any repo-level errors (or true if no poll has run yet).
func (p *Poller) Healthy() bool {
	return p.healthy.Load()
}

// Run polls once immediately, then again every cfg.PollInterval, until ctx
// is cancelled.
func (p *Poller) Run(ctx context.Context) {
	if err := p.PollOnce(ctx); err != nil {
		log.Printf("poll: %v", err)
	}

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.PollOnce(ctx); err != nil {
				log.Printf("poll: %v", err)
			}
		}
	}
}

// PollOnce checks every configured repo for new .deb assets, adds them to
// the index, and persists the index to cfg.CacheFile. A failure on one
// repo does not stop the others from being polled. Returns a non-nil error
// if any repo (or the cache save) failed, but the index and cache file
// still reflect whatever succeeded.
func (p *Poller) PollOnce(ctx context.Context) error {
	var firstErr error
	for i := range p.cfg.Repos {
		if err := p.pollRepo(ctx, &p.cfg.Repos[i]); err != nil {
			log.Printf("polling %s: %v", p.cfg.Repos[i].Repo, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if err := p.idx.SaveCache(p.cfg.CacheFile); err != nil {
		log.Printf("saving cache: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	p.healthy.Store(firstErr == nil)
	return firstErr
}

func (p *Poller) pollRepo(ctx context.Context, rc *config.RepoConfig) error {
	releases, err := p.gh.ListReleases(ctx, rc.Owner(), rc.Name())
	if err != nil {
		return fmt.Errorf("listing releases: %w", err)
	}

	for _, rel := range releases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		for _, asset := range rel.Assets {
			if !rc.Matches(asset.Name) {
				continue
			}
			key := assetKey(asset)
			if p.idx.Has(key) {
				continue
			}

			data, err := p.download(ctx, asset.DownloadURL)
			if err != nil {
				log.Printf("downloading %s: %v", asset.Name, err)
				continue
			}
			fields, err := debpkg.ParseControl(data)
			if err != nil {
				log.Printf("parsing %s: %v", asset.Name, err)
				continue
			}

			p.idx.Add(index.PackageEntry{
				Key:          key,
				Package:      fields.Package,
				Version:      fields.Version,
				Architecture: fields.Architecture,
				Description:  fields.Description,
				Depends:      fields.Depends,
				Filename:     asset.Name,
				Size:         asset.Size,
				SHA256:       asset.SHA256,
				DownloadURL:  asset.DownloadURL,
			})
		}
	}
	return nil
}

func (p *Poller) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// assetKey returns the Index dedup key for a GitHub release asset: its
// SHA256 digest when GitHub reports one, otherwise a fallback derived from
// its name and size.
func assetKey(a github.Asset) string {
	if a.SHA256 != "" {
		return "sha256:" + a.SHA256
	}
	return fmt.Sprintf("name:%s:size:%d", a.Name, a.Size)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/poller/... -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/poller
git commit -m "feat: add poller tying GitHub releases into the package index"
```

---

### Task 6: HTTP server (Packages/Packages.gz/.deb redirect/healthz)

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `index.Index`/`index.New`/`index.PackageEntry` (Task 4). Defines its own `HealthChecker` interface rather than depending on `*poller.Poller` directly, so `*poller.Poller` (Task 5, which already has a `Healthy() bool` method) satisfies it structurally with no import cycle.
- Produces: `server.HealthChecker` (interface: `Healthy() bool`); `server.Server`; `server.New(idx *index.Index, health HealthChecker) *Server`; `(*Server) Handler() http.Handler`.

- [ ] **Step 1: Write the failing server tests**

Create `internal/server/server_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/...`
Expected: FAIL — `undefined: New` (package doesn't exist yet).

- [ ] **Step 3: Implement the server**

Create `internal/server/server.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/server/... -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/server
git commit -m "feat: add HTTP server for Packages metadata and .deb redirects"
```

---

### Task 7: `main.go` wiring + end-to-end smoke test

**Files:**
- Create: `cmd/lazyrepo/main.go`
- Test: `cmd/lazyrepo/main_test.go`

**Interfaces:**
- Consumes: every package from Tasks 1-6 (`config`, `github`, `index`, `poller`, `server`, `testutil`). This is the final integration point; no new exported names are produced.

- [ ] **Step 1: Write the failing end-to-end test**

Create `cmd/lazyrepo/main_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/lazyrepo/...`
Expected: FAIL — `undefined: config`/build error, since `cmd/lazyrepo` doesn't exist as a buildable package yet without `main.go` (there is no non-test `.go` file, so `package main` has no importable symbols beyond the test file itself — the test still compiles once `main.go` exists in Step 3).

- [ ] **Step 3: Implement `main.go`**

Create `cmd/lazyrepo/main.go`:

```go
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/teamzuzu/lazyrepo/internal/config"
	"github.com/teamzuzu/lazyrepo/internal/github"
	"github.com/teamzuzu/lazyrepo/internal/index"
	"github.com/teamzuzu/lazyrepo/internal/poller"
	"github.com/teamzuzu/lazyrepo/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	idx, err := index.LoadCache(cfg.CacheFile)
	if err != nil {
		log.Fatalf("loading cache: %v", err)
	}

	gh := github.NewClient()
	p := poller.New(cfg, gh, idx)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go p.Run(ctx)

	srv := server.New(idx, p)
	httpServer := &http.Server{Addr: cfg.Listen, Handler: srv.Handler()}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("lazyrepo listening on %s", cfg.Listen)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server: %v", err)
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/lazyrepo/... -v`
Expected: PASS.

- [ ] **Step 5: Run the full test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: build succeeds, vet reports nothing, all tests across every package PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd
git commit -m "feat: wire up lazyrepo main entrypoint"
```

---

### Task 8: Docker packaging, example config, and README

**Files:**
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `config.example.yaml`
- Create: `README.md`

**Interfaces:**
- Consumes: the `cmd/lazyrepo` binary produced by Task 7. No new Go code.

- [ ] **Step 1: Write the Dockerfile**

Create `Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/lazyrepo ./cmd/lazyrepo

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/lazyrepo /lazyrepo
EXPOSE 8080
ENTRYPOINT ["/lazyrepo"]
CMD ["-config", "/data/config.yaml"]
```

- [ ] **Step 2: Write the example config**

Create `config.example.yaml`:

```yaml
listen: ":8080"
poll_interval: 15m
cache_file: /data/index.json

repos:
  - repo: rcarmo/pve-microvm
    # asset_filter omitted -> any release asset ending in .deb is indexed

  # Example of a repo that publishes more than one .deb per release, where
  # only assets matching this pattern should be tracked:
  # - repo: someorg/multi-deb-project
  #   asset_filter: 'agent-.*_amd64\.deb'
```

- [ ] **Step 3: Write docker-compose.yml**

Create `docker-compose.yml`:

```yaml
services:
  lazyrepo:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./config.yaml:/data/config.yaml:ro
      - lazyrepo-data:/data
    restart: unless-stopped

volumes:
  lazyrepo-data:
```

- [ ] **Step 4: Write the README**

Create `README.md`:

```markdown
# lazyrepo

Turns one or more GitHub repos that publish `.deb` files as release assets into a normal
apt repository, so `apt update`/`apt upgrade` picks up new releases automatically. No
`.deb` bytes are mirrored — lazyrepo only serves repository metadata and redirects package
downloads straight to GitHub.

## Configure

Copy `config.example.yaml` to `config.yaml` and list the repos to track:

```yaml
repos:
  - repo: rcarmo/pve-microvm
```

See `config.example.yaml` for all options (`listen`, `poll_interval`, `cache_file`,
per-repo `asset_filter`).

## Run

```bash
docker compose up -d
```

## Add the repo on a client

```bash
echo "deb [trusted=yes] http://<host>:8080/ ./" | sudo tee /etc/apt/sources.list.d/lazyrepo.list
sudo apt update
```

Packages are unsigned (flat apt repository format, `[trusted=yes]`) — see
`docs/superpowers/specs/2026-07-26-github-releases-apt-repo-design.md` for why, and for
the full design.
```

- [ ] **Step 5: Verify the Docker image builds**

Run: `docker build -t lazyrepo:test .`
Expected: image builds successfully with no errors.

- [ ] **Step 6: Commit**

```bash
git add Dockerfile docker-compose.yml config.example.yaml README.md
git commit -m "feat: add Docker packaging, example config, and README"
```
