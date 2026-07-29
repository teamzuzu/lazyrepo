# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

lazyrepo turns one or more GitHub repos that publish `.deb` files as release assets (e.g.
`rcarmo/pve-microvm`) into a normal apt repository, so `apt update`/`apt upgrade` picks up new
releases automatically. It never mirrors `.deb` bytes: it serves only repository metadata
(`Packages`/`Packages.gz`/`Release`) and 302-redirects package downloads straight to GitHub's
`browser_download_url`. Ships as a Docker image published to `ghcr.io/teamzuzu/lazyrepo`.

## Commands

```bash
go build ./...                          # build everything
go vet ./...                            # vet
go test ./...                           # full test suite
go test ./... -race                     # with the race detector (run before pushing)
go test ./internal/poller/... -v        # single package, verbose
go test ./internal/poller/... -run TestPollOnce_ReparsesEntryMissingInstalledSize -v  # single test

go run ./cmd/lazyrepo -config config.yaml   # run locally (copy config.example.yaml first)
docker build -t lazyrepo:test .             # build the image locally
docker compose up -d                        # run via the published ghcr.io image
```

No linter/formatter config beyond `gofmt`/`go vet` - there is no Makefile.

## Architecture

Six single-purpose packages under `internal/`, wired together by `cmd/lazyrepo/main.go`:

```
config  → poller ← github
                 ← debpkg
                 ← index → server (HealthChecker interface only, no import of poller)
```

- **`internal/config`**: loads/validates `config.yaml` (`Load`, `Config`, `RepoConfig`). `RepoConfig.Matches(name)` implements the per-repo asset filter (defaults to any `*.deb` suffix).
- **`internal/github`**: thin GitHub REST client (`Client.ListReleases`) with pagination. `NewClientWithBaseURL` exists purely so tests can point it at an `httptest.Server` instead of the real API.
- **`internal/debpkg`**: parses a `.deb`'s control file straight from its bytes — reads the outer `ar` archive (`ar.go`), decompresses whichever `control.tar.{gz,xz,zst}` member is present, and parses Debian control syntax (`parser.go`). Pure `[]byte` in, `*ControlFields` out; no filesystem or network access, and every decompression step is `LimitReader`-bounded against decompression bombs.
- **`internal/index`**: the in-memory + disk-cached package list (`Index`, `PackageEntry`). Renders the actual apt-facing files: `RenderPackages`/`RenderPackagesGz`/`RenderRelease`. `SaveCache`/`LoadCache` persist to JSON via atomic temp-file-then-rename.
- **`internal/poller`**: the only package that ties the others together. `PollOnce` walks every configured repo's releases (skipping draft/prerelease), downloads any new/incomplete `.deb` asset just long enough to run it through `debpkg.ParseControl`, and adds the result to the `Index`. A failure on one repo or asset is logged and skipped, never fatal to the rest of the poll.
- **`internal/server`**: HTTP layer (`/Packages`, `/Packages.gz`, `/Release`, `/healthz`, and a catch-all that 302-redirects `<filename>.deb` requests). Depends only on `internal/index` plus its own tiny `HealthChecker` interface — `*poller.Poller` satisfies it structurally, avoiding an import cycle.
- **`internal/testutil`**: `BuildAr`/`BuildDeb` construct synthetic `.deb`-shaped byte blobs in tests (real `ar`/`tar`/`gzip`, no vendored binary fixture). Shared by `debpkg`, `poller`, and `cmd/lazyrepo`'s tests.

Tests throughout use real `httptest.Server`s and real archive/compression bytes rather than mocks — see any `*_test.go` for the pattern.

### Repo format

Flat apt repository format only (`deb [trusted=yes] http://host:port/ ./`) — no `dists/`/`pool/`
hierarchy, no GPG signing. `Release` is served unsigned purely so apt knows which index files
exist upfront; without it apt blindly probes `Packages.xz`/`.bz2`/`.lzma` before falling back to
`.gz`, which is harmless but noisy (see `Index.RenderRelease`'s doc comment).

### The Installed-Size gotcha

**Every `PackageEntry` must have a nonzero `InstalledSize`, or apt will treat that package as
perpetually upgradable even when the candidate and installed versions are byte-identical
strings.** This was confirmed empirically (bisected field-by-field against a real `dpkg`
install in a sandboxed `apt`): apt only merges a repo's version record with the locally
installed one — letting `apt-cache policy` show one entry and `apt upgrade` correctly report
"already up to date" — when `Installed-Size` is present and matches what dpkg recorded at
install time. Missing it doesn't cause an error anywhere; it just means `apt update && apt
upgrade` reinstalls the same version forever. The field is always available for the taking:
`dpkg-deb` writes it into every real `.deb`'s own control file, so `debpkg.ParseControl` just
needs to read it (`ControlFields.InstalledSize`) and `poller`/`index` need to carry it through.

If you ever add another field to `PackageEntry` that existing deployments need backfilled,
follow the same pattern used for this one: `poller.pollRepo`'s dedup check
(`existing.InstalledSize != 0`) treats an already-indexed asset as "still needs re-parsing" when
that field is zero, so a cache from an older build self-heals on the next poll (or immediately,
since `Poller.Run` polls once at startup) instead of requiring anyone to manually clear
`/data/index.json`. A plain `Has(key)` check would silently skip re-parsing forever.
