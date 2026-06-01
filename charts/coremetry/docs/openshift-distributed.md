# Deploying Coremetry on OpenShift (distributed)

Production-grade guide for running Coremetry in **distributed** mode on
OpenShift, at a real-bank bar (1000s of services, 10000s of operations,
1B+ spans/day). Every value path and default cited here is from the
chart in this directory — `values.yaml`, `Chart.yaml`, and
`templates/*`. Nothing below requires editing a template.

> All commands assume the chart is pulled from the OCI registry:
> `oci://ghcr.io/cilcenk/charts/coremetry`. Pin `--version` to the
> chart `version` from `Chart.yaml` (currently `0.7.18`) in production.

---

## 1. `deployment.mode: distributed` — what it renders

The chart runs **one image in different roles** via the `COREMETRY_MODE`
env var. The single-binary contract is preserved: one image, one tag,
four roles. The role is selected by `deployment.mode` plus
`deployment.roles.*`.

```yaml
deployment:
  mode: distributed          # monolithic (default) | distributed
  roles:
    ingest:
      replicas: 2            # OTLP receivers + CH writers — scale for fan-in
      resources: {}          # falls back to top-level .Values.resources when {}
    api:
      replicas: 2            # HTTP + UI + SSE + MCP — scale for read HA + RPS
      resources: {}
    worker:
      replicas: 1            # DO NOT change — leader-elected via Redis lock
      resources: {}
    agent:                   # v0.7.4 runbook agent — optional, off by default
      enabled: false
      replicas: 1
      resources: {}
```

### What gets created in distributed mode

`templates/deployment-distributed.yaml` renders **three Deployments**
(four if `roles.agent.enabled: true`), each with
`COREMETRY_MODE` set to its role:

| Deployment | `COREMETRY_MODE` | Default replicas | Scales? |
|---|---|---|---|
| `<release>-ingest` | `ingest` | `2` | yes — OTLP fan-in |
| `<release>-api` | `api` | `2` | yes — read HA + RPS + HPA target |
| `<release>-worker` | `worker` | `1` | **no — locked at 1, leader-elected** |
| `<release>-agent` | `agent` | `1` | optional (HA-safe, per-step Redis lock) |

`templates/service.yaml` renders **four Services** (when distributed):

| Service | Selects | Ports | Purpose |
|---|---|---|---|
| `<release>` (stable name) | **api** pods | `http` (8088) | Alias for the api role. Route / Ingress / browser / collector / MCP all point here. |
| `<release>-ingest` | ingest pods | `otlp-grpc` (4317) + `http` (8088) | OTLP/gRPC ingest + health |
| `<release>-api` | api pods | `http` (8088) | UI / REST / SSE / MCP direct |
| `<release>-worker` | worker pods | `http` (8088) | Health/observability only — **no caller routes traffic here**; stays `ClusterIP` even if `service.type` is `LoadBalancer` |

The **stable `<release>` Service aliasing the api role** is the load-
bearing detail: Route/Ingress templates reference the Service by the
stable fullname, so flipping `monolithic ↔ distributed` does **not**
require changing your Route/Ingress. In monolithic mode the same
`<release>` Service fronts the single Deployment and additionally
exposes `otlp-grpc`.

> **Fullname note:** `coremetry.fullname` collapses to the release name
> when the release name contains the chart name. So
> `helm ... install coremetry ...` names everything `coremetry`,
> `coremetry-ingest`, `coremetry-api`, `coremetry-worker`. A
> non-matching release name (`helm ... install prod ...`) yields
> `prod-coremetry`, `prod-coremetry-ingest`, etc.

### Why worker is locked at 1

Every worker job (evaluator, anomaly detectors, topology aggregation,
Drain templater, problem AI explainer) is **leader-elected via a Redis
lock**. Running more than one worker replica wastes lock-arbitration
overhead without parallelising the work. The chart enforces this by
convention (`replicas: 1` with a "DO NOT change" comment) and by the
HPA never targeting it (see below). **Redis is required** in
distributed mode — without it (no `redis.enabled` and no
`redis.external.url`) the background workers can't arbitrate and you
risk duplicate Problems.

### Contrast vs `monolithic`

| | `monolithic` (default) | `distributed` |
|---|---|---|
| Deployments | 1 (`mode=all`) | 3 (ingest/api/worker) + optional agent |
| Services | 1 (`<release>`, http + otlp-grpc) | 4 (stable api alias + per-role) |
| Replica knob | `replicaCount` (top-level) | `deployment.roles.<role>.replicas` |
| HPA target | `<release>` Deployment | `<release>-api` Deployment |
| Scale-out story | all roles together | scale receivers independently of read fleet |
| Suitable for | SME / POC / single-node | banking scale, asymmetric budgets |

Switching is a one-line `deployment.mode` change + rolling upgrade —
but see [§8 Upgrade safety](#8-upgrade-safety) for the selector
immutability caveat.

### A typical bank topology

A 5×ingest / 2×api / 1×worker layout, with per-role resource budgets
(ingest is CPU/network-heavy, api memory-heavy for serving the browser,
worker bursty CH-heavy):

```yaml
deployment:
  mode: distributed
  roles:
    ingest:
      replicas: 5
      resources:
        requests: { cpu: "1",  memory: 1Gi }
        limits:   { cpu: "4",  memory: 4Gi }
    api:
      replicas: 2
      resources:
        requests: { cpu: 500m, memory: 2Gi }
        limits:   { cpu: "2",  memory: 8Gi }
    worker:
      replicas: 1
      resources:
        requests: { cpu: 500m, memory: 2Gi }
        limits:   { cpu: "4",  memory: 16Gi }
```

- **Replicas per role:** `deployment.roles.<role>.replicas`.
- **Resources per role:** `deployment.roles.<role>.resources`. When a
  role's `resources` is `{}` it falls back to the chart-wide
  `.Values.resources` (default requests `cpu:500m / memory:1Gi`, limits
  `cpu:4 / memory:16Gi`).
- **api autoscaling:** see [§3](#3-route-vs-ingress) note and
  `autoscaling.*` — the HPA targets the api role only.

---

## 2. OpenShift restricted-v2 SCC

Coremetry is drop-in for the default `restricted-v2` SCC. The rule of
thumb: **let the platform assign the UID/fsGroup; only harden the
container.**

### `podSecurityContext` stays EMPTY — do not pin a UID

```yaml
podSecurityContext: {}   # chart default — keep it empty on OpenShift
```

`restricted-v2` injects a `runAsUser` and `fsGroup` from the project's
allocated UID range at admission time. If you pin `runAsUser` or
`fsGroup` here, you fall **outside** that range and admission rejects
the pod (`unable to validate against any security context constraint`)
unless you also bind a custom SCC. The Coremetry binary is static and
only writes to `/tmp`, so any non-root UID works — leave it empty.

### Container `securityContext` the chart already sets

```yaml
securityContext:
  runAsNonRoot: true
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities: { drop: ["ALL"] }
  seccompProfile:
    type: RuntimeDefault
```

This is applied to **every** coremetry container in every role (init
container `wait-for-clickhouse`, the main container, and the
reset-schema Job). It satisfies both `restricted-v2` (OpenShift) and
the `restricted` Pod Security Standard.

### The `/tmp` emptyDir

Because `readOnlyRootFilesystem: true`, the chart mounts an `emptyDir`
at `/tmp` so the binary still has a writable scratch path. This is in
the pod spec for all roles — do not remove it.

### What NOT to add

Do **not** add any of the following — they all require elevated SCCs
and Coremetry needs none of them:

- `hostNetwork` / `hostPID` / `hostIPC`
- `privileged: true`
- `capabilities.add` (the chart drops `ALL`; add nothing back)
- a pinned `runAsUser` / `fsGroup` in `podSecurityContext`

> **Bundled ClickHouse caveat:** if you run the in-chart ClickHouse
> StatefulSet (`clickhouse.enabled: true`), the upstream image runs as
> a fixed baked-in UID (101) and needs `anyuid` bound, e.g.
> `oc adm policy add-scc-to-user anyuid -z <release>-coremetry-clickhouse -n <ns>`.
> **At banking scale, don't — use external ClickHouse** (see
> [§5](#5-external-clickhouse--redis)). The Coremetry pods themselves
> never need `anyuid`.

---

## 3. Route vs Ingress

`route.enabled` and `ingress.enabled` are **mutually exclusive** — use
one, not both. Both target the stable `<release>` Service (the api
alias in distributed mode), so neither changes when you switch
deployment modes.

### OpenShift → Route (preferred)

```yaml
route:
  enabled: true
  host: ""                    # empty → OpenShift router auto-generates a host
  annotations: {}
  tls:
    enabled: true
    termination: edge         # edge | reencrypt | passthrough
    insecureEdgeTerminationPolicy: Redirect
ingress:
  enabled: false
```

With `tls.termination: edge` the cluster router terminates TLS using
its wildcard certificate — no cert-manager, no TLS Secret needed. Use
`reencrypt` if your security posture requires TLS all the way to the
pod (you then supply the destination CA), or `passthrough` for
end-to-end TLS terminated by Coremetry.

### Vanilla Kubernetes → Ingress

Only on non-OpenShift clusters:

```yaml
route:
  enabled: false
ingress:
  enabled: true
  className: "nginx"
  hosts:
    - host: coremetry.example.com
      paths: [{ path: /, pathType: Prefix }]
  tls:
    - secretName: coremetry-tls
      hosts: [coremetry.example.com]
```

---

## 4. Air-gapped registries

The single Coremetry image serves **all roles** (single-binary
contract). For air-gapped clusters, mirror the upstream images into
your internal registry first, then flip everything with one value.

### `global.imageRegistry` — rewrite every image

`global.imageRegistry` replaces the `.registry` of **every** image in
the chart (coremetry, ClickHouse, Redis, OTel Collector) via the
`coremetry.image` helper:

```yaml
global:
  imageRegistry: docker.internal.bank.example.com
  imagePullSecrets:
    - internal-registry-pull
```

With this set, all images resolve from the mirror, e.g.:

```
docker.internal.bank.example.com/cilcenk/coremetry:0.7.18
docker.internal.bank.example.com/clickhouse/clickhouse-server:24.8-alpine
docker.internal.bank.example.com/library/redis:7-alpine
docker.internal.bank.example.com/otel/opentelemetry-collector-contrib:0.111.0
```

**Mirror these upstream images into your registry before installing.**
(Tags above are the chart defaults; the coremetry tag defaults to
`Chart.appVersion`.)

### Per-component fallback + priority

Resolution order in `coremetry.image`:

1. `global.imageRegistry` — wins for **all** images.
2. `<component>.image.registry` — per-image override (e.g.
   `image.registry`, `clickhouse.image.registry`,
   `redis.image.registry`, `otelCollector.image.registry`).
3. empty — runtime default (`docker.io` for upstream images;
   `ghcr.io` is the coremetry default).

So you can mirror most images globally but override one component (say,
a hardened internal CH build) via its own `image.registry`.

### Pull secrets

Set `global.imagePullSecrets` for a private mirror — it's merged into
`imagePullSecrets` on every workload (and the reset-schema Job). The
per-image `image.pullSecrets` field is legacy; prefer
`global.imagePullSecrets`.

---

## 5. External ClickHouse + Redis

At banking scale, do **not** run the bundled single-node CH StatefulSet.
Point Coremetry at an external CH (Altinity Operator / ClickHouse Cloud)
and an external Redis.

### ClickHouse

```yaml
clickhouse:
  enabled: false
  external:
    # Single host, OR comma-separated seeds for a multi-node cluster —
    # the driver round-robins / fails over across them (no separate LB).
    # 9000 = plain native; 9440 = native-TLS (set secure: true).
    addr: "ch1.databases.svc:9440,ch2.databases.svc:9440,ch3.databases.svc:9440,ch4.databases.svc:9440"
  database: "coremetry"
  username: "default"
  secure: true               # native-TLS for 9440 endpoints
  insecureSkipVerify: false  # keep false in prod once a CA bundle is loaded
  maxOpenConns: 20
```

Notes grounded in the chart:

- The external value key is **`clickhouse.external.addr`** (a single
  string — single host or comma-separated seed list), **not** an
  `addresses` array. The `coremetry.clickhouseAddr` helper feeds it
  straight into the rendered ConfigMap `clickhouse.addr`.
- `clickhouse.username` defaults to `default`. The **password comes
  from a Secret** (`secrets.clickHousePassword` or
  `secrets.existingSecret`), never inline in `values.yaml`.
- An empty CH password is honoured (the bundled CH starts with
  `ALLOW_EMPTY_PASSWORD=yes`), but production CH should set one.

### Redis

```yaml
redis:
  enabled: false
  external:
    url: "redis://coremetry-redis.databases.svc:6379/0"
```

- The external value key is **`redis.external.url`**. Explicit external
  URL wins over the in-cluster Service (`coremetry.redisURL` helper).
- Redis is **mandatory** in distributed mode (leader election + SSE
  cross-pod bridge). If you provide neither `redis.enabled: true` nor
  `redis.external.url`, install proceeds but the worker can't arbitrate
  and the NOTES will warn you.

### Credentials via Secret — never inline

Pre-create a Secret and point `secrets.existingSecret` at it. When set,
the chart **skips rendering its own Secret** and every role reads
JWT / CH password / OIDC client secret / initial-admin password / ES
credentials from your Secret. Required keys:

```bash
oc create secret generic coremetry-secrets -n coremetry \
  --from-literal=jwt-secret="$(openssl rand -hex 32)" \
  --from-literal=clickhouse-password='<ch-password>' \
  --from-literal=initial-admin-password='<bootstrap-admin-pw>' \
  --from-literal=oidc-client-secret='' \
  --from-literal=es-password='' \
  --from-literal=es-api-key=''
```

```yaml
secrets:
  existingSecret: coremetry-secrets
```

> Keys the chart reads from the Secret: `jwt-secret`,
> `clickhouse-password`, `initial-admin-password`, `oidc-client-secret`
> (only when `config.auth.oidc.enabled`), `es-password`, `es-api-key`.
> All are `optional: true` env refs except `oidc-client-secret`, so an
> existing Secret that omits the unused ones still boots.

---

## 6. MCP / SSE session affinity

MCP sessions and SSE streams hold **pod-local in-memory state**. Without
stickiness, a reconnect that round-robins to a different api pod loses
the MCP session and can drop events fired during the gap.

The chart defaults to `ClientIP` affinity on the stable `<release>`
Service **and** the per-role `<release>-api` Service:

```yaml
service:
  sessionAffinity: ClientIP            # ClientIP | None
  sessionAffinityTimeoutSeconds: 10800 # 3h — covers a long on-call shift
```

- This pins a client (source-IP + dest-port tuple) to one api pod for
  the timeout window — stable MCP/SSE routing without an ingress-layer
  cookie.
- The `<release>-worker` Service takes no traffic, so affinity there is
  moot.

### NAT'd external clients → use ingress cookie stickiness

`ClientIP` affinity collapses when many external clients share one
source IP (corporate NAT/egress). In that case set
`service.sessionAffinity: None` and front the Route/ingress with
cookie-based stickiness. For an OpenShift Route, add a cookie via
`route.annotations`; for an nginx ingress:

```yaml
ingress:
  annotations:
    nginx.ingress.kubernetes.io/affinity: "cookie"
    nginx.ingress.kubernetes.io/session-cookie-name: "coremetry-route"
    nginx.ingress.kubernetes.io/session-cookie-max-age: "10800"
```

---

## 7. Destructive `clickhouse.resetSchema`

```yaml
clickhouse:
  resetSchema: false   # keep false in steady state
```

When `true`, the chart renders a **`pre-install` + `pre-upgrade` Helm
hook Job** (`<release>-reset-schema`) that runs `coremetry
--reset-schema` against the configured CH (in-cluster or external)
**before** the app pods start. This **drops the entire database** —
every span, log, metric, dashboard, and audit event.

- It's idempotent (`DROP DATABASE IF EXISTS`), but it runs on **every**
  `helm upgrade` while the flag stays `true` — so every upgrade re-wipes
  the database.
- **Flip it back to `false` immediately after the first install**, or
  you will keep losing data on every subsequent `helm upgrade`.
- The Job uses the same image, ConfigMap, and Secret as the main
  Deployment, so credentials/addr/database match exactly.

Treat `resetSchema: true` as a one-shot first-install convenience (or a
deliberate "redeploy from scratch") only.

---

## 8. Upgrade safety

### Selector immutability

Deployment/StatefulSet `spec.selector` is **immutable** in Kubernetes.
Any chart change that alters selector labels — including
`app.kubernetes.io/component` (added in chart `0.1.3`), and switching
`deployment.mode` between `monolithic` and `distributed` (the component
suffix changes: `coremetry` ↔ `coremetry-api`/`-ingest`/`-worker`) —
**cannot** be applied with a plain `helm upgrade`. You must
**uninstall + reinstall**:

```bash
helm uninstall coremetry -n coremetry
helm upgrade --install coremetry oci://ghcr.io/cilcenk/charts/coremetry \
  --version <new-version> -n coremetry -f values-openshift.yaml
```

The **ClickHouse PVC survives** an uninstall (StatefulSet PVC retention),
so your warm store — every span/log/metric/dashboard — is preserved.
Verify before/after with:

```bash
oc get pvc -n coremetry
```

With **external** CH/Redis (the recommended banking layout), there is no
in-cluster PVC at all — your data lives entirely outside the chart's
lifecycle, so uninstall/reinstall is fully safe.

### `Chart.yaml` version vs appVersion

`Chart.yaml` keeps `version` and `appVersion` in sync (both `0.7.18`
today): `version` is the chart's own SemVer, `appVersion` is the
Coremetry image tag (`image.tag` defaults to `appVersion`). When pinning
in production, pin `--version` to the chart `version`. Bump both
together when shipping a chart change tied to an app release.

---

## 9. Render smoke test (before you apply)

Validate the manifests with `helm lint` + `helm template` before
touching the cluster. Expected output is called out per command.

```bash
# Lint the chart
helm lint charts/coremetry -f values-openshift.yaml

# Distributed render — assert the 3 coremetry ROLE Deployments are present.
# (Don't assert a raw `kind: Deployment` count: a full render also includes the
#  bundled OTel Collector Deployment, plus — unless you disable them for external
#  stores — a Redis Deployment + a ClickHouse StatefulSet. So the raw totals are
#  ~5 Deployments / 7 Services by default, or 4 Deployments / 5 Services with
#  external CH+Redis. The meaningful distributed-mode assertion is the 3 roles:)
helm template coremetry charts/coremetry \
  --set deployment.mode=distributed \
  | grep -E 'name: coremetry-(ingest|api|worker)$' | sort -u
#   →  coremetry-ingest / coremetry-api / coremetry-worker   (3 role Deployments)
#      + 4 Services: the stable <release> alias→api, plus -ingest / -api / -worker.

# Confirm COREMETRY_MODE is set per role
helm template coremetry charts/coremetry --set deployment.mode=distributed \
  | grep -A1 'name: COREMETRY_MODE'
#   → value: "ingest" / "api" / "worker"

# Air-gap rewrite — every image flips to the mirror
helm template coremetry charts/coremetry \
  --set global.imageRegistry=docker.internal.bank.example.com \
  | grep 'image:'
#   → all images prefixed with docker.internal.bank.example.com/

# reset-schema Job renders ONLY when the flag is set
helm template coremetry charts/coremetry --set clickhouse.resetSchema=true \
  | grep -c 'component: reset-schema'   #  → non-zero (Job present)
helm template coremetry charts/coremetry \
  | grep -c 'component: reset-schema'   #  → 0 (no Job by default)

# HPA targets the api role in distributed mode
helm template coremetry charts/coremetry \
  --set deployment.mode=distributed --set autoscaling.enabled=true \
  | grep -A4 'kind: HorizontalPodAutoscaler'
#   → name: coremetry-api ; scaleTargetRef → Deployment/coremetry-api
```

---

## 10. Complete example

`values-openshift.yaml` — distributed, Route with edge TLS, external
CH/Redis via an existing Secret, `ClientIP` session affinity,
restricted-v2-friendly (empty `podSecurityContext`):

```yaml
# values-openshift.yaml
deployment:
  mode: distributed
  roles:
    ingest:
      replicas: 5
      resources:
        requests: { cpu: "1",  memory: 1Gi }
        limits:   { cpu: "4",  memory: 4Gi }
    api:
      replicas: 2
      resources:
        requests: { cpu: 500m, memory: 2Gi }
        limits:   { cpu: "2",  memory: 8Gi }
    worker:
      replicas: 1                       # leader-elected — do not change

# restricted-v2: leave pod-level SC empty so the SCC injects UID/fsGroup.
podSecurityContext: {}

# OpenShift Route (edge TLS via cluster router) — NOT ingress.
route:
  enabled: true
  host: ""                             # router auto-generates
  tls:
    enabled: true
    termination: edge
    insecureEdgeTerminationPolicy: Redirect
ingress:
  enabled: false

# MCP / SSE stickiness.
service:
  sessionAffinity: ClientIP
  sessionAffinityTimeoutSeconds: 10800

# Autoscale the api read fleet (HPA targets the api role automatically).
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
  targetMemoryUtilizationPercentage: 80

# External ClickHouse (multi-node native-TLS seeds) — no bundled CH.
clickhouse:
  enabled: false
  resetSchema: false                   # MUST stay false except first install
  external:
    addr: "ch1.databases.svc:9440,ch2.databases.svc:9440,ch3.databases.svc:9440"
  database: "coremetry"
  username: "default"
  secure: true
  insecureSkipVerify: false

# External Redis — no bundled Redis.
redis:
  enabled: false
  external:
    url: "redis://coremetry-redis.databases.svc:6379/0"

# Credentials from a pre-created Secret — never inline.
secrets:
  existingSecret: coremetry-secrets

# Air-gapped mirror (drop the global block on a connected cluster).
global:
  imageRegistry: docker.internal.bank.example.com
  imagePullSecrets:
    - internal-registry-pull

config:
  auth:
    initialAdmin: "platform-oncall@bank.example.com"
```

Pre-create the Secret, then install:

```bash
oc new-project coremetry      # or: oc project coremetry

oc create secret generic coremetry-secrets -n coremetry \
  --from-literal=jwt-secret="$(openssl rand -hex 32)" \
  --from-literal=clickhouse-password='<ch-password>' \
  --from-literal=initial-admin-password='<bootstrap-admin-pw>' \
  --from-literal=oidc-client-secret='' \
  --from-literal=es-password='' \
  --from-literal=es-api-key=''

helm upgrade --install coremetry \
  oci://ghcr.io/cilcenk/charts/coremetry \
  --version 0.7.18 \
  -n coremetry --create-namespace \
  -f values-openshift.yaml
```

Post-install:

```bash
# Roll out the api fleet (the others come up in parallel)
oc rollout status deploy/coremetry-api -n coremetry

# Confirm the topology — the 3 role Deployments (ingest/api/worker) + their 4
# Services (stable alias→api, -ingest, -api, -worker). The OTel Collector (and,
# if not external, ClickHouse/Redis) also appear under this label.
oc get deploy,svc -n coremetry -l app.kubernetes.io/name=coremetry

# Get the Route URL
oc get route coremetry -n coremetry -o jsonpath='{.spec.host}{"\n"}'

# Point apps' OTLP/gRPC at the ingest Service
#   OTEL_EXPORTER_OTLP_ENDPOINT=http://coremetry-ingest:4317
```

Sign in with the bootstrap admin (`config.auth.initialAdmin` /
`initial-admin-password` from the Secret) and rotate the password
immediately.
