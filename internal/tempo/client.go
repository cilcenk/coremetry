// Package tempo implements a read-through client for an external
// Grafana Tempo deployment (v0.5.189). Used as a fallback for
// trace-by-id lookups when Coremetry sampled the trace out — many
// operators keep 100% retention in Tempo (+ S3) while running
// Coremetry at 5% for affordable hot-path observability. Without
// this, the operator hits "trace not found" on every long-tail ID
// and has to switch tabs to Grafana. With it, the same `/trace?id=`
// URL silently falls back, with a banner explaining where the
// data came from so the operator isn't misled about coverage.
package tempo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Settings is the persisted Tempo backend config. Only one Tempo
// endpoint per Coremetry install today; if federation becomes a
// real need, extend to a list.
type Settings struct {
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"baseUrl"`
	// AuthType — none | bearer | basic. Bearer covers most
	// Grafana Cloud setups (Tempo API key as Bearer); basic
	// covers self-hosted Tempo behind nginx with htpasswd.
	AuthType string `json:"authType,omitempty"`
	// Token holds the bearer token OR the basic-auth password.
	// Never echoed in Snapshot() responses — the UI only sees
	// HasToken so the operator can tell if one is configured.
	Token    string `json:"token,omitempty"`
	// Username — only used for basic auth.
	Username string `json:"username,omitempty"`
	// OrgID — X-Scope-OrgID header for multi-tenant Tempo (Grafana
	// Cloud requires this; self-hosted single-tenant ignores it).
	OrgID    string `json:"orgId,omitempty"`
}

// Snapshot is the version returned by GET /api/settings/tempo.
// Mirrors Settings but masks the token + adds a HasToken signal.
type Snapshot struct {
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"baseUrl"`
	AuthType string `json:"authType,omitempty"`
	HasToken bool   `json:"hasToken"`
	Username string `json:"username,omitempty"`
	OrgID    string `json:"orgId,omitempty"`
}

// Service is the per-process Tempo client. Holds the live config
// + an HTTP client tuned for typical Tempo S3-backed cold lookup
// latency (1-5s in practice; we allow up to 30s for the long tail).
type Service struct {
	mu  sync.RWMutex
	cfg Settings
	cli *http.Client
}

func New() *Service {
	return &Service{
		cli: &http.Client{Timeout: 30 * time.Second},
	}
}

// settingsStore is the narrow chstore interface this package uses
// to persist its config. Lets the tempo package avoid importing the
// concrete *chstore.Store and prevents an import cycle if the
// store ever takes a dependency back on tempo.
type settingsStore interface {
	GetTempoSettingsRaw(ctx context.Context) ([]byte, error)
	PutTempoSettingsRaw(ctx context.Context, raw []byte) error
}

// LoadPersisted hydrates the in-memory config from the saved JSON
// blob in system_settings. Missing blob = empty config (Configured
// reports false; lookups skip the HTTP attempt). Called once at
// boot from main(); safe to call again on demand if the operator
// wants a hard re-read.
func (s *Service) LoadPersisted(ctx context.Context, store settingsStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetTempoSettingsRaw(ctx)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var cfg Settings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("tempo decode: %w", err)
	}
	s.Configure(cfg)
	return nil
}

// SavePersisted writes the typed config to system_settings. The
// HTTP handler calls this after merging the operator's submitted
// payload with the previously stored token (so a partial update
// without a new token doesn't blank the existing one).
func (s *Service) SavePersisted(ctx context.Context, store settingsStore, cfg Settings) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := store.PutTempoSettingsRaw(ctx, raw); err != nil {
		return err
	}
	s.Configure(cfg)
	return nil
}

// Configure swaps the live config. Called by SavePersisted +
// LoadPersisted; safe for concurrent reads via the RWMutex.
func (s *Service) Configure(cfg Settings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// Snapshot returns the public config view (no token).
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{
		Enabled:  s.cfg.Enabled,
		BaseURL:  s.cfg.BaseURL,
		AuthType: s.cfg.AuthType,
		HasToken: s.cfg.Token != "",
		Username: s.cfg.Username,
		OrgID:    s.cfg.OrgID,
	}
}

// CurrentSettings returns the full config including the token —
// only used by SavePersisted to round-trip the existing token
// when the operator submits a partial update that doesn't
// include a new one. Never call this from a handler that
// echoes its return value over the wire.
func (s *Service) CurrentSettings() Settings {
	if s == nil {
		return Settings{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Configured reports whether Tempo is wired up and ready to
// answer lookups. Used by the trace-fallback path to skip the
// HTTP attempt when no backend is configured.
func (s *Service) Configured() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled && s.cfg.BaseURL != ""
}

// LookupTrace asks Tempo for a trace by ID. Returns an empty
// slice + nil error when Tempo doesn't have it (404) so callers
// can treat "not found" as a clean fall-through rather than an
// error condition. Network / parse failures surface as real
// errors.
//
// Tempo's GET /api/traces/{id} returns OTLP-encoded data; we
// request JSON via Accept so we don't have to pull in a protobuf
// dependency for a single endpoint.
func (s *Service) LookupTrace(ctx context.Context, traceID string) ([]chstore.SpanRow, error) {
	if !s.Configured() {
		return nil, errors.New("tempo not configured")
	}
	cfg := s.CurrentSettings()

	url := strings.TrimRight(cfg.BaseURL, "/") + "/api/traces/" + traceID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	switch cfg.AuthType {
	case "bearer":
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
	case "basic":
		if cfg.Username != "" || cfg.Token != "" {
			req.SetBasicAuth(cfg.Username, cfg.Token)
		}
	}
	if cfg.OrgID != "" {
		req.Header.Set("X-Scope-OrgID", cfg.OrgID)
	}

	resp, err := s.cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tempo call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return []chstore.SpanRow{}, nil
	}
	// Cap body at 50MB — pathological traces (1000s of spans) can
	// be large but never legitimately need more than this.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tempo %d: %s", resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	return parseOTLPTrace(body)
}

// ── OTLP-JSON parsing ───────────────────────────────────────────

// otlpTrace handles both Tempo response shapes — older versions
// nested under `batches`, newer ones under `resourceSpans`. We
// try both rather than gating on Tempo version.
type otlpTrace struct {
	Batches       []otlpBatch `json:"batches"`
	ResourceSpans []otlpBatch `json:"resourceSpans"`
}

type otlpBatch struct {
	Resource struct {
		Attributes []otlpAttr `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Scope struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"scope"`
		Spans []otlpSpan `json:"spans"`
	} `json:"scopeSpans"`
	// Older Tempo nests under "instrumentationLibrarySpans"
	// instead of scopeSpans. Same shape otherwise.
	InstrumentationLibrarySpans []struct {
		InstrumentationLibrary struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"instrumentationLibrary"`
		Spans []otlpSpan `json:"spans"`
	} `json:"instrumentationLibrarySpans"`
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []otlpAttr `json:"attributes"`
	Status            struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string  `json:"stringValue,omitempty"`
		// OTLP-JSON encodes int64 as a string. Parsing as
		// *string handles overflow correctly.
		IntValue    *string  `json:"intValue,omitempty"`
		DoubleValue *float64 `json:"doubleValue,omitempty"`
		BoolValue   *bool    `json:"boolValue,omitempty"`
	} `json:"value"`
}

func (a otlpAttr) String() string {
	v := a.Value
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return *v.IntValue
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'f', -1, 64)
	case v.BoolValue != nil:
		if *v.BoolValue {
			return "true"
		}
		return "false"
	}
	return ""
}

// kindMap translates OTLP SpanKind ints to Coremetry's lowercase
// string convention. 0 = unspecified → "internal" (the safe
// default; matches Coremetry's own ingest path).
var kindMap = []string{"internal", "internal", "server", "client", "producer", "consumer"}

func parseOTLPTrace(data []byte) ([]chstore.SpanRow, error) {
	var t otlpTrace
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("tempo decode: %w", err)
	}
	batches := t.Batches
	if len(batches) == 0 {
		batches = t.ResourceSpans
	}
	var out []chstore.SpanRow
	for _, b := range batches {
		// Resource attributes — pull service.name + host.name out
		// explicitly so SpanRow's structured fields stay populated.
		// Everything else lands in ResourceAttributes as a map.
		resAttrs := map[string]string{}
		var svc, host string
		for _, a := range b.Resource.Attributes {
			v := a.String()
			resAttrs[a.Key] = v
			switch a.Key {
			case "service.name":
				svc = v
			case "host.name":
				host = v
			}
		}
		// Collect spans from both old + new envelope shapes.
		type spanGroup struct {
			scopeName string
			spans     []otlpSpan
		}
		var groups []spanGroup
		for _, ss := range b.ScopeSpans {
			groups = append(groups, spanGroup{scopeName: ss.Scope.Name, spans: ss.Spans})
		}
		for _, il := range b.InstrumentationLibrarySpans {
			groups = append(groups, spanGroup{scopeName: il.InstrumentationLibrary.Name, spans: il.Spans})
		}
		for _, g := range groups {
			for _, sp := range g.spans {
				attrs := map[string]string{}
				for _, a := range sp.Attributes {
					attrs[a.Key] = a.String()
				}
				start, _ := strconv.ParseInt(sp.StartTimeUnixNano, 10, 64)
				end, _ := strconv.ParseInt(sp.EndTimeUnixNano, 10, 64)
				kind := "internal"
				if sp.Kind >= 0 && sp.Kind < len(kindMap) {
					kind = kindMap[sp.Kind]
				}
				statusCode := "unset"
				switch sp.Status.Code {
				case 1:
					statusCode = "ok"
				case 2:
					statusCode = "error"
				}
				row := chstore.SpanRow{
					TraceID:            decodeID(sp.TraceID),
					SpanID:             decodeID(sp.SpanID),
					ParentSpanID:       decodeID(sp.ParentSpanID),
					Name:               sp.Name,
					Kind:               kind,
					ServiceName:        svc,
					HostName:           host,
					StartTime:          start,
					EndTime:            end,
					DurationMs:         float64(end-start) / 1e6,
					StatusCode:         statusCode,
					StatusMessage:      sp.Status.Message,
					Attributes:         attrs,
					ResourceAttributes: resAttrs,
					ScopeName:          g.scopeName,
					DBSystem:           attrs["db.system"],
					DBStatement:        attrs["db.statement"],
					HTTPMethod:         attrs["http.method"],
					HTTPRoute:          attrs["http.route"],
					PeerService:        attrs["peer.service"],
				}
				// http.status_code is conventionally an int but
				// some libraries emit it as a string attribute;
				// parse as int when it fits the uint16 range we
				// reserve in SpanRow.
				if hsRaw, ok := attrs["http.status_code"]; ok {
					if v, err := strconv.Atoi(hsRaw); err == nil && v >= 0 && v <= 65535 {
						row.HTTPStatus = uint16(v)
					}
				}
				out = append(out, row)
			}
		}
	}
	return out, nil
}

// decodeID handles both hex string IDs (Tempo's normal form) and
// base64-encoded IDs (older OTLP-JSON style). Coremetry normalises
// every trace/span ID to lowercase hex internally — same input
// shape regardless of source.
func decodeID(s string) string {
	if s == "" {
		return ""
	}
	// Hex fast path — most Tempo deployments return hex already.
	hexOK := true
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			hexOK = false
		}
		if !hexOK {
			break
		}
	}
	if hexOK {
		return strings.ToLower(s)
	}
	// Base64 fallback for legacy OTLP-JSON. Try standard first,
	// then URL-safe encoding before giving up.
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return fmt.Sprintf("%x", b)
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return fmt.Sprintf("%x", b)
	}
	return s
}
