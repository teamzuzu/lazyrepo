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
		cfg.PollInterval = 6 * time.Hour
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
