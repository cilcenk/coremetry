# Coremetry Helm chart

Enterprise OpenTelemetry APM — traces, metrics, logs, profiles on
ClickHouse. One image, one tag, one release. The same single binary
runs as a monolithic POC install or scales out into role-split
Deployments for billion-span-a-day production.

- Chart `version` / Coremetry `appVersion`: see [`Chart.yaml`](Chart.yaml).
- Source: <https://github.com/cilcenk/coremetry>

## Quick start (monolithic)

```bash
helm upgrade --install coremetry \
  oci://ghcr.io/cilcenk/charts/coremetry \
  -n coremetry --create-namespace
```

This brings up one Coremetry Deployment plus the bundled ClickHouse,
Redis, and OTel Collector — suitable for SME / POC / single-node
installs. See [`templates/NOTES.txt`](templates/NOTES.txt) (printed
after install) for sign-in, OTLP endpoint, and HA notes.

## Deployment modes

`deployment.mode` selects the topology:

- **`monolithic`** (default) — one Deployment, `COREMETRY_MODE=all`.
  `replicaCount` applies. One Service fronts UI/API + OTLP/gRPC.
- **`distributed`** — three Deployments (ingest / api / worker) running
  the same image in different roles via `COREMETRY_MODE`, plus four
  Services. Worker is locked at 1 replica (leader-elected via Redis);
  the HPA targets the api role; the stable `<release>` Service aliases
  the api role so Route/Ingress don't change.

## Deploying on OpenShift (distributed)

Production-grade, real-bank guidance — restricted-v2 SCC, air-gapped
registries, external ClickHouse + Redis, OpenShift Route, MCP/SSE
session affinity, destructive reset-schema, upgrade safety, render
smoke tests, and a complete `values-openshift.yaml`:

→ **[docs/openshift-distributed.md](docs/openshift-distributed.md)**

For flat (non-Helm) OpenShift manifests, see
[`examples/openshift/`](../../examples/openshift/README.md).

## Configuration

All knobs and their defaults are documented inline in
[`values.yaml`](values.yaml). Highlights:

| Area | Key(s) |
|---|---|
| Topology | `deployment.mode`, `deployment.roles.*.replicas`, `deployment.roles.*.resources` |
| Image / air-gap | `image.*`, `global.imageRegistry`, `global.imagePullSecrets` |
| External ClickHouse | `clickhouse.enabled`, `clickhouse.external.addr`, `clickhouse.secure` |
| External Redis | `redis.enabled`, `redis.external.url` |
| Secrets | `secrets.existingSecret` (preferred) or inline `secrets.*` |
| Exposure | `route.enabled` (OpenShift) or `ingress.enabled` (vanilla k8s) |
| MCP / SSE stickiness | `service.sessionAffinity`, `service.sessionAffinityTimeoutSeconds` |
| Autoscaling | `autoscaling.*` (targets the api role in distributed mode) |
| Destructive reset | `clickhouse.resetSchema` (keep `false` except first install) |
