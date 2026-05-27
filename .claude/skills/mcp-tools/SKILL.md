---
name: mcp-tools
description: Adding a new tool to Coremetry's MCP server. Reach for this BEFORE editing internal/mcptools/ or adding a method an external LLM will call. Captures the Deps closure + range_s convention + clamp pattern + description-writing style + auth gating.
---

# /mcp-tools — add a Model Context Protocol tool

Coremetry exposes a JSON-RPC 2.0 MCP server (spec 2024-11-05) over
HTTP+SSE so external LLMs (Claude Desktop, agent frameworks) can
query telemetry. Infrastructure shipped v0.6.4-v0.6.7:

| Concern | Where |
|---|---|
| Protocol layer (registry, JSON-RPC, SSE) | `internal/mcp/mcp.go` |
| Concrete tools (closures over chstore + logstore) | `internal/mcptools/tools.go` |
| Resources (URI-addressed snapshots) | `internal/mcptools/tools.go` (`registerResources`) |
| Prompts (curated system+user pairs) | `internal/mcptools/prompts.go` |
| Boot wiring | `main.go` (Register call after mcp.Server construction) |

This skill captures the conventions a new tool MUST follow. They
exist because LLMs are different consumers than the React UI is:
small context windows, error-tolerant input shapes, descriptions
they read to decide *whether* to call.

## When to add an MCP tool vs. just an API route

Add an MCP tool when:
- An LLM should be able to use this surface autonomously during
  an investigation (e.g. "find the failing service", "show me
  recent errors").
- The surface returns structured data that fits in <2k tokens.
  Anything bigger blows the LLM's context.
- An equivalent HTTP route already exists — MCP tools are usually
  a thin wrapper around the same `chstore.Store` method the
  React UI uses.

Don't add an MCP tool for:
- Mutations (create/update/delete) without operator-in-the-loop.
  Roles still apply (viewer/editor/admin enforced via the JWT
  middleware), but an LLM acknowledging a problem unprompted is
  blast-radius the operator hasn't approved.
- Pure UI affordances (table sort, chart pan). These exist for
  human eyes; an LLM doesn't need them.
- Surfaces that return >2k tokens worth of data. Build a paged
  / summary variant first.

## Steps

### 1. Pick the chstore (or logstore) method the tool will call

MCP tools are thin wrappers. The data path stays in `chstore.Store`
— the tool just exposes it with an LLM-shaped contract. If the
method doesn't exist yet, add it to chstore FIRST (per the
`/clickhouse-schema` skill), THEN come back here.

### 2. Define a typed args struct

Sits in `internal/mcptools/tools.go` near the tool factory. Naming
convention is `<toolName>Args`. JSON tags use `snake_case` because
that's what the JSON Schema layer below mirrors.

```go
type listServicesArgs struct {
    NameContains string `json:"name_contains,omitempty"`
    RangeS       int    `json:"range_s,omitempty"`
    Limit        int    `json:"limit,omitempty"`
}
```

Required fields don't carry `omitempty`. Optional fields do — the
LLM is more likely to omit a field than to send an explicit zero
value.

### 3. Use `range_s`, not `from` / `to` nanos

This is the single most-violated convention. LLMs are notoriously
bad at constructing 19-digit unix-nanosecond timestamps, especially
for relative windows ("last 30 minutes"). The repo's chstore
methods take from/to time.Time — the TOOL converts `range_s` into
that pair via:

```go
from, to := rangeWindow(a.RangeS) // helper in tools.go
```

`rangeWindow(0)` returns a sane default (30min). The tool's JSON
Schema declares `range_s` with both `minimum: 0` and `maximum`
(typically 604800 = 7 days) so the LLM can't fan a request that'll
scan three months of partitions.

If the chstore method really needs absolute timestamps from the
client (rare — only "look up trace by id at exact time"), accept
them as `time_iso8601` strings instead, parsed via
`time.Parse(time.RFC3339, ...)`. Still don't ask the LLM for
nanoseconds.

### 4. Cap the result count with `clampLimit`

```go
limit := clampLimit(a.Limit, /*default*/ 50, /*max*/ 500)
```

Hard rule: server-side cap is the backstop. Don't trust the LLM
to set Limit. The default is for the LLM-without-an-opinion call;
the max is for context-window safety.

Typical caps:
- list-* tools: default 50, max 500
- per-row detail tools (get_*): not paged, but the BODY should be
  bounded (truncate large strings, etc.)
- search/query tools: default 100, max 1000

### 5. Build the `mcp.Tool` registration

```go
func myTool(d Deps) mcp.Tool {
    return mcp.Tool{
        Name:        "snake_case_tool_name",
        Description: "<one-paragraph contract — see step 6>",
        InputSchema: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "name_contains": map[string]any{
                    "type":        "string",
                    "description": "Substring match. Empty = all.",
                },
                "range_s": map[string]any{
                    "type":        "integer",
                    "minimum":     0,
                    "maximum":     604800,
                    "description": "Lookback seconds. Default 1800.",
                },
            },
            "required": []string{"name_contains"}, // only if truly required
        },
        Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
            var a myToolArgs
            if len(raw) > 0 {
                if err := json.Unmarshal(raw, &a); err != nil {
                    return nil, fmt.Errorf("decode args: %w", err)
                }
            }
            from, to := rangeWindow(a.RangeS)
            limit := clampLimit(a.Limit, 50, 500)
            rows, err := d.Store.GetXFiltered(ctx, ..., from, to, ..., limit, 0)
            if err != nil {
                return nil, err
            }
            return map[string]any{"rows": rows, "count": len(rows)}, nil
        },
    }
}
```

### 6. Write the description like a contract, not a sentence

The description is the **only** signal the LLM has for deciding
whether to call your tool. It's not user docs. Three things to
include, in order:

1. **What it returns.** "RED metrics + open problem count for one
   service." Not "queries the service summary table".
2. **When to use it.** "Use after `list_services` to drill into a
   specific service." This is how the LLM chains tools.
3. **Cost/scope warning if any.** "Reads the 5-minute pre-
   aggregate so cheap to call repeatedly." Or "scans raw spans;
   keep range_s small."

Bad: `"Lists services."`
Good: `"List Coremetry services with their current RPS, error rate, and p99 latency. Reads the 5-minute pre-aggregate so it's cheap to call repeatedly. Use this as the entry point when investigating an incident: 'which services are unhealthy right now?'"`

### 7. Mirror args → JSON Schema EXACTLY

If the struct field is `NameContains string \`json:"name_contains"\``,
the JSON Schema property is `"name_contains": {"type": "string"}`.
Mismatches silently break tool calls — the args unmarshal to a
zero-value struct and the tool returns "all services" instead of
the filter the LLM asked for.

JSON Schema types map: `string`, `integer`, `number`, `boolean`,
`array`, `object`. Use `minimum` / `maximum` for integer bounds,
`enum` for string allowlists, `description` for free-form
guidance the LLM reads.

### 8. Register in `Register()`

End of `internal/mcptools/tools.go`'s `Register(srv, d)`. Order
doesn't matter — the LLM gets the full catalogue on `tools/list`.

### 9. Auth gating

MCP tools inherit the api server's JWT middleware: viewer / editor
/ admin roles flow through the same path as REST. The chstore
method should NOT carry role logic — the route layer does. For a
mutation tool, also call `s.audit(...)` from inside the handler
(through Deps if needed) so the action lands in the audit log.

A read-only tool that surfaces data the viewer role can already
see in the UI needs no extra gating beyond JWT presence.

### 10. Boot wiring sanity

`main.go` constructs `mcp.NewServer(...)`, then calls
`mcptools.Register(srv, mcptools.Deps{Store: store, LogStore:
logstore})`, then `api.NewServer(...).SetMCP(srv)`. The order
matters: tools must be registered BEFORE the SSE endpoint starts
accepting traffic, or early `tools/list` requests will return
empty.

This is already wired — don't move it. If you add a new Deps
field (e.g. `Tempo *tempo.Service`), thread it through
`mcptools.Deps` and the `Register` call in `main.go`.

### 11. Test by hand (no integration test suite yet)

There's no test layer for mcptools. Smoke-test by hitting the
MCP HTTP endpoint with a JSON-RPC envelope:

```bash
curl -sS -b /tmp/cm.cookies -X POST http://localhost:8088/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call",
       "params":{"name":"my_tool",
                 "arguments":{"range_s":300,"limit":5}}}' \
  | python3 -m json.tool
```

Check: tool found, args parsed (no `decode args` error in the
body), expected shape returned, count reasonable.

### 12. Ship via `/release`

Single `v0.6.X — feature: MCP tool <name>` commit. Brief body
describing what it surfaces + when an LLM would reach for it.
Update the `## Tool catalogue` comment block at top of `tools.go`.

## Patterns you should not invent locally

- **Don't write your own range parser.** Use `rangeWindow(s)`.
- **Don't return raw chstore types directly.** Wrap in
  `map[string]any{"<thing>": rows, "count": ..., "window_s":
  ...}` so the LLM has signals about what each field is.
- **Don't reach into the api.Server.** Tools are stateless
  closures over Deps. If something needs the http.Request, it's
  a route, not an MCP tool.
- **Don't add OAuth / OIDC handshake inside the tool.** JWT
  middleware handles it.

## Resources + Prompts — adjacent surfaces

If your "tool" really exposes a **pinned snapshot** an LLM should
attach to its context (no args, stable URI), register a
**Resource** instead — example pattern in `registerResources()`.
URIs follow `coremetry://<noun>` or `coremetry://<noun>/<id>`.

If your "tool" really is a **curated investigation template**
(system+user message pair the LLM should run on demand), register
a **Prompt** in `prompts.go`. The in-app ✨ Explain buttons are
the canonical examples.

If you're unsure: tools are verbs (`list_X`, `get_Y`, `search_Z`),
resources are nouns (`coremetry://services`), prompts are tasks
(`explain_problem`, `compare_traces`).

## Anti-patterns

- **Don't expose mutations as tools without an operator gate.**
  An LLM autonomously ack'ing problems is a postmortem waiting
  to happen.
- **Don't accept `from_ns` / `to_ns` instead of `range_s`.** Every
  LLM that hits the tool will get the nanosecond math wrong; you'll
  spend the next bug-fix release explaining "why is the LLM asking
  about year 56000".
- **Don't omit `description` on properties.** The LLM treats an
  un-described property as guessable. It will guess wrong.
- **Don't lie in the tool description.** "Reads the 5-minute
  pre-aggregate" promises an SLO — if the chstore method actually
  scans raw spans, your tool will time out under load and the LLM
  will retry, and retry, and retry.
- **Don't add a "debug" tool that returns the raw SQL.** Pure
  blast-radius; an LLM can leak it. If you need that during
  development, use `/admin/sql` in the UI.
