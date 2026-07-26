# lazyrepo: an apt repository backed by GitHub Releases

## Problem

Some projects (e.g. [rcarmo/pve-microvm](https://github.com/rcarmo/pve-microvm)) publish `.deb`
packages as GitHub Release assets, but don't run an apt repository. Users have to watch the
releases page and manually download/install each new version. This project turns any number of
such GitHub repos into a normal `apt`-installable repository, so `apt update && apt upgrade` picks
up new releases automatically.

## Goals

- Generic: config-driven list of `owner/repo` entries, not hardcoded to one project.
- No local mirroring of `.deb` bytes — apt clients download the actual package straight from
  GitHub; the server only serves repository metadata.
- All historical releases are exposed (not just latest), so specific versions can be installed/pinned.
- Runs as a Docker container.
- No GPG signing; clients add the repo with `[trusted=yes]`.
- Unauthenticated GitHub API access only (no PAT support in v1).

## Non-goals

- Mirroring/caching `.deb` file bytes on the server.
- GPG-signed `Release`/`InRelease` files.
- Multi-architecture directory structure (`dists/`/`pool/`) — a flat repo is sufficient.
- Authenticated GitHub API access (higher rate limits, private repos).
- A UI/dashboard. Configuration is a YAML file; observability is logs + `/healthz`.

## Repository format

Uses apt's ["flat repository format"](https://wiki.debian.org/DebianRepository/Format#Flat_Repository_Format):
no `Release` file, no `dists/`/`pool/` hierarchy, just a `Packages` file (plus a gzip'd copy) at the
server root. A client adds it with:

```
deb [trusted=yes] http://<host>:<port>/ ./
```

Flat format is chosen because:
- it doesn't require a `Release` file, which fits the "no signing" decision cleanly (apt does not
  demand a signed Release for flat repos added with `[trusted=yes]`)
- there's no natural "distribution"/"component" concept for a GitHub-releases mirror — every
  package's `Architecture` field (read from its own control file, typically `all` or `amd64`) is
  enough for apt to filter correctly without a directory structure

## Architecture

```
                    ┌─────────────┐
   config.yaml ───▶ │   config    │
                    └──────┬──────┘
                           │ repo list
                           ▼
                    ┌─────────────┐      GET /repos/{owner}/{repo}/releases
                    │   poller    │ ───────────────────────────────────────▶ GitHub API
                    │ (ticker)    │ ◀───────────────────────────────────────
                    └──────┬──────┘      releases + asset metadata (name, size, sha256, url)
                           │ new .deb assets (by sha256, not seen before)
                           ▼
                    ┌─────────────┐      GET <asset browser_download_url>
                    │ deb fetch + │ ───────────────────────────────────────▶ GitHub (release asset)
                    │   parser    │ ◀───────────────────────────────────────
                    └──────┬──────┘      .deb bytes (ar archive)
                           │ PackageEntry{Package,Version,Arch,Depends,Description,
                           │              Filename,Size,SHA256,DownloadURL}
                           ▼
                    ┌─────────────┐ persist ┌──────────────┐
                    │    index    │────────▶│ cache_file   │ (JSON, survives restarts)
                    │ (in-memory) │◀────────│              │
                    └──────┬──────┘  load   └──────────────┘
                           │
                           ▼
                    ┌─────────────┐
                    │ http server │  GET /Packages, /Packages.gz  → rendered from index
                    │             │  GET /<filename>.deb          → 302 → DownloadURL
                    │             │  GET /healthz                → 200 if last poll succeeded
                    └─────────────┘
```

## Components

1. **config** — loads and validates `config.yaml` (see schema below); applies defaults for
   `listen`, `poll_interval`, `cache_file`.
2. **github client** — wraps `GET /repos/{owner}/{repo}/releases` (paginated). Skips draft and
   prerelease entries. Returns each release's tag, and for each asset: name, size, SHA256 digest,
   content type, and `browser_download_url`.
3. **poller** — runs on a ticker (`poll_interval`). For each configured repo, lists releases, and
   for each asset matching that repo's `asset_filter` (or, if unset, any asset whose name ends in
   `.deb`), checks whether its SHA256 digest is already present in the index. New assets are
   downloaded and parsed; unchanged ones are skipped. After a full pass, rewrites `cache_file`.
   A failed poll (GitHub API error) logs a warning and leaves the existing index untouched — it
   does not clear or degrade previously-served data.
4. **deb parser** — takes the downloaded bytes of a `.deb` (an `ar` archive), locates the
   `control.tar.{gz,xz,zst}` member, decompresses it, and reads the `control` file within to extract
   `Package`, `Version`, `Architecture`, `Depends` (if present), `Description`. A `.deb` that fails
   to parse (corrupt, or an asset that merely has a `.deb`-like name but isn't a real package) is
   logged and skipped — it does not appear in the index and does not stop the poller.
5. **index** — an in-memory list of `PackageEntry` records, keyed by SHA256 digest for dedup/diffing.
   Serialized to/from `cache_file` as JSON so a container restart doesn't require re-downloading and
   re-parsing every historical release. Renders `Packages` (and its gzip'd form) as plain string
   concatenation of Debian control-file stanzas, generated fresh per request (cheap; no need to
   cache the rendered text itself).
6. **http server** — exposes:
   - `GET /Packages` — plaintext, one stanza per `PackageEntry`
   - `GET /Packages.gz` — gzip of the same
   - `GET /<filename>.deb` — looks up `filename` in the index and issues a 302 redirect to that
     entry's `DownloadURL` (the real GitHub asset URL); 404 if not found
   - `GET /healthz` — 200 if the most recent poll succeeded (or no poll has run yet at startup),
     500 if the most recent poll errored

## Config schema

```yaml
listen: ":8080"        # optional, default ":8080"
poll_interval: 15m     # optional, default 15m
cache_file: /data/index.json   # optional, default ./index.json

repos:
  - repo: rcarmo/pve-microvm          # required, "owner/repo"
    # asset_filter omitted -> any asset whose name ends in .deb is indexed
  - repo: someorg/multi-deb-project
    asset_filter: 'agent-.*_amd64\.deb'   # optional regex; only matching asset names are indexed
```

`repo` is required and validated as `owner/name`. `asset_filter`, when present, is a Go regexp
matched against the asset's `name`. Config is loaded once at startup; changes require a restart
(no hot-reload in v1).

## Rate limits

Unauthenticated GitHub API access is capped at 60 requests/hour per source IP. Each poll tick costs
one `GET /repos/{owner}/{repo}/releases` request per configured repo (pagination adds more only for
repos with enough releases to need a second page). With a handful of repos and the default 15
minute interval this stays well within budget. If this becomes a problem later, adding an optional
PAT is a small, isolated change to the github client (raises the cap to 5000/hr) — deferred as a
non-goal for v1.

## Deployment

Multi-stage `Dockerfile`: build a static Go binary, copy into a minimal (`scratch` or `distroless`)
final image. `config.yaml` and `cache_file`'s directory are mounted as volumes. A `docker-compose.yml`
is provided for convenience (port mapping, volume for `/data`, restart policy).

## Testing

- Unit tests for the `.deb`/`ar`/`control` parser, against small fixture `ar` archives built
  in-test (no need to vendor a real `.deb` binary into the repo).
- Unit tests for `Packages`/`Packages.gz` rendering: field formatting, stanza ordering, gzip
  round-trip.
- Poller tests against `httptest.Server` standing in for the GitHub API: serves a fake release
  list plus a synthetic `.deb` fixture; asserts the index updates, `cache_file` is written, and a
  simulated restart reloads it without re-downloading.
- One smoke test: start the server against the fake GitHub API, `curl /Packages`, confirm a
  `GET /<filename>.deb` request 302s to the expected fixture URL.

## Open questions / explicitly deferred

- No hot-reload of `config.yaml` — a restart is required to add/remove tracked repos.
- No GitHub PAT support — acceptable at current scale (few repos, unauthenticated rate limit is
  sufficient); revisit if more repos are added.
- No signing — clients must add the repo with `[trusted=yes]`.
- Draft/prerelease GitHub releases are always skipped; not configurable in v1.
