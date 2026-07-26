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
	p := &Poller{cfg: cfg, gh: gh, idx: idx, httpClient: &http.Client{Timeout: 2 * time.Minute}}
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
