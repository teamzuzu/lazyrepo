<img src=lazy.png width=300>

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

## Kubernetes

Manifests are in [deploy/kubernetes](deploy/kubernetes). They deploy one replica,
a Service, an Ingress, a ConfigMap containing the application configuration, and
a 1Gi persistent volume for the index cache.

1. Edit `deploy/kubernetes/configmap.yaml` to set the GitHub repositories to
   track, polling interval, and optional per-repository asset filters.
2. Set your public domain in `deploy/kubernetes/ingress.yaml` (replace
   `apt.example.com`). Set `ingressClassName` if your cluster has no default
   Ingress controller. Point the domain's DNS at the controller.
3. For HTTPS, create a TLS certificate Secret and enable the commented `tls`
   section with the same domain and Secret name.
4. Use your cluster's default StorageClass, or set `storageClassName` in
   `persistentvolumeclaim.yaml`. Pin the container image to a release tag or
   digest for production.

Apply in your chosen namespace:

```bash
kubectl apply -k deploy/kubernetes -n <namespace>
kubectl rollout status deployment/lazyrepo -n <namespace>
kubectl logs deployment/lazyrepo -n <namespace>
```

The Ingress domain is configured in the Ingress resource; Kubernetes does not
substitute hostnames from ConfigMaps into Ingress rules. The ConfigMap holds the
GitHub repository configuration. After changing it, apply the manifests again
and restart the application to load the new configuration:

```bash
kubectl apply -k deploy/kubernetes -n <namespace>
kubectl rollout restart deployment/lazyrepo -n <namespace>
```

Allow outbound HTTPS to `api.github.com`, `github.com`, and GitHub release asset
hosts (including `release-assets.githubusercontent.com`). Check the logs for a
successful initial poll and verify that `/Packages` lists the expected packages.
The readiness probe checks metadata serving; it can pass before the first poll
finishes. Monitor `/healthz` for polling failures. Liveness uses TCP so an
upstream outage does not repeatedly restart a service with a usable cached index.
The single replica and Recreate strategy prevent concurrent cache writers.

Clients can then use `deb [trusted=yes] https://<your-domain>/ ./` when TLS is
enabled. Repository metadata remains unsigned.

## Add the repo on a client

```bash
echo "deb [trusted=yes] http://<host>:8080/ ./" | sudo tee /etc/apt/sources.list.d/lazyrepo.list
sudo apt update
```

Packages are unsigned (flat apt repository format, `[trusted=yes]`) — see
`docs/superpowers/specs/2026-07-26-github-releases-apt-repo-design.md` for why, and for
the full design.
