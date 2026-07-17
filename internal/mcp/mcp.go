// Package mcp implements the Model Context Protocol server side
// for Coremetry. MCP is JSON-RPC 2.0 over an HTTP+SSE transport,
// designed so external LLM clients (Claude Desktop, Anthropic
// API tool-calling integrations, internal copilots) can discover
// and invoke a server's tools, list its resources, and read its
// prompt templates.
//
// What Coremetry exposes (built up across v0.6.4–v0.6.7):
//
//	v0.6.4 — core protocol scaffolding: JSON-RPC framing,
//	         HTTP+SSE transport, session lifecycle, initialize +
//	         ping methods. tools/resources/prompts capabilities
//	         advertised but their registries are empty.
//	v0.6.5 — tools/list + tools/call wired to a Coremetry tools
//	         registry (list_services, search_logs, get_trace,
//	         query_metric, list_problems, …). Salt-okunur —
//	         mutation tool'u bugüne dek hiç eklenmedi (v0.9.14
//	         audit'i: yazma gelirse audit_log source alanı şart).
//	v0.6.6 — resources/list + resources/read exposing
//	         traces/logs/metrics as MCP resources.
//	v0.6.7 — prompts/list + prompts/get exposing Coremetry's
//	         curated system prompts (Explain trace, Suggest
//	         runbook, Compare deploys, …) as MCP prompts so an
//	         external LLM can take the same systematic approach
//	         an operator sees in /problems.
//
// Auth: the HTTP handlers in this package are wrapped by the same
// auth middleware as /api/*. Browser sessions work via the JWT
// cookie; programmatic clients (Claude Desktop, internal LLMs)
// obtain a token via POST /api/auth/login then carry it in the
// Authorization: Bearer header. This avoids inventing a separate
// API key system — viewer/editor/admin roles apply to MCP calls
// exactly as they do to REST.
//
// Transport choice: this implementation uses the original
// HTTP+SSE transport (MCP spec 2024-11-05). Newer Streamable-HTTP
// (2025-03-26) is similar but uses a single endpoint + session
// header; can layer it on later without breaking existing clients.
//
// References:
//   - https://modelcontextprotocol.io
//   - https://spec.modelcontextprotocol.io/specification
package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// ProtocolVersion is what we advertise during initialize. Clients
// echoing a different version are negotiated against this — if
// the client's version isn't supported we still reply with ours
// per the MCP spec: "the server SHOULD respond with its own
// version" and let the client decide whether to proceed.
const ProtocolVersion = "2024-11-05"

// ProtocolVersionStreamable — Streamable-HTTP transport'unun (v0.9.14)
// initialize yanıtında bildirdiği sürüm. SSE yolu 2024-11-05'te kalır;
// iki transport aynı dispatch gövdesini paylaşır, yalnız el-sıkışma
// sürümü ayrışır.
const ProtocolVersionStreamable = "2025-03-26"

// JSON-RPC 2.0 envelope types. We don't use a third-party
// JSON-RPC library because (a) the surface is small, (b) MCP's
// notification vs request distinction is just "id present or
// not", and (c) carrying a dep for ~80 lines of code is the
// kind of thing /scale-audit would later flag.

// Request is the incoming JSON-RPC message. ID is left as
// RawMessage so we can echo it back verbatim — clients send
// numbers, strings, or null per spec and we shouldn't coerce.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification — JSON-RPC spec: a request without an id is a
// notification, no response expected. notifications/initialized
// is the canonical example.
func (r *Request) IsNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// Response is the outgoing reply. Either Result or Error is set,
// never both. omitempty on both fields keeps the wire form clean.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error follows JSON-RPC 2.0 — standard codes are -32700..-32603,
// implementation-defined server errors are -32099..-32000. MCP
// defines its own codes in the same -32xxx space which we hand
// out below.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error codes — covers JSON-RPC base + MCP-specific.
const (
	ErrParse          = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
	// ErrRateLimited (v0.9.14) — tools/call kapısının reddi; server-
	// error bandının tepesi. HTTP 429 yerine JSON-RPC hatası: LLM
	// tool-use döngüleri bunu sonuç olarak görür, bekleyip yeniden
	// dener.
	ErrRateLimited = -32000
	// MCP-defined application errors live in the
	// -32099..-32000 server-error band. ErrToolNotFound is
	// the only one we need at v0.6.4; tools/resources/prompts
	// add their own as those registries land.
)

// initializeParams + initializeResult are the typed shapes for
// the initialize method. We don't model every spec field
// (clientInfo is informational; we log it but don't act on it),
// only the bits we need to negotiate.
type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      ClientInfo      `json:"clientInfo,omitempty"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities is what we tell the client we support. Each
// optional bag is "exists or doesn't" semantics — an empty struct
// signals "feature supported, no sub-features yet". As tools /
// resources / prompts come online in later releases the
// corresponding pointer gets non-nil.
//
// v0.6.4 advertises tools/resources/prompts as supported (so
// clients run their listing dance against us) even though the
// registries are empty — keeps client codepaths warm and means
// the v0.6.5+ rollouts don't need a protocol bump.
type ServerCapabilities struct {
	Tools     *CapToolsBag     `json:"tools,omitempty"`
	Resources *CapResourcesBag `json:"resources,omitempty"`
	Prompts   *CapPromptsBag   `json:"prompts,omitempty"`
	Logging   *struct{}        `json:"logging,omitempty"`
}

type CapToolsBag struct {
	// ListChanged: server emits notifications/tools/list_changed
	// when its tool catalogue mutates. v0.6.4 returns false
	// (tools registry is empty + immutable); v0.6.5 flips this
	// if we ever support hot-loading tools.
	ListChanged bool `json:"listChanged,omitempty"`
}
type CapResourcesBag struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}
type CapPromptsBag struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// Tool is an MCP tool definition. Tools are the LLM's
// callable surface — discoverable via tools/list, invoked via
// tools/call. The input schema is a JSON Schema object the
// client uses to validate args before invocation (and to render
// a form UI in Claude Desktop / similar inspectors).
//
// The handler receives the raw params JSON as a RawMessage —
// concrete tool implementations decode into their own typed
// args struct. Return value is anything json.Marshal-able; the
// server wraps it in the tools/call response envelope.
//
// Schema example (mirrors JSON Schema draft-2020-12 enough for
// every MCP client):
//
//	InputSchema: map[string]any{
//	    "type": "object",
//	    "properties": map[string]any{
//	        "service": map[string]any{"type": "string"},
//	        "range_ns": map[string]any{"type": "integer", "minimum": 0},
//	    },
//	    "required": []string{"service"},
//	}
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     ToolHandler
}

// ToolHandler runs one tools/call invocation. Returns a value
// the server marshals into the response, OR an error which is
// converted into a JSON-RPC error with code ErrInternal. The
// raw args are passed through so the handler picks its own
// decode (tagged struct, manual map dispatch, etc.).
type ToolHandler func(ctx context.Context, args json.RawMessage) (any, error)

// Resource is a named read-only data reference. Where tools are
// "invoke an action with args", resources are "fetch the
// current state of <URI>". Browsers in Claude Desktop list
// resources as pinned references the user can click to attach;
// LLMs read them via resources/read.
//
// URI scheme is opaque to the protocol — we use coremetry://
// for everything Coremetry exposes (services, problems, etc.).
// MimeType hints how the client renders the read response;
// "application/json" is standard for our payloads.
type Resource struct {
	URI         string
	Name        string
	Description string
	MimeType    string
	// Reader returns the content for this URI. ctx is the live
	// request context; the URI is passed through unchanged so
	// the same Reader can serve a template family.
	Reader ResourceReader
}

// ResourceTemplate is a URI pattern with {placeholder} segments.
// LLMs match against the pattern to discover that
// "coremetry://service/{name}" can be parameterised. The Reader
// receives the concrete URI; its handler parses out the
// placeholder values.
type ResourceTemplate struct {
	URITemplate string
	Name        string
	Description string
	MimeType    string
	Reader      ResourceReader
}

// ResourceReader is the function signature both concrete and
// templated resources share. uri is the full URI being read.
// Returns either a text payload or an error; binary blobs can
// layer on later if needed (image/png, audio, etc.) — the spec
// supports them via a `blob` field on the content envelope.
type ResourceReader func(ctx context.Context, uri string) (text string, err error)

// Prompt is a server-curated LLM prompt template. prompts/list
// surfaces them so the client can show "/coremetry-explain-
// trace [trace_id]" in its slash-command menu; prompts/get with
// args runs the Renderer to produce the final messages the
// client feeds to its model.
//
// Coremetry's prompts close over chstore/logstore so calling
// "explain_trace" actually fetches the trace data and embeds it
// into the user message — the LLM doesn't have to make a
// follow-up tool call before reasoning. This is the same
// pattern as the in-app "✨ Explain" button, just exposed via
// MCP for external clients.
type Prompt struct {
	Name        string
	Description string
	Arguments   []PromptArgument
	Renderer    PromptRenderer
}

// PromptArgument describes one input slot for prompts/get. Type
// is always implicitly string in the MCP spec; the description
// is what gets shown to the user when their client renders the
// argument form.
type PromptArgument struct {
	Name        string
	Description string
	Required    bool
}

// PromptMessage is one role+content pair the Renderer emits.
// MCP prompts can return system + user + assistant messages;
// the client typically replays them as the conversation seed.
type PromptMessage struct {
	Role    string        `json:"role"`
	Content PromptContent `json:"content"`
}

// PromptContent is the body of a message. Type is currently
// always "text" for Coremetry's prompts — image / resource
// content can layer on later.
type PromptContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// PromptRenderer takes the prompts/get arguments and returns the
// resolved message set. Errors bubble up as JSON-RPC errors.
// Receives the raw arg map so the renderer picks its own decode.
type PromptRenderer func(ctx context.Context, args map[string]string) ([]PromptMessage, error)

// Server is the MCP server. One instance per Coremetry process;
// holds the live session map + the registries that v0.6.5+ will
// populate.
//
// The handler funcs HandleSSE + HandleMessage are wired onto the
// existing api.Server mux as /api/mcp/sse and /api/mcp/messages
// behind the standard auth middleware. Session state is in-memory
// only; a session is local to the pod the client first connected
// to. In distributed deploy + multi-replica api, a sticky-session
// LB rule (or session affinity at the ingress) is required —
// documented in v0.6.4's CHANGELOG.
type Server struct {
	info ServerInfo

	mu       sync.RWMutex
	sessions map[string]*session

	// tools is the global registry consulted by tools/list and
	// tools/call. Tools are registered once at boot (from main()
	// after the api.Server + chstore + logstore are wired) and
	// are not mutated thereafter — so we serve tools/list under
	// a read lock and don't bother emitting list_changed.
	tools map[string]Tool

	// resources + resourceTemplates back the resources/list,
	// resources/read, and resources/templates/list methods.
	// Same lifecycle as tools — populated once at boot,
	// immutable thereafter. Static resources have fixed URIs;
	// templated resources expose URI patterns the LLM can
	// instantiate with concrete values (e.g. a trace_id).
	resources         map[string]Resource // key = URI
	resourceTemplates []ResourceTemplate

	// prompts is the curated prompt registry served by
	// prompts/list and prompts/get. Same lifecycle as tools /
	// resources — boot-time populated, immutable.
	prompts map[string]Prompt

	// gate (v0.9.14) — opsiyonel tools/call kapısı. Paket
	// auth-agnostik kalır: kimlik/limit bilgisi api katmanında,
	// buraya yalnız "geçir/RED" fonksiyonu iner. nil = kapısız.
	gate ToolCallGate
}

// ToolCallGate — tools/call öncesi çağrılır; hata dönerse çağrı
// JSON-RPC -32000 ile reddedilir (HTTP 429 DEĞİL: istemci
// kütüphaneleri JSON-RPC hatasını LLM'e tool sonucu olarak gösterir,
// model bekleyip devam edebilir). initialize/tools/list/prompts
// kapı dışıdır (ucuz keşif).
type ToolCallGate func(ctx context.Context, tool string) error

// SetToolCallGate wires the optional rate-limit gate. Boot'ta bir
// kez çağrılır (api.SetMCP), sonrasında değişmez.
func (s *Server) SetToolCallGate(g ToolCallGate) {
	s.gate = g
}

// session is the per-client connection state. The outbound
// channel feeds the SSE stream goroutine; HandleMessage writes
// JSON-RPC responses here. Closed when the SSE stream goroutine
// exits (ctx cancel / client disconnect).
type session struct {
	id     string
	out    chan json.RawMessage
	closed chan struct{}

	mu          sync.Mutex
	initialized bool
	clientInfo  ClientInfo
}

// New constructs an MCP server with the given serverInfo. Name +
// version surface in the initialize response — clients (and
// Claude Desktop's debug UI) display them.
func New(name, version string) *Server {
	return &Server{
		info:      ServerInfo{Name: name, Version: version},
		sessions:  map[string]*session{},
		tools:     map[string]Tool{},
		resources: map[string]Resource{},
		prompts:   map[string]Prompt{},
	}
}

// RegisterPrompt adds a prompt to the registry. Name uniqueness
// enforced — duplicate registration logs and overwrites.
func (s *Server) RegisterPrompt(p Prompt) {
	if p.Name == "" {
		log.Printf("[mcp] RegisterPrompt: empty name; skipped")
		return
	}
	if p.Renderer == nil {
		log.Printf("[mcp] RegisterPrompt %q: nil Renderer; skipped", p.Name)
		return
	}
	s.mu.Lock()
	if _, exists := s.prompts[p.Name]; exists {
		log.Printf("[mcp] RegisterPrompt: %q already registered, overwriting", p.Name)
	}
	s.prompts[p.Name] = p
	s.mu.Unlock()
}

// PromptCount reports how many prompts are registered. Useful in
// boot logs so the operator can confirm the registry wired up.
func (s *Server) PromptCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.prompts)
}

// RegisterResource adds a concrete (fixed-URI) resource. URI
// uniqueness enforced — duplicate registration logs a warning
// and overwrites.
func (s *Server) RegisterResource(r Resource) {
	if r.URI == "" {
		log.Printf("[mcp] RegisterResource: empty URI; skipped")
		return
	}
	if r.Reader == nil {
		log.Printf("[mcp] RegisterResource %q: nil Reader; skipped", r.URI)
		return
	}
	s.mu.Lock()
	if _, exists := s.resources[r.URI]; exists {
		log.Printf("[mcp] RegisterResource: %q already registered, overwriting", r.URI)
	}
	s.resources[r.URI] = r
	s.mu.Unlock()
}

// RegisterResourceTemplate adds a URI-pattern resource. The
// pattern uses {placeholder} segments per RFC 6570 level 1 — we
// match prefix + suffix around each placeholder without pulling
// in a full URI Template engine. Good enough for the patterns
// Coremetry exposes (single placeholder near the end).
func (s *Server) RegisterResourceTemplate(rt ResourceTemplate) {
	if rt.URITemplate == "" {
		log.Printf("[mcp] RegisterResourceTemplate: empty URITemplate; skipped")
		return
	}
	if rt.Reader == nil {
		log.Printf("[mcp] RegisterResourceTemplate %q: nil Reader; skipped", rt.URITemplate)
		return
	}
	s.mu.Lock()
	s.resourceTemplates = append(s.resourceTemplates, rt)
	s.mu.Unlock()
}

// ResourceCount reports the static + template totals so boot
// logs can confirm registration. Returns (static, templates).
func (s *Server) ResourceCount() (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.resources), len(s.resourceTemplates)
}

// RegisterTool adds a tool to the registry. Caller MUST do this
// before serving requests — there's no list_changed notification
// yet (tools/list returns a static snapshot). Last writer wins
// on a duplicate name; we log because that's almost certainly a
// bug.
func (s *Server) RegisterTool(t Tool) {
	if t.Name == "" {
		log.Printf("[mcp] RegisterTool: empty name; skipped")
		return
	}
	s.mu.Lock()
	if _, exists := s.tools[t.Name]; exists {
		log.Printf("[mcp] RegisterTool: %q already registered, overwriting", t.Name)
	}
	s.tools[t.Name] = t
	s.mu.Unlock()
}

// ToolCount reports how many tools are registered. Useful in
// boot logs so the operator can confirm the registry wired up.
func (s *Server) ToolCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tools)
}

// newSession allocates a session and registers it. Caller is
// responsible for cleanup via removeSession when the SSE stream
// closes.
func (s *Server) newSession() *session {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Same fallback rationale as sse.randomPodID — extremely
		// rare; timestamp-derived id keeps the session functional
		// even if non-cryptographic.
		log.Printf("[mcp] session id rand: %v", err)
	}
	sess := &session{
		id:     hex.EncodeToString(b[:]),
		out:    make(chan json.RawMessage, 32),
		closed: make(chan struct{}),
	}
	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	return sess
}

func (s *Server) lookupSession(id string) *session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}

func (s *Server) removeSession(id string) {
	s.mu.Lock()
	if sess, ok := s.sessions[id]; ok {
		delete(s.sessions, id)
		// out is closed by the SSE goroutine on its way out;
		// closing it here would race with concurrent writes
		// from in-flight HandleMessage requests. closed is the
		// signal those requests check before sending.
		close(sess.closed)
	}
	s.mu.Unlock()
}

// HandleSSE implements the GET side of the HTTP+SSE transport.
// The first event emitted is an `endpoint` event whose data is
// the absolute path the client should POST messages to —
// including the session id query param. All subsequent events
// are `message` events carrying JSON-RPC responses for requests
// the client POSTed.
func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sess := s.newSession()
	defer s.removeSession(sess.id)

	// Spec requirement: first SSE event names the endpoint the
	// client should POST requests to. Path is absolute and
	// session-scoped.
	endpointURL := fmt.Sprintf("/api/mcp/messages?sessionId=%s", sess.id)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURL)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			// SSE comment frame — keeps proxies (NGINX,
			// CloudFlare) from idling out the connection.
			// Not a protocol-level ping; MCP's ping method is
			// separately exposed and rides as a JSON-RPC message.
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case msg := <-sess.out:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// HandleMessage is the POST side of the transport. Client sends
// one JSON-RPC request; we respond synchronously via the SSE
// stream (not in the HTTP response body — per MCP spec, the POST
// returns 202 Accepted and the response rides the SSE channel).
//
// This split lets the server send notifications + responses to
// unsolicited events back on the same SSE stream, which is what
// makes "live tool call" streaming work in v0.6.5+.
func (s *Server) HandleMessage(w http.ResponseWriter, r *http.Request) {
	sessID := r.URL.Query().Get("sessionId")
	if sessID == "" {
		http.Error(w, "sessionId query param required", http.StatusBadRequest)
		return
	}
	sess := s.lookupSession(sessID)
	if sess == nil {
		http.Error(w, "unknown session — connect to /api/mcp/sse first", http.StatusNotFound)
		return
	}

	var req Request
	// v0.9.20 (self-review fix) — 4MB gövde limiti HandleStreamable'a
	// eklenmiş ama bu eski POST yolunda unutulmuştu (majör): sınırsız
	// gövde decode'u tek istekle belleği şişirebilirdi.
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "parse error: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.JSONRPC != "2.0" {
		// We still accept it but log the deviation — MCP spec
		// strictly requires "2.0".
		log.Printf("[mcp] session=%s non-2.0 jsonrpc %q", sessID, req.JSONRPC)
	}

	// Notifications get no response on the SSE channel. ACK with
	// 202 and dispatch off-thread so a slow notification handler
	// doesn't block the POST.
	if req.IsNotification() {
		go s.dispatchNotification(r.Context(), sess, &req)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := s.dispatch(r.Context(), sess, &req)
	raw, err := json.Marshal(resp)
	if err != nil {
		log.Printf("[mcp] marshal response: %v", err)
		http.Error(w, "internal encode error", http.StatusInternalServerError)
		return
	}
	select {
	case sess.out <- raw:
	case <-sess.closed:
		// SSE stream gone — drop the response. Client must
		// have hung up; nothing we can do.
	case <-time.After(sendTimeout):
		// SSE consumer wedged (browser tab hidden, no flush).
		// Drop with a log so the operator can see the cluster's
		// MCP backpressure if it becomes a pattern.
		log.Printf("[mcp] session=%s response queue full — dropped %s", sessID, req.Method)
	}
	w.WriteHeader(http.StatusAccepted)
}

// sendTimeout — HandleMessage'ın SSE tüketicisi tıkalıyken yanıtı
// düşürmeden önce beklediği süre. Var (const değil) ki mcp_test
// wedged-consumer senaryosunu saniyeler beklemeden pinleyebilsin.
var sendTimeout = 2 * time.Second

// HandleStreamable — Streamable-HTTP transport'u (v0.9.14, MCP spec
// 2025-03-26), STATELESS kipte: tek POST = tek JSON-RPC isteği,
// yanıt DOĞRUDAN gövdede (SSE kanalı yok). Session hiç üretilmez
// (Mcp-Session-Id başlığı dönülmez) — istemci sessionless çalışır.
// Bu, çok-pod'lu kurulumdaki pod-lokal session kırılganlığını
// (audit EK BULGU) kökten çözer: her POST bağımsızdır, LB'de hangi
// pod'a düşerse düşsün. Claude Code'un birincil `--transport http`
// yolu budur; SSE yolu eski istemciler için aynen kalır.
func (s *Server) HandleStreamable(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	// v0.9.20 (self-review fix) — 2025-03-26 spec'i batch KABULÜNÜ
	// zorunlu kılar; tekil-decode batch'i -32700'le reddediyordu.
	// Parse hatasında id spec gereği null olarak GÖNDERİLİR
	// (omitempty onu düşürüyordu → RawMessage("null")).
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var reqs []Request
		if err := json.Unmarshal(trimmed, &reqs); err != nil || len(reqs) == 0 {
			writeStreamableJSON(w, http.StatusBadRequest,
				errorResp(json.RawMessage("null"), ErrParse, "parse error: invalid batch"))
			return
		}
		resps := make([]*Response, 0, len(reqs))
		for i := range reqs {
			if reqs[i].IsNotification() {
				s.dispatchNotification(r.Context(), nil, &reqs[i])
				continue // JSON-RPC: bildirime yanıt girdisi yazılmaz
			}
			resps = append(resps, s.handleStreamableOne(r.Context(), &reqs[i]))
		}
		if len(resps) == 0 {
			w.WriteHeader(http.StatusAccepted) // tamamı bildirimdi
			return
		}
		writeStreamableJSON(w, http.StatusOK, resps)
		return
	}
	var req Request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeStreamableJSON(w, http.StatusBadRequest,
			errorResp(json.RawMessage("null"), ErrParse, "parse error: "+err.Error()))
		return
	}
	if req.IsNotification() {
		// Spec: bildirime 202, gövde yok.
		s.dispatchNotification(r.Context(), nil, &req)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeStreamableJSON(w, http.StatusOK, s.handleStreamableOne(r.Context(), &req))
}

// handleStreamableOne — tek isteğin sessionless dispatch'i
// (initialize bu transport'un sürüm sabitiyle özel-durumlanır).
func (s *Server) handleStreamableOne(ctx context.Context, req *Request) *Response {
	if req.Method == "initialize" {
		return s.handleInitializeStreamable(req)
	}
	return s.dispatch(ctx, nil, req)
}

func writeStreamableJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[mcp] streamable encode: %v", err)
	}
}

// handleInitializeStreamable — handleInitialize'ın sessionless hali:
// aynı capabilities/serverInfo, sürüm 2025-03-26, hiçbir durum yazımı.
func (s *Server) handleInitializeStreamable(req *Request) *Response {
	var p initializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errorResp(req.ID, ErrInvalidParams, "initialize params: "+err.Error())
		}
	}
	if p.ClientInfo.Name != "" {
		log.Printf("[mcp] streamable initialize from %s/%s (their protocol=%s)",
			p.ClientInfo.Name, p.ClientInfo.Version, p.ProtocolVersion)
	}
	return successResp(req.ID, initializeResult{
		ProtocolVersion: ProtocolVersionStreamable,
		Capabilities: ServerCapabilities{
			Tools:     &CapToolsBag{},
			Resources: &CapResourcesBag{},
			Prompts:   &CapPromptsBag{},
		},
		ServerInfo: s.info,
	})
}

// dispatch routes a request to the appropriate method handler
// and returns the JSON-RPC response shape (success or error).
// Method registry is centralised here so the surface is easy to
// audit at a glance.
func (s *Server) dispatch(ctx context.Context, sess *session, req *Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(sess, req)
	case "ping":
		return s.handlePing(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "resources/list":
		return s.handleResourcesList(req)
	case "resources/read":
		return s.handleResourcesRead(ctx, req)
	case "resources/templates/list":
		return s.handleResourceTemplatesList(req)
	case "prompts/list":
		return s.handlePromptsList(req)
	case "prompts/get":
		return s.handlePromptsGet(ctx, req)
	default:
		return errorResp(req.ID, ErrMethodNotFound,
			fmt.Sprintf("method not found: %s", req.Method))
	}
}

// dispatchNotification handles the no-id variant. Only
// notifications/initialized matters at v0.6.4 — it transitions
// the session into the "ready for arbitrary methods" state.
func (s *Server) dispatchNotification(_ context.Context, sess *session, req *Request) {
	// sess == nil = Streamable-HTTP (stateless, v0.9.14): oturum
	// durumu yok, bildirimler sessizce kabul edilir (spec: 202).
	if sess == nil {
		return
	}
	switch req.Method {
	case "notifications/initialized":
		sess.mu.Lock()
		sess.initialized = true
		sess.mu.Unlock()
	default:
		log.Printf("[mcp] session=%s unknown notification %q", sess.id, req.Method)
	}
}

// handleInitialize negotiates the protocol version + advertises
// capabilities. Per spec we accept the client's clientInfo as
// purely informational and respond with our own.
func (s *Server) handleInitialize(sess *session, req *Request) *Response {
	var p initializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errorResp(req.ID, ErrInvalidParams, "initialize params: "+err.Error())
		}
	}
	sess.mu.Lock()
	sess.clientInfo = p.ClientInfo
	sess.mu.Unlock()
	if p.ClientInfo.Name != "" {
		log.Printf("[mcp] session=%s initialize from %s/%s (their protocol=%s)",
			sess.id, p.ClientInfo.Name, p.ClientInfo.Version, p.ProtocolVersion)
	}
	result := initializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			// v0.6.4 — advertise the surface even though registries
			// are still empty. Saves a protocol bump in v0.6.5+.
			Tools:     &CapToolsBag{},
			Resources: &CapResourcesBag{},
			Prompts:   &CapPromptsBag{},
		},
		ServerInfo: s.info,
	}
	return successResp(req.ID, result)
}

// handlePing — trivial; just echoes back an empty result. MCP's
// ping is application-level (separate from SSE heartbeat) and
// clients use it to verify the session is alive end-to-end.
func (s *Server) handlePing(req *Request) *Response {
	return successResp(req.ID, struct{}{})
}

// toolListEntry is the wire shape for one entry in the
// tools/list response — name + description + the JSON Schema
// for inputs. Clients render this in their tool-picker UI.
type toolListEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// handleToolsList returns the registry snapshot. Sorted only
// implicitly by Go's map iteration order — MCP clients sort
// their own UI. If a deterministic order is needed later (some
// inspectors do alpha-sort, some preserve server order), it's
// a one-line addition.
func (s *Server) handleToolsList(req *Request) *Response {
	s.mu.RLock()
	tools := make([]toolListEntry, 0, len(s.tools))
	for _, t := range s.tools {
		schema := t.InputSchema
		if schema == nil {
			// Spec requires inputSchema; tools that take no args
			// still emit `{"type":"object"}` so the client's
			// validator doesn't choke. Cheap default.
			schema = map[string]any{"type": "object"}
		}
		tools = append(tools, toolListEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	s.mu.RUnlock()
	return successResp(req.ID, map[string]any{"tools": tools})
}

// toolsCallParams is the input shape for tools/call. Arguments
// is left as RawMessage so the handler closure picks its own
// decode strategy.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// toolCallContent is one item in the response's content array.
// MCP supports text + image + resource references; we ship text
// only — every Coremetry tool returns JSON which the LLM is
// perfectly capable of parsing from a text payload. Image /
// resource shapes can layer on later without a wire break.
type toolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// toolCallResult is the tools/call response envelope. IsError
// signals the LLM that the call failed at the application layer
// (vs a transport error, which would surface as a JSON-RPC
// error). Per MCP convention, isError=true tools still produce
// content (the error message) so the LLM has something to
// reason about.
type toolCallResult struct {
	Content []toolCallContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// handleToolsCall dispatches to the registered handler. Decode
// failures + handler errors both come back as isError=true
// content rather than JSON-RPC errors, so the LLM sees a
// machine-readable message instead of an opaque -32603. Reserve
// JSON-RPC errors for "tool not found" / "params malformed at
// the protocol level".
func (s *Server) handleToolsCall(ctx context.Context, req *Request) *Response {
	var p toolsCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrInvalidParams, "tools/call params: "+err.Error())
	}
	if p.Name == "" {
		return errorResp(req.ID, ErrInvalidParams, "tools/call: name is required")
	}
	s.mu.RLock()
	tool, ok := s.tools[p.Name]
	s.mu.RUnlock()
	if !ok {
		return errorResp(req.ID, ErrMethodNotFound, fmt.Sprintf("tool not found: %s", p.Name))
	}
	// v0.9.14 — rate-limit kapısı (varsa) tool çözümünden SONRA,
	// handler'dan ÖNCE: bilinmeyen tool -32601 kalır, gate yalnız
	// gerçek çağrıları sayar.
	if s.gate != nil {
		if err := s.gate(ctx, p.Name); err != nil {
			return errorResp(req.ID, ErrRateLimited, err.Error())
		}
	}

	out, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		return successResp(req.ID, toolCallResult{
			Content: []toolCallContent{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
	}
	// Marshal the handler's output as the text content. JSON form
	// is what every LLM tool-use loop expects to feed back into
	// the next turn; we deliberately don't try to "render" it.
	body, err := json.Marshal(out)
	if err != nil {
		return successResp(req.ID, toolCallResult{
			Content: []toolCallContent{{Type: "text", Text: "marshal error: " + err.Error()}},
			IsError: true,
		})
	}
	return successResp(req.ID, toolCallResult{
		Content: []toolCallContent{{Type: "text", Text: string(body)}},
	})
}

// successResp wraps the result into the JSON-RPC envelope.
func successResp(id json.RawMessage, result any) *Response {
	raw, err := json.Marshal(result)
	if err != nil {
		return errorResp(id, ErrInternal, "marshal result: "+err.Error())
	}
	return &Response{JSONRPC: "2.0", ID: id, Result: raw}
}

// errorResp wraps a code+message into a JSON-RPC error envelope.
func errorResp(id json.RawMessage, code int, message string) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}
}

// ── resources/list, resources/read, resources/templates/list ───

// resourceListEntry mirrors the wire shape MCP clients expect
// from resources/list — URI + descriptive metadata. No content;
// the client calls resources/read for that.
type resourceListEntry struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type resourceTemplateListEntry struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

func (s *Server) handleResourcesList(req *Request) *Response {
	s.mu.RLock()
	out := make([]resourceListEntry, 0, len(s.resources))
	for _, r := range s.resources {
		out = append(out, resourceListEntry{
			URI:         r.URI,
			Name:        r.Name,
			Description: r.Description,
			MimeType:    r.MimeType,
		})
	}
	s.mu.RUnlock()
	return successResp(req.ID, map[string]any{"resources": out})
}

func (s *Server) handleResourceTemplatesList(req *Request) *Response {
	s.mu.RLock()
	out := make([]resourceTemplateListEntry, 0, len(s.resourceTemplates))
	for _, rt := range s.resourceTemplates {
		out = append(out, resourceTemplateListEntry{
			URITemplate: rt.URITemplate,
			Name:        rt.Name,
			Description: rt.Description,
			MimeType:    rt.MimeType,
		})
	}
	s.mu.RUnlock()
	return successResp(req.ID, map[string]any{"resourceTemplates": out})
}

type resourcesReadParams struct {
	URI string `json:"uri"`
}

type resourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

// handleResourcesRead resolves a URI against the static registry
// first, then falls through to templates by pattern match. URI
// that matches neither yields a JSON-RPC ErrMethodNotFound — MCP
// has no dedicated "resource not found" code so reusing
// method-not-found is the convention several reference clients
// rely on.
func (s *Server) handleResourcesRead(ctx context.Context, req *Request) *Response {
	var p resourcesReadParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrInvalidParams, "resources/read params: "+err.Error())
	}
	if p.URI == "" {
		return errorResp(req.ID, ErrInvalidParams, "resources/read: uri is required")
	}

	s.mu.RLock()
	if r, ok := s.resources[p.URI]; ok {
		reader := r.Reader
		mime := r.MimeType
		s.mu.RUnlock()
		text, err := reader(ctx, p.URI)
		if err != nil {
			return errorResp(req.ID, ErrInternal, "read resource: "+err.Error())
		}
		return successResp(req.ID, map[string]any{
			"contents": []resourceContent{{URI: p.URI, MimeType: mime, Text: text}},
		})
	}
	// Templates by prefix+suffix match. On the order of 10 templates
	// expected, not 10000, so the O(n) scan is fine.
	tmpls := append([]ResourceTemplate(nil), s.resourceTemplates...)
	s.mu.RUnlock()
	for _, rt := range tmpls {
		if uriMatches(rt.URITemplate, p.URI) {
			text, err := rt.Reader(ctx, p.URI)
			if err != nil {
				return errorResp(req.ID, ErrInternal, "read resource: "+err.Error())
			}
			return successResp(req.ID, map[string]any{
				"contents": []resourceContent{{URI: p.URI, MimeType: rt.MimeType, Text: text}},
			})
		}
	}
	return errorResp(req.ID, ErrMethodNotFound, fmt.Sprintf("resource not found: %s", p.URI))
}

// uriMatches is the tiny URI-Template matcher. Only handles the
// "literal {placeholder} literal" pattern (RFC 6570 level 1) —
// good enough for "coremetry://trace/{trace_id}" style URIs we
// expose. A second placeholder silently fails to match, which is
// the safe direction (a bad pattern matches nothing rather than
// matching everything).
func uriMatches(template, concrete string) bool {
	openIdx := indexByte(template, '{')
	if openIdx < 0 {
		return template == concrete
	}
	closeIdx := indexByte(template, '}')
	if closeIdx < openIdx {
		return false
	}
	prefix := template[:openIdx]
	suffix := template[closeIdx+1:]
	if indexByte(suffix, '{') >= 0 {
		return false
	}
	if !startsWith(concrete, prefix) {
		return false
	}
	if !endsWith(concrete, suffix) {
		return false
	}
	mid := concrete[len(prefix) : len(concrete)-len(suffix)]
	return mid != ""
}

// ExtractURITemplateValue pulls the placeholder value out of a
// concrete URI matched against its template. Exported so concrete
// Reader implementations don't have to re-implement the slicing.
// Returns "" if the template has no placeholder or the URI
// doesn't match.
func ExtractURITemplateValue(template, concrete string) string {
	openIdx := indexByte(template, '{')
	if openIdx < 0 {
		return ""
	}
	closeIdx := indexByte(template, '}')
	if closeIdx < openIdx {
		return ""
	}
	prefix := template[:openIdx]
	suffix := template[closeIdx+1:]
	if !startsWith(concrete, prefix) || !endsWith(concrete, suffix) {
		return ""
	}
	return concrete[len(prefix) : len(concrete)-len(suffix)]
}

// Tiny byte helpers kept inline — pulling strings just for these
// felt like the kind of thing /scale-audit would call out.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// ── prompts/list, prompts/get ──────────────────────────────────

type promptListEntry struct {
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Arguments   []promptArgumentEntry `json:"arguments,omitempty"`
}

type promptArgumentEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

func (s *Server) handlePromptsList(req *Request) *Response {
	s.mu.RLock()
	out := make([]promptListEntry, 0, len(s.prompts))
	for _, p := range s.prompts {
		args := make([]promptArgumentEntry, 0, len(p.Arguments))
		for _, a := range p.Arguments {
			args = append(args, promptArgumentEntry{
				Name:        a.Name,
				Description: a.Description,
				Required:    a.Required,
			})
		}
		out = append(out, promptListEntry{
			Name:        p.Name,
			Description: p.Description,
			Arguments:   args,
		})
	}
	s.mu.RUnlock()
	return successResp(req.ID, map[string]any{"prompts": out})
}

type promptsGetParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// handlePromptsGet runs the registered renderer with the
// client-supplied args. Required args are checked at the
// protocol layer so the renderer can trust they're present.
func (s *Server) handlePromptsGet(ctx context.Context, req *Request) *Response {
	var p promptsGetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrInvalidParams, "prompts/get params: "+err.Error())
	}
	if p.Name == "" {
		return errorResp(req.ID, ErrInvalidParams, "prompts/get: name is required")
	}
	s.mu.RLock()
	prompt, ok := s.prompts[p.Name]
	s.mu.RUnlock()
	if !ok {
		return errorResp(req.ID, ErrMethodNotFound, fmt.Sprintf("prompt not found: %s", p.Name))
	}
	if p.Arguments == nil {
		p.Arguments = map[string]string{}
	}
	for _, a := range prompt.Arguments {
		if a.Required && p.Arguments[a.Name] == "" {
			return errorResp(req.ID, ErrInvalidParams,
				fmt.Sprintf("prompts/get %q: missing required arg %q", p.Name, a.Name))
		}
	}
	msgs, err := prompt.Renderer(ctx, p.Arguments)
	if err != nil {
		return errorResp(req.ID, ErrInternal, "render prompt: "+err.Error())
	}
	return successResp(req.ID, map[string]any{
		"description": prompt.Description,
		"messages":    msgs,
	})
}
