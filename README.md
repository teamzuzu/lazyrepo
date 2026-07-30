# lazy repo

![logo](./lazy.png | width=200)

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
