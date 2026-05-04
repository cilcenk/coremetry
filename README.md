# Coremetry

[![CI](https://github.com/cilcenk/coremetry/actions/workflows/ci.yml/badge.svg)](https://github.com/cilcenk/coremetry/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/cilcenk/coremetry?display_name=tag&sort=semver)](https://github.com/cilcenk/coremetry/releases)
[![License: PolyForm Noncommercial](https://img.shields.io/badge/license-PolyForm%20Noncommercial%201.0.0-orange.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cilcenk/coremetry)](https://goreportcard.com/report/github.com/cilcenk/coremetry)

Enterprise OpenTelemetry APM. Traces, metrics, logs, and profiles on
ClickHouse, with a Next.js UI, custom dashboards, SLOs, anomaly detection,
local + OIDC auth, and a Tempo-compatible API for Grafana integration.

```
apps ──▶ OTel Collector ──▶ Coremetry (gRPC :4317)
                              │
                              ├── ClickHouse  (storage)
                              ├── Redis       (cache + leader lock, optional)
                              └── HTTP UI/API (:8088)
```

---

## Screenshots

<p align="center">
  <img src="docs/screenshots/trace-waterfall.png" alt="Trace waterfall — span timeline with stacktraces" width="100%">
  <br><sub><em>Tempo-style trace waterfall — span timeline, attributes, events, and inline stacktraces.</em></sub>
</p>

<p align="center">
  <img src="docs/screenshots/grafana-tempo.png" alt="Coremetry as a Tempo datasource in Grafana" width="100%">
  <br><sub><em>Coremetry exposed as a native Tempo datasource — query traces from Grafana Explore, with node graph and trace-to-logs jumps.</em></sub>
</p>

<table>
  <tr>
    <td width="50%">
      <img src="docs/screenshots/services.png" alt="Services list with Apdex"><br>
      <sub><b>Services</b> — Apdex, error rate, P99 latency, sortable</sub>
    </td>
    <td width="50%">
      <img src="docs/screenshots/dashboard.png" alt="Custom dashboard"><br>
      <sub><b>Dashboards</b> — metric / span-aggregation / stat / markdown panels</sub>
    </td>
  </tr>
  <tr>
    <td width="50%">
      <img src="docs/screenshots/service-graph.png" alt="Service graph"><br>
      <sub><b>Service Graph</b> — auto-discovered topology with call rates and error stains</sub>
    </td>
    <td width="50%">
      <img src="docs/screenshots/problems.png" alt="Problems with anomaly badges"><br>
      <sub><b>Problems</b> — rule-based + Watchdog-style anomaly detection</sub>
    </td>
  </tr>
  <tr>
    <td width="50%">
      <img src="docs/screenshots/slos.png" alt="SLOs with error budget burn rate"><br>
      <sub><b>SLOs</b> — availability / latency SLIs with error-budget burn down</sub>
    </td>
    <td width="50%">
      <img src="docs/screenshots/profiling.png" alt="Continuous profiling flame graph"><br>
      <sub><b>Profiling</b> — async-profiler / pprof flame graphs, trace-to-profile drill-down</sub>
    </td>
  </tr>
  <tr>
    <td width="50%">
      <img src="docs/screenshots/trace-stacktrace.png" alt="Trace span with exception stacktrace"><br>
      <sub><b>Span detail</b> — exception events with full stack frames, log correlation</sub>
    </td>
    <td width="50%">
      <img src="docs/screenshots/login.png" alt="Login screen with optional SSO"><br>
      <sub><b>Login</b> — local username/password + optional OIDC SSO button</sub>
    </td>
  </tr>
</table>

---

## Quick start (Docker Compose, recommended for local + small prod)

Requires Docker + Docker Compose plugin.

```bash
git clone https://github.com/cilcenk/coremetry.git
cd coremetry
docker compose up -d
```

What you get:

| Service       | URL                       | Notes                                       |
| ------------- | ------------------------- | ------------------------------------------- |
| **Coremetry UI** | http://localhost:8088     | Login: `admin@coremetry.local` / `admin`       |
| OTel Collector| `localhost:14317` (gRPC)  | Apps send here; collector forwards to coremetry|
|               | `localhost:14318` (HTTP)  |                                             |
| ClickHouse    | `localhost:9000`          | Native protocol; HTTP on `:8123`            |
| Grafana       | http://localhost:3000     | Pre-wired Coremetry → Tempo datasource (admin/admin) |
| Java demo     | (in network)              | Auto-instrumented Spring Boot, sends traces |

Optional profiles:

```bash
docker compose --profile grafana   up -d   # add Grafana
docker compose --profile go-demo   up -d   # add a Go traffic generator
```

Point your own apps at the collector:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14318   # HTTP
# or
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14317   # gRPC
```

---

## Expose via Cloudflare Tunnel (zero-cloud, public HTTPS URL)

For sharing a demo without paying for a cloud host, the bundled
`cloudflared` service publishes Coremetry to a public HTTPS URL. Two modes:

### Quick tunnel (no Cloudflare account, ephemeral URL)

```bash
cp .env.example .env
# Edit .env — at minimum set:
#   COREMETRY_JWT_SECRET=$(openssl rand -hex 32)
#   COREMETRY_INITIAL_PASSWORD=<a strong password>
#   CLICKHOUSE_PASSWORD=<another strong password>
# Leave CLOUDFLARE_TUNNEL_TOKEN empty.

docker compose up -d
docker compose --profile tunnel up -d cloudflared

# Watch the logs — Cloudflare prints the public URL it assigned.
docker compose logs -f cloudflared
# Look for: https://<random-words>.trycloudflare.com
```

The URL changes on every cloudflared restart. Good for "just look at this for an hour", not for production.

### Named tunnel (persistent URL on your domain)

1. **Cloudflare dashboard** → Zero Trust → Networks → Tunnels → **Create**
2. Name it `coremetry`, pick **cloudflared**, copy the install token
3. Add a public hostname → service URL `http://coremetry:8088` (the docker network resolves it)
4. Paste the token into `.env`:
   ```
   CLOUDFLARE_TUNNEL_TOKEN=eyJhI...
   ```
5. Bring it up:
   ```bash
   docker compose --profile tunnel-named up -d cloudflared-named
   ```

Cloudflare terminates TLS at the edge, so you don't need Caddy/Traefik
inside the stack. The coremetry container stays HTTP-only on the docker
network and is never directly reachable from the public internet.

### ⚠ Before exposing publicly

| Setting | Why |
|---|---|
| `COREMETRY_JWT_SECRET` | empty value rotates on every restart, drops sessions |
| `COREMETRY_INITIAL_PASSWORD` | the default `admin` is in this README |
| `CLICKHOUSE_PASSWORD` | even though CH isn't published through the tunnel, set it |
| Drop CH host ports | comment out `9000:9000` and `8123:8123` in compose if you don't need direct CH access |
| Drop java-demo and Grafana host ports | same — they're internal-only when going through the tunnel |

After first login at the tunnel URL, sign in with `admin@coremetry.local` /
`<INITIAL_PASSWORD>`, open the user menu → **Change password** to rotate
even that one.

---

## Production install (Helm)

The chart lives in [`charts/coremetry`](charts/coremetry). It deploys:

- **Coremetry** — the Go binary with embedded UI
- **ClickHouse** (`clickhouse.enabled: true`, default) — single-node
  StatefulSet using the official `clickhouse/clickhouse-server:24.8-alpine`
  image with a 20 GiB PVC mounted at `/var/lib/clickhouse`. For
  production scale set `clickhouse.enabled: false` and point
  `clickhouse.external.addr` at an Altinity-Operator-managed cluster.
- **Redis** (`redis.enabled: true`, default) — in-cluster cache + leader
  lock. Disable + use `redis.external.url` for managed Redis.
- **OTel Collector** (`otelCollector.enabled: true`, default) —
  upstream collector for apps to point their OTLP exporter at.

Both the **Docker image** and the **Helm chart** are published to GHCR
on every `v*.*.*` tag. No registry secret needed — both are public.

| Artifact | Pull URL |
|---|---|
| Docker image | `ghcr.io/cilcenk/coremetry:<version>` |
| Helm chart (OCI) | `oci://ghcr.io/cilcenk/charts/coremetry --version <version>` |

Browse versions at <https://github.com/cilcenk?tab=packages>.

### Vanilla Kubernetes — bundled CH (one-command demo)

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  --namespace coremetry --create-namespace \
  --set secrets.jwtSecret=$(openssl rand -hex 32) \
  --set secrets.initialAdminPassword=$INITIAL_PW \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=coremetry.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

(Or clone the repo and use `./charts/coremetry` for a local checkout.)

That installs everything — coremetry, CH, Redis, OTel Collector — on a
single namespace. Storage: ~20 GiB PVC for CH (override via
`clickhouse.storage.size`).

### Vanilla Kubernetes — external CH (production)

```bash
helm install coremetry ./charts/coremetry \
  --namespace coremetry --create-namespace \
  --set clickhouse.enabled=false \
  --set clickhouse.external.addr=ch-cluster.databases.svc:9000 \
  --set secrets.clickHousePassword=$CH_PASSWORD \
  --set secrets.jwtSecret=$(openssl rand -hex 32) \
  --set secrets.initialAdminPassword=$INITIAL_PW \
  --set ingress.enabled=true ...
```

### OpenShift

The chart is **drop-in compatible with OpenShift's `restricted-v2` SCC**:
no fixed `runAsUser`, no privileged caps, `seccompProfile: RuntimeDefault`,
read-only root with a writable `/tmp` emptyDir. The image's USER is
non-root (UID 65532) and `/app` is group-readable so the random UID
OpenShift assigns at admission can still execute the binary. The
bundled ClickHouse uses the official `clickhouse/clickhouse-server:24.8-alpine`
image with the PVC mounted at `/var/lib/clickhouse`. The image runs as
fixed UID 101 — bind `anyuid` SCC to the chart's service account first
(see snippet below).

Use a **Route** instead of Ingress — the OCP router terminates HTTPS at
the edge with the cluster's wildcard cert, no cert-manager needed.
HTTPS-by-default + HTTP→HTTPS redirect are the chart's defaults:

```yaml
route:
  enabled: false
  host: ""               # empty → router auto-generates coremetry-coremetry.apps.<cluster>
  tls:
    enabled: true
    termination: edge                       # ← edge HTTPS
    insecureEdgeTerminationPolicy: Redirect # ← HTTP auto-redirects to HTTPS
```

#### One-command install (bundled CH + Redis + Collector + Route on HTTPS)

```bash
oc new-project coremetry

helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  --namespace coremetry \
  --set route.enabled=true \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set secrets.initialAdminPassword="$(openssl rand -base64 16)" \
  --set 'config.auth.initialAdmin=admin@coremetry.local'
```

If you're testing unreleased changes from a working tree (e.g. you just
cloned the repo to add a feature), install from the local chart instead
of the OCI registry:

```bash
helm install coremetry ./charts/coremetry \
  -n coremetry \
  --set route.enabled=true \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set secrets.initialAdminPassword="$(openssl rand -base64 16)"
```

> **ClickHouse + restricted-v2:** the bundled ClickHouse image runs as
> fixed UID 101, which the default OpenShift SCC rejects. Two paths:
>   1. **Recommended for prod** — point at an external CH (next snippet).
>   2. **Quick demo** — bind `anyuid` to the chart's CH service account
>     *after* the first failed install creates it:
>
>     ```bash
>     oc adm policy add-scc-to-user anyuid \
>       -z coremetry-clickhouse -n coremetry
>     oc rollout restart statefulset/coremetry-clickhouse -n coremetry
>     ```

#### External Elasticsearch as the logs backend

If you already ship logs to ES (Filebeat / Logstash / OTel Collector ES
exporter) and want Coremetry's UI to query them directly:

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  -n coremetry \
  --set route.enabled=true \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set secrets.initialAdminPassword="$(openssl rand -base64 16)" \
  --set logs.backend=elasticsearch \
  --set 'logs.elasticsearch.addresses={https://es.your-cluster.com:9200}' \
  --set logs.elasticsearch.index='logs-otel-*' \
  --set secrets.esApiKey="$YOUR_BASE64_ES_API_KEY" \
  --set logs.elasticsearch.insecureSkipVerify=false
```

The API key value is the **base64 `id:api_key` string** — the `encoded`
field returned by `POST /_security/api_key`. Coremetry's ingest path
keeps writing logs to ClickHouse; only the read side flips to ES, so
this is reversible with one env-var change. See
[External Elasticsearch logs backend (optional)](#external-elasticsearch-logs-backend-optional)
for the field-shape table and a least-privilege API-key example.

#### External ClickHouse (the right answer for prod on OpenShift)

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  -n coremetry \
  --set route.enabled=true \
  --set clickhouse.enabled=false \
  --set clickhouse.external.addr=ch-cluster.databases.svc:9000 \
  --set clickhouse.database=coremetry \
  --set secrets.clickHousePassword="$CH_PASSWORD" \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set secrets.initialAdminPassword="$(openssl rand -base64 16)"
```

#### Air-gapped clusters

Mirror the images into your internal registry, then point the chart at
it with one flag — no per-image overrides needed:

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  -n coremetry \
  --set route.enabled=true \
  --set global.imageRegistry=registry.example.com \
  --set 'global.imagePullSecrets={registry-pull-cred}' \
  --set secrets.jwtSecret="$(openssl rand -hex 32)" \
  --set secrets.initialAdminPassword="$(openssl rand -base64 16)"
```

`global.imageRegistry` rewrites the registry of all four images
(coremetry, ClickHouse, Redis, OTel Collector) in one place. See
[Air-gapped / private mirror](#air-gapped--private-mirror-globalimageregistry)
below for the upstream → mirror path mapping.

#### Post-install verify

```bash
oc rollout status deploy/coremetry -n coremetry
oc get route coremetry -n coremetry -o jsonpath='https://{.spec.host}{"\n"}'
# Login: admin@coremetry.local / <the password from above>
```

Pod admission errors that suggest you need to lift SCC restrictions are
almost always avoidable by overriding chart values rather than granting
extra privilege — open an issue with the error if you hit one.

### Connecting an existing OTel Collector to Coremetry

If your cluster already runs a Collector (e.g. OpenShift's
`OpenTelemetryCollector` operator), disable the chart's bundled one and
point your existing collector's exporter at coremetry's gRPC service:

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  -n coremetry \
  --set otelCollector.enabled=false \
  --set route.enabled=true ...
```

Then in your Collector's CR, add an OTLP exporter pointing at:

```
coremetry-coremetry.coremetry.svc.cluster.local:4317
```

Example exporter config:

```yaml
exporters:
  otlp/coremetry:
    endpoint: coremetry-coremetry.coremetry.svc.cluster.local:4317
    tls: { insecure: true }       # in-cluster traffic, no TLS termination on coremetry
    sending_queue: { enabled: true, num_consumers: 4, queue_size: 5000 }
    retry_on_failure: { enabled: true, initial_interval: 1s, max_interval: 30s }

service:
  pipelines:
    traces:  { receivers: [otlp], processors: [batch], exporters: [otlp/coremetry] }
    metrics: { receivers: [otlp], processors: [batch], exporters: [otlp/coremetry] }
    logs:    { receivers: [otlp], processors: [batch], exporters: [otlp/coremetry] }
```

Cross-namespace works too — qualify the host with the coremetry namespace
(`coremetry-coremetry.<namespace>.svc.cluster.local`). For OCP NetworkPolicy
clusters allow ingress on TCP/4317 to coremetry from the collector's NS.

### External Elasticsearch logs backend (optional)

Coremetry's ingest path always writes logs to ClickHouse. The **read** side
of `/api/logs` can be redirected to an external Elasticsearch cluster
that your existing shipping pipeline (Filebeat, Logstash, OTel Collector
ES exporter) already populates — no re-indexing, no double-storage.

The bundled `docker compose` stack already wires this up: a local ES
container, the collector's `elasticsearch` exporter in `mapping.mode: ecs`,
and `COREMETRY_LOGS_BACKEND=elasticsearch` on the coremetry container.
Everything below describes how to point Coremetry at a *real*
production / test ES cluster.

**Document field shape** — defaults match the OTel Collector ES exporter
in ECS mode:

| Path           | Purpose                                |
|----------------|----------------------------------------|
| `@timestamp`   | log time (RFC 3339 nanos)              |
| `trace.id`     | hex trace ID — used by `?traceId=…`    |
| `span.id`      | hex span ID — used by `?spanId=…`      |
| `service.name` | service filter dropdown                |
| `message`      | log body                               |
| `log.level`    | severity text (info / warn / error / …) |

Override per-deployment in the chart's `logs.elasticsearch.fields.*` block
if your shipper uses different paths.

**Authentication.** Three modes, picked in this precedence order:

| Mode | Set | When to use |
|------|-----|-------------|
| API key | `COREMETRY_ES_API_KEY` | **Preferred for prod / test.** Scoped to a single index pattern, rotatable without touching user accounts. |
| HTTP basic | `COREMETRY_ES_USERNAME` + `COREMETRY_ES_PASSWORD` | Quick path when the cluster only exposes built-in users. |
| None | _(leave all empty)_ | Only sane for `xpack.security.enabled: false` clusters (the bundled local ES). |

The API key value is the **base64 `id:api_key` string** — the `encoded`
field returned by `POST /_security/api_key`. To create one with
read-only access to a single index pattern:

```bash
curl -u elastic:$ES_PASSWORD -X POST "https://es.example.com/_security/api_key" \
  -H 'Content-Type: application/json' -d '{
    "name": "coremetry-logs-reader",
    "role_descriptors": {
      "logs-read": {
        "indices": [{ "names": ["logs-otel-*"], "privileges": ["read", "view_index_metadata"] }]
      }
    }
  }'
# Response includes "encoded": "<base64-string>" — that string is COREMETRY_ES_API_KEY.
```

Set `COREMETRY_ES_INSECURE=true` if the cluster's TLS cert is self-signed
(common in staging) — chain verification will be skipped for that
client only. Do **not** set it against a public / production cluster.

**Helm install pointing at a managed ES cluster:**

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  -n coremetry \
  --set logs.backend=elasticsearch \
  --set 'logs.elasticsearch.addresses={https://es.example.com:9200}' \
  --set logs.elasticsearch.index='logs-otel-*' \
  --set secrets.esApiKey="$(cat my-api-key.b64)" \
  --set logs.elasticsearch.insecureSkipVerify=false \
  --set route.enabled=true ...
```

`secrets.esApiKey` lands in the chart-managed Kubernetes Secret, so
rotating the key later is `kubectl edit secret … -n coremetry` followed
by a pod restart — no `helm upgrade` needed.

If you maintain your own pre-existing Secret (e.g. from External Secrets
Operator), set `secrets.existingSecret: my-secret` and put the same keys
(`es-password`, `es-api-key`) in it.

### Java demo (optional)

Spring Boot demo app with zero-code OTel auto-instrumentation +
async-profiler sidecar. Off by default; enable to see traces / metrics /
profiles flow end-to-end with no app code to write:

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  -n coremetry \
  --set javaDemo.enabled=true \
  --set route.enabled=true ...
```

The demo image (`ghcr.io/cilcenk/coremetry-java-demo`) is pushed alongside
coremetry's image on every release tag.

### Air-gapped / private mirror (`global.imageRegistry`)

Enterprise / regulated environments usually require all images to be
mirrored into an internal registry. The chart's `global.imageRegistry`
overrides the registry portion of every image (coremetry + ClickHouse +
Redis + OTel Collector) in one place.

Mirror these four upstream images into your registry first:

| Upstream | Suggested mirror path |
|---|---|
| `ghcr.io/cilcenk/coremetry:<version>`            | `<your-registry>/cilcenk/coremetry:<version>` |
| `clickhouse/clickhouse-server:24.8-alpine`    | `<your-registry>/clickhouse/clickhouse-server:24.8-alpine` |
| `library/redis:7-alpine`                      | `<your-registry>/library/redis:7-alpine` |
| `otel/opentelemetry-collector-contrib:0.111.0`| `<your-registry>/otel/opentelemetry-collector-contrib:0.111.0` |
| `ghcr.io/cilcenk/coremetry-java-demo:<version>`  | `<your-registry>/cilcenk/coremetry-java-demo:<version>` *(only if `javaDemo.enabled=true`)* |

Then install with the override:

```bash
helm install coremetry oci://ghcr.io/cilcenk/charts/coremetry --version 0.2.2 \
  -n coremetry \
  --set global.imageRegistry=registry.example.com \
  --set 'global.imagePullSecrets={registry-pull-cred}' \
  --set route.enabled=true \
  ...
```

`global.imagePullSecrets` is propagated to every pod (coremetry + CH +
Redis + Collector) so a single registry credential covers the whole
stack.

To swap a single image without touching the rest, set its per-image
`registry` / `repository`:

```bash
--set image.registry=quay.io --set image.repository=yourorg/coremetry
```

### ClickHouse on OpenShift — anyuid SCC

The bundled CH (`clickhouse.enabled: true`) uses the official
`clickhouse/clickhouse-server:24.8-alpine` image, which runs as the
fixed UID 101. OpenShift's `restricted-v2` SCC rejects fixed UIDs
outside the project's allocated range, so before installing on OCP
you need ONE of:

```bash
# Option A — bind anyuid SCC to the chart's service account
oc adm policy add-scc-to-user anyuid -z coremetry-coremetry -n coremetry

# Option B — disable bundled CH and point at an external one
helm install ... \
  --set clickhouse.enabled=false \
  --set clickhouse.external.addr=ch-cluster.databases.svc:9000
```

Production deployments should prefer Option B with an Altinity Operator
managed cluster.

After install, the chart's NOTES print port-forward / ingress URLs and warn
if you left any insecure defaults in place.

### Common overrides

```yaml
# my-values.yaml
replicaCount: 3                 # safe — Redis lock arbitrates background workers

clickhouse:
  addr: ch.databases.svc:9000
  database: coremetry
  username: coremetry

# Use an existing Secret instead of letting the chart create one:
secrets:
  existingSecret: coremetry-prod-secrets
  # Must contain keys: jwt-secret, clickhouse-password,
  #                    initial-admin-password, oidc-client-secret

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: coremetry.example.com
      paths: [{ path: /, pathType: Prefix }]
  tls:
    - secretName: coremetry-tls
      hosts: [coremetry.example.com]

autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 10

# OIDC SSO (Google, Keycloak, Okta, Auth0…)
config:
  auth:
    oidc:
      enabled: true
      issuerUrl:   https://accounts.google.com
      clientId:    "<your-client-id>"
      redirectUrl: https://coremetry.example.com/api/auth/oidc/callback
      defaultRole: viewer
      allowedDomains: ["acme.com"]
secrets:
  oidcClientSecret: "<your-client-secret>"

# External managed Redis (ElastiCache, Memorystore, Upstash...)
redis:
  enabled: false
  external:
    url: "rediss://:password@my-redis:6380/0"
```

```bash
helm upgrade --install coremetry ./charts/coremetry -f my-values.yaml -n coremetry
```

### Helm chart values reference

| Key                          | Default                        | Notes |
| ---------------------------- | ------------------------------ | ----- |
| `image.repository`           | `ghcr.io/cenk/coremetry`          | |
| `image.tag`                  | `Chart.appVersion`             | |
| `replicaCount`               | `1`                            | Scale up freely; lock arbitrates workers |
| `service.httpPort`           | `8088`                         | Web UI + REST API + OTLP/HTTP fallback |
| `service.grpcPort`           | `4317`                         | OTLP/gRPC ingest |
| `ingress.enabled`            | `false`                        | |
| `autoscaling.enabled`        | `false`                        | HPA on CPU + memory |
| `clickhouse.enabled`         | `true`                         | Bundle CH StatefulSet (dev/small prod) |
| `clickhouse.external.addr`   | `""`                           | If set, overrides bundled CH (e.g. `ch.svc:9000`) |
| `clickhouse.storage.size`    | `20Gi`                         | PVC size for bundled CH |
| `clickhouse.database`        | `coremetry`                       | Created on first boot |
| `route.enabled`              | `false`                        | OpenShift Route — HTTPS via edge TLS by default |
| `redis.enabled`              | `true`                         | In-cluster Redis (small/dev) |
| `redis.external.url`         | `""`                           | If set, in-cluster Redis is skipped |
| `otelCollector.enabled`      | `true`                         | Sidecar collector deployment |
| `config.retention.spansDays` | `30`                           | TTL on the spans table |
| `config.auth.oidc.enabled`   | `false`                        | Local auth always remains available |
| `secrets.jwtSecret`          | `""`                           | **Set in prod** — empty rotates on restart |
| `secrets.initialAdminPassword` | `"admin"`                    | **Change before exposing** |

Full surface in [`charts/coremetry/values.yaml`](charts/coremetry/values.yaml).

---

## Build from source

```bash
make            # builds frontend (Next.js static export) + Go binary
./coremetry        # uses ./config.yaml
```

Requirements: Go 1.25+, Node 20+, a reachable ClickHouse.

Configure via [`config.yaml`](config.yaml) or environment variables:

| Env var                       | Effect                                     |
| ----------------------------- | ------------------------------------------ |
| `COREMETRY_CH_ADDR`              | ClickHouse host:port                       |
| `COREMETRY_CH_PASSWORD`          | ClickHouse password                        |
| `COREMETRY_HTTP_ADDR`            | UI/API listen address (default `:8088`)    |
| `COREMETRY_GRPC_ADDR`            | OTLP/gRPC listen (default `:4317`)         |
| `COREMETRY_REDIS_URL`            | `redis://host:port/db` — enables cache + lock |
| `COREMETRY_JWT_SECRET`           | HS256 signing key (set this in prod)       |
| `COREMETRY_INITIAL_ADMIN`        | Bootstrap admin email                       |
| `COREMETRY_INITIAL_PASSWORD`     | Bootstrap admin password                    |
| `COREMETRY_OIDC_ENABLED`         | `true` to enable OIDC SSO                  |
| `COREMETRY_OIDC_ISSUER_URL`      | OIDC discovery URL                         |
| `COREMETRY_OIDC_CLIENT_ID`       | OIDC client ID                             |
| `COREMETRY_OIDC_CLIENT_SECRET`   | OIDC client secret                         |
| `COREMETRY_OIDC_REDIRECT_URL`    | Public callback URL                        |

---

## Scaling notes

| Load              | Architecture                                             |
| ----------------- | -------------------------------------------------------- |
| < 50 svc, < 5k spans/s | Single Coremetry replica. ClickHouse on SSD. No Redis required. |
| 50–500 svc, 5–50k spans/s | Multiple Coremetry replicas behind a service. **Enable Redis** (cache + leader lock). External managed CH. |
| 500+ svc, very high ingest | Same as above + Kafka/Redpanda between OTLP receiver and CH writers (durability + replay). CH cluster with sharding (Altinity Operator). |

Why Redis matters once you horizontally scale Coremetry:

- **Distributed lock** — the alert evaluator, anomaly detector, and SLO
  computation must run *once per tick*, not once per replica. Without
  Redis, every replica opens the same Problems.
- **Hot cache** — sidebar polls `/api/problems` every 30s per logged-in
  user. At 100 active users that's 3 RPS just for the badge. The 5s TTL
  cache collapses this to ~0.2 RPS against ClickHouse.
- **Service list, metric names, OIDC discovery** — read-mostly, expensive
  to compute, ideal cache candidates.

---

## Authentication

- **Local** username/password is always available, even when OIDC is on
  (so you keep an admin fallback if the IdP is unreachable).
- **OIDC SSO** is opt-in via `auth.oidc.enabled`. Standard Authorization
  Code + PKCE + nonce. First-time OIDC users are auto-provisioned with
  `auth.oidc.defaultRole`. Optional `allowedDomains` whitelist.
- Sessions are stateless JWTs in `HttpOnly` cookies. Set
  `COREMETRY_JWT_SECRET` in production so sessions survive restarts.
- The first run seeds an admin from `auth.initial_admin` /
  `initial_password`. Rotate the password from the user menu after first
  login. Subsequent runs are no-ops if any users exist.

---

## Layout

```
.
├── main.go                  # entrypoint
├── internal/
│   ├── api/                 # HTTP API + Tempo-compatible routes
│   ├── auth/                # JWT + bcrypt + OIDC + middleware
│   ├── cache/               # Redis cache + distributed lock (Noop fallback)
│   ├── chstore/             # ClickHouse repository layer
│   ├── consumer/            # Batch ingest pipeline (in-memory queue → CH)
│   ├── otlp/                # OTLP gRPC + HTTP ingest
│   ├── evaluator/           # Alert rule evaluator (background)
│   ├── anomaly/             # Watchdog-style baseline anomaly detector
│   └── profileconv/         # pprof / async-profiler ingest + flame graph
├── frontend/                # Next.js static export (embedded into binary)
├── charts/coremetry/           # Helm chart
├── docker-compose.yml       # Local dev stack (CH + collector + Java demo + Grafana)
└── config.yaml              # Default config
```
