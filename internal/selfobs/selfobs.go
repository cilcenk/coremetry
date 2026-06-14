// Package selfobs wires Coremetry's own OTel SDK (traces + metrics)
// so the running binary emits telemetry about itself — appears on
// /services alongside the demo + production traffic it observes.
//
// v0.6.42 — operator-reported gap: the frontend already shipped with
// @opentelemetry/sdk-trace-web (lib/otel.ts) and stamped a W3C
// `traceparent` on every fetch, but the Go backend had no SDK on the
// receiving side, so the trace context died at the HTTP boundary and
// `coremetry-api` / `coremetry-ingest` / `coremetry-worker` /
// `coremetry-mcp` never showed up as services.
//
// Design choices:
//
//   • Opt-in via env. COREMETRY_SELF_OBS_OTLP_ENDPOINT empty → SDK
//     not initialised; binary behaves exactly as before. Set it to
//     `localhost:4317` (the docker-compose otel-collector) to turn
//     on self-observability.
//
//   • One TracerProvider + one MeterProvider per process. They are
//     stored on the global otel package so otelhttp / otelgrpc /
//     chstore.tracedConn pick them up without explicit plumbing.
//
//   • Resource attributes derived from runMode so each role
//     surfaces as a distinct `service.name` on /services:
//       - all:    coremetry-monolithic
//       - api:    coremetry-api
//       - ingest: coremetry-ingest
//       - worker: coremetry-worker
//     `service.instance.id` is the hostname (so multiple replicas
//     can be told apart on /services rows).
//
//   • Sampling default: ParentBased(TraceIDRatioBased(0.1)). If the
//     frontend's traceparent indicates "sampled", we follow; root
//     spans the backend creates itself get sampled at 10%. Operator
//     can override via COREMETRY_SELF_OBS_SAMPLE_RATE.
//
//   • Self-loop guard: this package is NEVER imported by code that
//     runs inside the OTLP receiver paths (`internal/otlp/*`). The
//     receiver-side instrumentation is enabled only outside ingest
//     mode — see main.go around `mode.ingest` for the gate. Without
//     that, the ingester would emit spans about receiving spans,
//     re-enter itself, and amplify.
//
//   • Metrics — runtime + process counters via go.opentelemetry.io/
//     contrib/instrumentation/runtime. Periodic export every 30s.
//     Custom counters / histograms register against the package's
//     global meter (returned by `Meter()`) so call sites don't
//     repeat the package path.
package selfobs

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	envEndpoint   = "COREMETRY_SELF_OBS_OTLP_ENDPOINT"
	envSampleRate = "COREMETRY_SELF_OBS_SAMPLE_RATE"
	envVersion    = "COREMETRY_VERSION"
)

var (
	enabled     bool
	tracerSink  trace.Tracer
	meterSink   metric.Meter
	tpShutdown  func(context.Context) error
	mpShutdown  func(context.Context) error
)

// Init constructs both providers when COREMETRY_SELF_OBS_OTLP_ENDPOINT
// is set. Returns a shutdown function the caller defers; safe to call
// even when disabled (no-op shutdown).
//
// mode is the running role string ("api", "ingest", "worker", "all")
// — used to derive service.name. version is the binary version.
func Init(ctx context.Context, mode, version string) func(context.Context) error {
	endpoint := strings.TrimSpace(os.Getenv(envEndpoint))
	if endpoint == "" {
		log.Printf("[selfobs] %s unset — self-observability disabled", envEndpoint)
		tracerSink = noop.NewTracerProvider().Tracer("coremetry")
		meterSink = noopMeter{}
		return func(context.Context) error { return nil }
	}
	enabled = true

	res, err := buildResource(mode, version)
	if err != nil {
		log.Printf("[selfobs] resource: %v — disabled", err)
		tracerSink = noop.NewTracerProvider().Tracer("coremetry")
		meterSink = noopMeter{}
		return func(context.Context) error { return nil }
	}

	// ── Traces ────────────────────────────────────────────────────
	// v0.6.45 — WithInsecure(), NOT WithDialOption(insecure creds).
	// The first cut (v0.6.42) used WithDialOption which left
	// otlptracegrpc's own TLS default in place, so the client tried
	// a TLS handshake against the collector's plaintext OTLP port →
	// "tls: first record does not look like a TLS handshake" and
	// every export hit the 10s deadline. WithInsecure flips the
	// transport to h2c, matching the metric exporter below.
	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
		// Block until first connect would deadlock when the collector
		// boots in the same docker-compose; the default async dial
		// retries indefinitely which is what we want for a sidecar
		// collector.
	)
	if err != nil {
		log.Printf("[selfobs] trace exporter: %v — disabled", err)
		tracerSink = noop.NewTracerProvider().Tracer("coremetry")
		meterSink = noopMeter{}
		return func(context.Context) error { return nil }
	}

	sampleRate := parseSampleRate()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp,
			sdktrace.WithMaxExportBatchSize(512),
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))),
	)
	otel.SetTracerProvider(tp)
	tpShutdown = tp.Shutdown
	tracerSink = tp.Tracer("coremetry")

	// ── Metrics ──────────────────────────────────────────────────
	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		log.Printf("[selfobs] metric exporter: %v — metrics off but traces on", err)
		meterSink = noopMeter{}
	} else {
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
				sdkmetric.WithInterval(30*time.Second))),
		)
		otel.SetMeterProvider(mp)
		mpShutdown = mp.Shutdown
		meterSink = mp.Meter("coremetry")

		// Runtime metrics — heap, goroutines, GC. Same MeterProvider,
		// no extra wiring required at call sites.
		if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(15 * time.Second)); err != nil {
			log.Printf("[selfobs] runtime metrics: %v", err)
		}
	}

	log.Printf("[selfobs] enabled — endpoint=%s service=%s sample=%.2f",
		endpoint, serviceName(mode), sampleRate)
	return shutdownAll
}

// Tracer returns the package-wide tracer. Noop when disabled.
func Tracer() trace.Tracer { return tracerSink }

// Meter returns the package-wide meter. Noop when disabled.
func Meter() metric.Meter { return meterSink }

// Enabled reports whether real OTel pipelines are running. Hot paths
// can fast-skip span work when this is false (the noop tracer already
// does so internally; this is just for the rare case where building
// expensive attribute values would itself be wasteful).
func Enabled() bool { return enabled }

func buildResource(mode, version string) (*resource.Resource, error) {
	host, _ := os.Hostname()
	if version == "" {
		version = strings.TrimSpace(os.Getenv(envVersion))
	}
	if version == "" {
		version = "dev"
	}
	// v0.8.x bug-fix: use NewSchemaless for the custom attrs, NOT
	// NewWithAttributes(semconv.SchemaURL, …). resource.Merge ERRORS when
	// both sides carry a different non-empty schema URL — resource.Default()
	// pins the SDK's bundled semconv schema (e.g. .../1.26.0) while the
	// imported semconv package may be newer (.../1.37.0). When those drifted
	// (a dependency bump, 2026-05-31) the merge failed → buildResource
	// returned an error → selfobs silently fell back to noop and the backend
	// coremetry-api/worker/agent self-telemetry stopped emitting. A schemaless
	// resource has an empty schema URL, so the merge keeps Default()'s URL
	// without conflict and stays robust to future SDK/semconv version skew.
	// (service.name/version/instance keys are stable across semconv versions.)
	return resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName(mode)),
		semconv.ServiceVersion(version),
		semconv.ServiceInstanceID(host),
		semconv.DeploymentEnvironment(strings.TrimSpace(os.Getenv("COREMETRY_DEPLOY_ENV"))),
	))
}

func serviceName(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "all", "":
		return "coremetry-monolithic"
	case "api":
		return "coremetry-api"
	case "ingest":
		return "coremetry-ingest"
	case "worker":
		return "coremetry-worker"
	default:
		return "coremetry-" + mode
	}
}

func parseSampleRate() float64 {
	raw := strings.TrimSpace(os.Getenv(envSampleRate))
	if raw == "" {
		return 0.1
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || v > 1 {
		log.Printf("[selfobs] bad %s=%q — falling back to 0.1", envSampleRate, raw)
		return 0.1
	}
	return v
}

func shutdownAll(ctx context.Context) error {
	var errs []string
	if tpShutdown != nil {
		if err := tpShutdown(ctx); err != nil {
			errs = append(errs, "trace: "+err.Error())
		}
	}
	if mpShutdown != nil {
		if err := mpShutdown(ctx); err != nil {
			errs = append(errs, "metric: "+err.Error())
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("selfobs shutdown: %s", strings.Join(errs, "; "))
}

// noopMeter — minimal implementation we can return when the metric
// exporter fails to construct. Each instrument constructor returns an
// instrument that does nothing on Add/Record. Lets call sites use
// `selfobs.Meter()` unconditionally without branching on enabled.
type noopMeter struct{ metric.Meter }
