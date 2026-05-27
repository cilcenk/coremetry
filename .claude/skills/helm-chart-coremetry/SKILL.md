---
name: helm-chart-coremetry
description: Coremetry-specific Helm chart guardrails — OpenShift restricted-v2 SCC compatibility, global.imageRegistry air-gapped rewrite, external ClickHouse / Redis config, monolithic vs distributed deployment.mode (v0.6.2), MCP/SSE session affinity (v0.6.21), Reset-schema hook, Route vs Ingress decision, version-bump rules. Use BEFORE any change under `charts/coremetry/`, `examples/openshift/`, or anything that touches the chart's deploy / ingress / OpenShift Route shape. Triggers on words like "helm", "values.yaml", "deployment.mode", "OpenShift", "SCC", "global.imageRegistry", "external ClickHouse", "external Redis", "Route", "Ingress", "reset-schema", "Chart.yaml", "session affinity", "sticky session".
---

# /helm-chart-coremetry — Coremetry chart guardrails

The chart is the canonical Coremetry install path for production
(`charts/coremetry/`). Banks running OpenShift restricted-v2 SCC,
air-gapped registries, external ClickHouse + Redis, and either
monolithic-pod OR distributed scale-out all deploy from the same
chart. Generic Helm advice misses what makes this chart work
across that matrix.

**Read this before any change to `charts/coremetry/`.**

## When to use

- Adding / modifying a template under `charts/coremetry/templates/`
- Changing `values.yaml` defaults or structure
- Touching `Chart.yaml` version
- Adding an OpenShift-specific manifest
- Wiring a new external dependency (CH cluster, Redis Sentinel, etc.)
- Cutting a chart release (`helm package` + OCI push to GHCR)
- Reviewing a "chart broke on OpenShift" / "chart broke air-gapped"
  bug report

## Steps

### 1. OpenShift restricted-v2 SCC compatibility

OpenShift's `restricted-v2` SCC is the default for unprivileged
projects. The chart MUST render manifests that the SCC will admit
without warnings.

**Required:**

- `podSecurityContext: {}` — leave EMPTY by default. OpenShift's
  SCC injects the UID + fsGroup at admission time from the
  project's allocated range. Pinning `runAsUser` forces an SCC
  binding the operator probably doesn't have.
- `securityContext` (container-level) — set these EXACTLY:
  ```yaml
  securityContext:
    runAsNonRoot: true
    readOnlyRootFilesystem: true
    allowPrivilegeEscalation: false
    capabilities: { drop: ["ALL"] }
    seccompProfile:
      type: RuntimeDefault
  ```
- **Don't** add `runAsUser: <number>`. Don't `runAsGroup`.
  Don't `fsGroup` at the pod level. SCC picks them.
- **Don't** add `hostNetwork`, `hostPID`, `privileged: true`,
  `allowPrivilegeEscalation: true`, or any capability ADD —
  restricted-v2 rejects all of these.

The Coremetry binary mounts `tmp` as `emptyDir` so `readOnlyRoot`
works (the static binary writes only to /tmp). If a future change
needs a writable extra path, add another `emptyDir` volume — never
flip `readOnlyRootFilesystem` to false.

### 2. global.imageRegistry — air-gapped rewrite rule

Banks running air-gapped clusters mirror upstream images into an
internal registry. The chart MUST support a single env-var-style
rewrite that flips EVERY image reference to the internal registry.

The pattern (already implemented in `_helpers.tpl::coremetry.image`):

```yaml
global:
  imageRegistry: ""       # default: empty → docker.io / GHCR
  imagePullSecrets: []
```

Resolution order: `global.imageRegistry` (when set) → wins for
EVERY image. Per-component `.image.registry` falls back to it.
Tags fall back to `.Chart.AppVersion`.

**When adding a new chart-managed image:**
- Use `{{ include "coremetry.image" (dict "imageRoot" .Values.<component>.image "global" .Values.global "defaultTag" "<pin>") }}` — never `image: <registry>/<repo>:<tag>` hardcoded.
- Document the mirror path in values.yaml comments (banks have
  to mirror upstream before deploying).

`imagePullSecrets` follows the same pattern via
`coremetry.imagePullSecrets` — merges global + per-image.

### 3. deployment.mode — monolithic vs distributed (v0.6.2)

Coremetry binary supports four roles via `COREMETRY_MODE` env
var (v0.6.0). The chart picks the topology via:

```yaml
deployment:
  mode: monolithic    # OR "distributed"
  roles:
    ingest:  { replicas: 2, resources: {} }
    api:     { replicas: 2, resources: {} }
    worker:  { replicas: 1, resources: {} }   # MUST stay 1
```

**Monolithic** — `charts/coremetry/templates/deployment.yaml`
renders one Deployment, `COREMETRY_MODE` unset (= "all"). This
is unchanged from v0.5.x; in-place upgrades work.

**Distributed** —
`charts/coremetry/templates/deployment-distributed.yaml` renders
three Deployments (ingest / api / worker). Worker replicas locked
at 1 (leader-elected jobs). The stable `<release>` Service is
ALIASED to the api role in distributed mode so existing
ingress/route references keep working.

**When adding a chart resource that needs the api fleet:**
- Service / Ingress / Route → target `<release>` (alias). Works
  in both modes.
- HPA / NetworkPolicy → target `<release>-api` directly in
  distributed mode (already handled by hpa.yaml v0.6.2).
- Background-job depends-on → use `<release>-worker` (singleton).

### 4. Session affinity for MCP + SSE (v0.6.21)

MCP sessions and SSE streams hold pod-local in-memory state. A
re-routed reconnect loses the session.

`values.yaml`:
```yaml
service:
  sessionAffinity: ClientIP       # ClientIP | None
  sessionAffinityTimeoutSeconds: 10800   # 3h
```

`service.yaml` emits the affinity block on the stable + per-role
Services when not "None". When changing the chart to add a new
Service that serves MCP / SSE — mirror the same affinity block.

For external clients behind NAT (which collapses ClientIP), point
operators at the nginx-ingress sticky-cookie annotation block in
NOTES.txt (cookie-based stickiness survives NAT).

### 5. External ClickHouse / Redis — opt-out the bundled sub-chart

The chart bundles a single-pod ClickHouse + Redis for evaluation
installs. Banks point at their own clusters:

```yaml
clickhouse:
  enabled: false                              # disable bundled CH
  external:
    addr: "ch1:9000,ch2:9000,ch3:9000"        # seed list — driver round-robins
    database: "coremetry"
    username: "coremetry"

secrets:
  clickhousePassword: "..."                   # via Secret or existingSecret

redis:
  enabled: false
  external:
    url: "redis://redis.bank.internal:6379/0"
```

**When adding a new external-dep knob:**
- Mirror the `<dep>.enabled + <dep>.external.<addr/url/etc.>`
  pattern. Defaults match the bundled sub-chart's coordinates so
  flipping `enabled: false` and not setting external feels
  broken (intentional — surfaces misconfig at deploy time).
- Sensitive fields (passwords, API keys) MUST go through the
  Secret indirection — never inline in values.yaml.
- Use `secrets.existingSecret` so banks can manage credentials
  in their own secret management system without forking the
  chart.

### 6. Reset-schema hook — DESTRUCTIVE, pre-install only

```yaml
clickhouse:
  resetSchema: true
```

Triggers a pre-install Hook Job (`reset-schema-job.yaml`) that
runs `coremetry --reset-schema` against the configured CH database
+ exits. Drops the database + lets `chstore.New()` recreate it
at next boot. ALL ingested data is lost.

**Rules:**
- `resetSchema: false` MUST be the default. Always.
- Only useful on FIRST install. Document this in values.yaml
  AND on the hook job (already done — see existing comment).
- Banks using the chart for upgrades MUST set `resetSchema: false`
  explicitly to avoid an "operator typo wipes prod" scenario.
- NEVER add a similar "drop everything" hook for any other dep —
  CH is uniquely safe because the bundled CH is a fresh-install
  affordance, not a long-lived data store.

### 7. Route vs Ingress — pick one, not both

- **Ingress** (`ingress.enabled: true`) — vanilla k8s, NGINX-class
  controllers, optional TLS via cert-manager.
- **Route** (`route.enabled: true`) — OpenShift only, leverages
  cluster cert + the OpenShift router. Preferred on OCP because
  the router handles edge TLS automatically.

The chart MUST treat them as mutually exclusive. Both `enabled:
true` produces two paths competing for the same hostname. Add a
chart-render-time assertion if a future contributor mixes them.

**When extending the chart's external surface:**
- Add to BOTH the Ingress template AND the Route template (they
  expose the same Service).
- Test the rendered manifest with `helm template charts/coremetry
  --set ingress.enabled=true` and `--set route.enabled=true`.

### 8. Chart.yaml version vs appVersion

`Chart.yaml` has `version` (chart) and `appVersion` (binary).
Coremetry keeps them IN SYNC via the release process — both
bumped together when chart-visible changes ship.

```yaml
version: 0.6.21
appVersion: "0.6.21"
```

**Bump rules:**
- ANY chart template change (new field, new resource, default
  flip) → bump both `version` and `appVersion`.
- ANY binary-side change that the chart REFERENCES (new env var,
  new endpoint) → bump both.
- Binary-only change with no chart reference → chart version
  stays. NOTES.txt or value comments referencing the version are
  STILL ok to update without a bump (text only).

Per the release skill, `helm package` + OCI push to
`oci://ghcr.io/cilcenk/charts/coremetry` runs on tag push. If the
chart version doesn't change, the push is a no-op for the chart
artifact (GHCR rejects duplicate version tags).

### 9. Migration / upgrade safety

The Deployment selector changed in chart 0.1.3 (added
`app.kubernetes.io/component`) — selectors are IMMUTABLE post-
create. NOTES.txt + README document the `helm uninstall +
reinstall` path; PVC stays so CH data survives.

**When adding to selectors:**
- Don't. Use labels for new metadata. The selector is locked.
- If a future change ABSOLUTELY needs a new selector dimension,
  bump the chart MAJOR version AND document the uninstall +
  reinstall path in NOTES.txt the same way 0.1.3 did.

### 10. Helm-render smoke test before tagging

```
helm lint charts/coremetry
helm template charts/coremetry > /tmp/mono.yaml
helm template charts/coremetry --set deployment.mode=distributed > /tmp/dist.yaml
helm template charts/coremetry --set clickhouse.resetSchema=true | grep -A 5 "kind: Job"
helm template charts/coremetry --set global.imageRegistry=my.registry.internal | grep "image:"
```

All four should pass. Spot-check that:
- Distributed mode produces 3 Deployments + 4 Services.
- Reset-schema hook only renders when explicitly set.
- Air-gapped registry override rewrites EVERY image reference.

## Anti-patterns

- **Hardcoded `runAsUser: 1000` (or any specific UID).** Breaks
  restricted-v2 SCC. The container is non-root by design (USER
  65532 in Dockerfile), but the SCC picks its own range. Let it.
- **Adding a `hostNetwork`, `hostPath`, `hostPID`, or
  `privileged` field.** Restricted-v2 rejects all four.
- **Inlining secrets in values.yaml.** Always go through
  `secrets.<name>` + the Secret template OR
  `secrets.existingSecret` (bank-managed). README's "production
  install" example sets `secrets.jwtSecret="$(openssl rand -hex
  32)"` as the demo path — that's a CLI override, not the
  default.
- **`securityContext.runAsNonRoot: false`.** SCC rejects on
  OpenShift; not needed elsewhere either (the binary doesn't
  need root).
- **Adding `priorityClassName` without a fallback.** Banks
  running custom PriorityClass schemes will fail admission if
  the chart pins a name that doesn't exist in their cluster.
  Make it `priorityClassName: "{{ .Values.priorityClassName }}"`
  with a `default: ""` (empty → no priority class).
- **Reading from `.Capabilities.KubeVersion` for feature gating.**
  Operators run k8s 1.19+ for OpenShift 4.x compatibility; new-
  feature dependencies should fall back to working manifests on
  the floor version, not error out via Capabilities checks.
- **Cluster-scoped resources (ClusterRole / ClusterRoleBinding /
  PSP) without an explicit opt-out.** Some banks deploy multiple
  Coremetry instances per cluster; cluster-scoped resources
  collide. Use `Role` + `RoleBinding` per namespace; gate any
  truly-cluster-scoped resource on a value `cluster.enabled:
  false` default.
- **Chart version-bump without binary-version-bump.** Drift
  between chart and binary versions historically caused
  "operator deployed chart 0.6.10 with v0.5.487 binary" surprises.
  See `Chart.yaml` v0.5.0 → v0.6.2 → v0.6.21 catch-up history.

## Hard-constraint reminders

- **Single-binary contract.** Even in distributed mode, all three
  Deployments run the SAME image. Don't fork the chart to deploy
  different images per role.
- **No multi-tenant.** Coremetry stays single-tenant per install.
  Don't add chart values that look like multi-tenancy
  (`tenants:` lists, per-tenant CH databases). The pitch
  depends on this.
- **Audit-verify-context.** When a chart bug report comes in,
  read ±10 lines around the suspected template fragment before
  recommending a fix. External Helm-chart linters frequently
  flag intentional patterns as warnings.

## Historical incidents — read these before guessing

- **chart 0.1.3** — Deployment selector adopted ClickHouse /
  Redis pods because the selector lacked `app.kubernetes.io/
  component`. Service routed traffic to the wrong pods, traces
  silently dropped. Selector is immutable post-create — fix
  required uninstall + reinstall + new chart-major.
- **v0.5.388 / v0.5.424** — cluster topology probe timed out
  on busy CH at the 3s default; misconfig banner false-fired.
  Bumped to 8s in the chart's connection probe config.
- **v0.5.428 / v0.5.439** — added cluster-probe cache + stale
  fallback so a transient timeout doesn't blink the topology
  banner red.
- **v0.6.2** — Helm chart `deployment.mode: monolithic |
  distributed` toggle shipped. Stable Service aliases the api
  role in distributed mode so ingress/route don't change shape.
- **v0.6.21** — `service.sessionAffinity: ClientIP` default
  added because MCP/SSE sessions are pod-local. NOTES.txt
  cookie-based ingress example for NAT'd external clients.

## Don't

- **Don't add `Chart.lock` to `.gitignore`.** Chart deps lock
  reproducibility — commit it.
- **Don't `helm package` from a dirty working tree.** The chart
  push to GHCR is the canonical artifact; an uncommitted change
  becomes invisible after the tag.
- **Don't introduce sub-charts via the dependencies path.**
  Banks must mirror EVERY image; sub-chart dependencies obscure
  this and break air-gapped installs. Bundle the manifests
  inline.
