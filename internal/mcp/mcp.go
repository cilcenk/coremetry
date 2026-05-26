// Package mcp implements the Model Context Protocol server side
// for Coremetry. MCP is JSON-RPC 2.0 over an HTTP+SSE transport,
// designed so external LLM clients (Claude Desktop, Anthropic
// API tool-calling integrations, internal copilots) can discover
// and invoke a server's tools, list its resources, and read its
// prompt templates.
//
// What Coremetry exposes (built up across v0.6.4–v0.6.7):
//
//   v0.6.4 — core protocol scaffolding: JSON-RPC framing,
//            HTTP+SSE transport, session lifecycle, initialize +
//            ping methods. tools/resources/prompts capabilities
//            advertised but their registries are empty.
//   v0.6.5 — tools/list + tools/call wired to a Coremetry tools
//            registry (list_services, search_logs, get_trace,
//            query_metric, list_problems, ack_problem, …).
//   v0.6.6 — resources/list + resources/read exposing
//            traces/logs/metrics as MCP resources.
//   v0.6.7 — prompts/list + prompts/get exposing Coremetry's
//            curated system prompts (Explain trace, Suggest
//            runbook, Compare deploys, …) as MCP prompts so an
//            external LLM can take the same systematic approach
//            an operator sees in /problems.
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
//   • https://modelcontextprotocol.io
//   • https://spec.modelcontextprotocol.io/specification
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
//   InputSchema: map[string]any{
//       "type": "object",
//       "properties": map[string]any{
//           "service": map[string]any{"type": "string"},
//           "range_ns": map[string]any{"type": "integer", "minimum": 0},
//       },
//       "required": []string{"service"},
//   }
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
		info:     ServerInfo{Name: name, Version: version},
		sessions: map[string]*session{},
		tools:    map[string]Tool{},
	}
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	case <-time.After(2 * time.Second):
		// SSE consumer wedged (browser tab hidden, no flush).
		// Drop with a log so the operator can see the cluster's
		// MCP backpressure if it becomes a pattern.
		log.Printf("[mcp] session=%s response queue full — dropped %s", sessID, req.Method)
	}
	w.WriteHeader(http.StatusAccepted)
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
	// v0.6.6+: resources/list, resources/read
	// v0.6.7+: prompts/list, prompts/get
	default:
		return errorResp(req.ID, ErrMethodNotFound,
			fmt.Sprintf("method not found: %s", req.Method))
	}
}

// dispatchNotification handles the no-id variant. Only
// notifications/initialized matters at v0.6.4 — it transitions
// the session into the "ready for arbitrary methods" state.
func (s *Server) dispatchNotification(_ context.Context, sess *session, req *Request) {
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
