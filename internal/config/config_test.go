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
