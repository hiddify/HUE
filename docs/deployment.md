# Deployment

## Docker compose

Stack: Postgres 16 + HUE behind a single TLS port. Adminer is gated
behind a `dev` profile.

```bash
cd deployments/docker
mkdir -p secrets certs geo

# Required secrets (file-mounted)
echo 'supersecret'                                          > secrets/pg_password
printf 'postgres://hue:supersecret@postgres:5432/hue?sslmode=disable' \
                                                            > secrets/db_url
echo "own_$(openssl rand -base64 18 | tr -d '/+=' | tr 'A-Z' 'a-z')" \
                                                            > secrets/bootstrap_token

# TLS cert (production: use a real cert; for local, mkcert is fine)
# mkcert localhost 127.0.0.1 ::1 → place tls.crt + tls.key in certs/

# Optional MaxMind GeoLite2-City.mmdb in geo/ for country/city extraction.

docker compose up -d
```

Then read the bootstrap token back:

```bash
cat deployments/docker/secrets/bootstrap_token
```

After the first successful start, the bootstrap row in `api_keys` is
present; **remove the bootstrap_token file** so a leak doesn't let
someone re-bootstrap a cleared DB.

The compose file at [deployments/docker/docker-compose.yml](../deployments/docker/docker-compose.yml)
sets resource limits (1 CPU + 512 MB for Postgres, 2 CPU + 1 GB for
HUE), healthchecks, and `depends_on: condition: service_healthy` so
HUE doesn't try to migrate against a not-yet-ready Postgres.

## Production Docker image

Multi-stage build → `gcr.io/distroless/static-debian12:nonroot`. ~25 MB
final image, runs as UID 65532, no shell, no package manager.

```bash
DOCKER_BUILDKIT=1 docker build \
  -f deployments/docker/Dockerfile \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t hue:dev .
```

`HEALTHCHECK` calls the binary's own `healthcheck` subcommand — no curl
in the image.

## Kubernetes

Manifests live in [deployments/k8s/](../deployments/k8s/):

| File | What it provisions |
|---|---|
| `namespace.yaml` | `hue` namespace with `pod-security: restricted` |
| `deployment.yaml` | 2-replica Deployment, non-root, read-only FS, topology spread, drop-all caps, startup/readiness/liveness probes |
| `service.yaml` | ClusterIP on 443 → 8443 |
| `configmap.yaml` | Non-secret env vars |
| `secret.example.yaml` | **Template** — copy and fill, never commit real values |
| `hpa.yaml` | HPA on CPU + memory with sensible scale-up/down policies |
| `networkpolicy.yaml` | DNS + Postgres egress only by default |
| `servicemonitor.yaml` | Prometheus-operator scrape config (optional) |

Deploy:

```bash
kubectl apply -f deployments/k8s/namespace.yaml
# (create your own copy of secret.example.yaml with real values)
kubectl apply -f hue-secrets.yaml
kubectl apply -f deployments/k8s/configmap.yaml
kubectl apply -f deployments/k8s/service.yaml
kubectl apply -f deployments/k8s/deployment.yaml
kubectl apply -f deployments/k8s/hpa.yaml
kubectl apply -f deployments/k8s/networkpolicy.yaml
# kubectl apply -f deployments/k8s/servicemonitor.yaml   # if prom-operator is installed
```

### Migrations in production

The Deployment ships with `HUE_AUTO_MIGRATE=false` to keep schema changes
out of the request hot path. Run migrations as a separate, pre-rollout
Job:

```yaml
apiVersion: batch/v1
kind: Job
metadata: { name: hue-migrate, namespace: hue }
spec:
  ttlSecondsAfterFinished: 600
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: ghcr.io/hiddify/hue:vX.Y.Z
          command: ["/usr/local/bin/hue", "migrate"]   # TODO subcommand
          envFrom: [{ configMapRef: { name: hue-config } }]
          env:
            - name: HUE_DB_URL
              valueFrom: { secretKeyRef: { name: hue-secrets, key: db_url } }
```

The `hue migrate` subcommand is on the roadmap; today migrations are
applied via `HUE_AUTO_MIGRATE=true` in dev.

### TLS certs

The Deployment expects a `kubernetes.io/tls` Secret named `hue-tls`
mounted at `/certs`. The most common production wiring is
[cert-manager](https://cert-manager.io) with a `Certificate` resource
that renews automatically.

### Bootstrap key

For the very first deploy, set `bootstrap_token` in `hue-secrets`
(plaintext must start with `own_`). After the first pod is `Ready`,
edit the Secret and remove that key, then `kubectl rollout restart
deployment/hue` so subsequent pods start without it. If you forget,
it's harmless on every restart (the bootstrap function is idempotent
— it leaves an existing Owner key alone) but a leak gets riskier the
longer it stays around.

### Encryption key

Production also needs `HUE_PASSWORD_ENC_KEY` — a 64-hex-char (32-byte)
random value. AES-256-GCM at-rest encryption falls through to
plaintext when empty (CI/dev fallback), so make sure to set it before
storing real Client passwords or ACME private keys.

```bash
echo "$(openssl rand -hex 32)" > secrets/password_enc_key
```

Mount as `HUE_PASSWORD_ENC_KEY_FILE=/run/secrets/password_enc_key` in
Docker, or as a `secretKeyRef` in the Kubernetes Deployment.

### ACME

Set `HUE_ACME_CONTACT_EMAIL` to enable `DomainCertificateService.RequestACME`.
HUE serves the HTTP-01 challenge at `/.well-known/acme-challenge/` on
its own listener — make sure your Service/Ingress routes that path
without rewriting. For testing, also set
`HUE_ACME_DIRECTORY_URL=https://acme-staging-v02.api.letsencrypt.org/directory`
to avoid Let's Encrypt's production rate limit.
