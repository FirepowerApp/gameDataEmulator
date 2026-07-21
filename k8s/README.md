# Kubernetes deployment

The emulator deploys to the same cluster as the Firepower backend, into two namespaces:

| Environment | Namespace | Trigger |
|-------------|-----------|---------|
| Staging | `firepower-staging` | Auto-deploy on every merge to `main` |
| Production | `firepower` | Manual `workflow_dispatch` only |

Manifests are Kustomize overlays:

```
k8s/
├── base/                    Deployment + Service (image tag templated as ${IMAGE_TAG})
│   ├── deployment.yaml
│   ├── service.yaml
│   └── kustomization.yaml
└── overlays/
    ├── staging/             → namespace: firepower-staging
    └── production/          → namespace: firepower
```

The `Deploy to Kubernetes` workflow (`.github/workflows/deploy.yml`) renders the chosen overlay, substitutes the image tag with `envsubst`, applies it, and waits for the rollout.

---

## How to deploy

### Staging (automatic)

Merge to `main`. The `Build and Push Docker Image` workflow publishes `ghcr.io/firepowerapp/gamedataemulator`, and on its success the `Deploy to Kubernetes` workflow auto-deploys the freshly built image to `firepower-staging`. No manual action needed.

### Production (manual)

1. Confirm the image you want is in GHCR (e.g. `latest`, or a pinned `sha-<sha7>`).
2. GitHub → Actions → **Deploy to Kubernetes** → **Run workflow**.
3. Set `environment` = `production` and `image_tag` to the tag that passed staging.
4. Run. The workflow verifies the image exists, applies the production overlay, and waits for the rollout to complete.

### Testing manifest changes from a branch (before merge)

You can run the deploy against staging from any branch without merging — this is how you validate manifest changes against the real cluster:

1. Push your branch.
2. Actions → **Deploy to Kubernetes** → **Run workflow** → select your branch.
3. Set `environment` = `staging`, leave `image_tag` = `latest` (or pin one).
4. Run.

---

## How to bootstrap a namespace (first-time setup)

The deploy workflow applies the Deployment and Service but does **not** create the namespace. A namespace must exist before the first deploy to a new environment, or `kubectl apply` fails.

1. Create the namespace:

   ```bash
   kubectl create namespace firepower-staging   # or: firepower
   ```

2. Verify it exists:

   ```bash
   kubectl get namespace firepower-staging
   ```

3. Run the deploy workflow (see above). The Deployment and Service land in the namespace.

> The backend repo owns the shared cluster scaffolding (namespaces, config, secrets) via its own Infrastructure workflow. If the `firepower-staging` / `firepower` namespaces already exist from a backend deploy, skip this step — the emulator deploys into the same namespaces.

---

## Required secrets

The deploy workflow reads these from GitHub Actions secrets. They are **org-level** secrets on the `FirepowerApp` org, shared across all repos (the same ones the backend's deploy uses), so no per-repo setup is needed:

| Secret | Purpose |
|--------|---------|
| `TS_CLIENT_ID` | Tailscale OAuth client ID — connects the runner to the cluster network |
| `TS_CLIENT_SECRET` | Tailscale OAuth secret |
| `KUBECONFIG` | kubeconfig for the target cluster |

`GITHUB_TOKEN` (auto-provided by Actions) covers GHCR authentication.

To confirm the org secrets exist: org admins can check `github.com/organizations/FirepowerApp/settings/secrets/actions`, or run `gh api orgs/FirepowerApp/actions/secrets --jq '.secrets[].name'` with an `admin:org`-scoped token.

---

## What the Deployment runs

A single `gamedataemulator` container exposing two ports:

| Port | Name | Serves |
|------|------|--------|
| 8125 | `pbp` | Schedule (`/v1/schedule/{date}`) + play-by-play (`/v1/gamecenter/{id}/play-by-play`) |
| 8124 | `stats` | MoneyPuck CSV (`/moneypuck/gameData/20252026/{id}.csv`) |

Readiness and liveness probes hit the schedule endpoint. The container makes outbound HTTPS calls to `api-web.nhle.com` and `moneypuck.com` at runtime, so the cluster must allow that egress.

---

## Troubleshooting

**`namespaces "firepower-staging" not found`** — the namespace doesn't exist yet. See "How to bootstrap a namespace" above.

**Rollout times out** — check pod logs: `kubectl logs -n firepower-staging deployment/gamedataemulator`. The most common cause is the container failing to reach `api-web.nhle.com` / `moneypuck.com` (egress blocked) or missing CA certificates (the image ships them; a custom base image might not).

**`docker manifest inspect` fails in the workflow** — the image tag isn't in GHCR yet. For manual runs, pick a tag that exists (`latest` or a published `main-<sha7>`).

---

## Related

- [`../.github/workflows/deploy.yml`](../.github/workflows/deploy.yml) — the deploy workflow
- [`../README.md`](../README.md) — the Deployment section in the main README
