---
name: copilot-surface
description: Add a new AI Copilot "✨ Explain" surface to Coremetry — system prompt, API handler, wrapper, frontend button. Use when the user wants AI explanation behaviour on a new page/panel (e.g. "explain this metric anomaly", "explain this database query"). Routes through `s.copilotExplain(r, …)` so the /ai surface attribution stays accurate.
---

# /copilot-surface — add a new AI explain surface

Each "✨ Explain X" button in Coremetry follows a well-trodden
pattern (see `internal/copilot/copilot.go` bottom for prior art:
SystemPromptTrace, SystemPromptSpan, SystemPromptSLOBurn,
SystemPromptSlowQuery, etc.). This skill walks the agent through
the 5 files to touch + the conventions to follow.

The 10-step "When you ship a new feature" checklist in CLAUDE.md
collapses to 5 here because Copilot surfaces are read-only and
share the existing infrastructure — no schema change, no settings
persistence (Copilot config already lives in `system_settings`),
no audit row (Copilot is configured-or-not, not RBAC-gated).

## Args

`/copilot-surface <name>` — short kebab-case name for the new
surface, e.g. `explain-flow`, `explain-cardinality`, `runbook-incident`.

If omitted, ask the user. Don't invent a surface name.

## Conventions (from CLAUDE.md)

- AI Copilot system prompts live in `internal/copilot/copilot.go`
  bottom, exported as `SystemPromptX() string`.
- All Copilot endpoints go through `s.copilotExplain(r, ...)`
  wrapper so the /ai surface attribution stays accurate.
  Never call `s.copilot.Explain` directly — that bypasses the
  `ai_calls` recorder and the `/ai` page goes blind.
- Surface name is derived from the URL path
  (`/api/copilot/explain-X` → `"explain-X"`) by the helper in
  `internal/api/ai_observability.go`.

## Files to touch (5)

### 1. `internal/copilot/copilot.go`

Add the system prompt at the bottom, following the existing
shape:

```go
// systemX — operator hits "✨ Explain" on <surface>. Prompt
// receives <inputs>. Goal: <one-line goal>.
//
// Bound: short. Operator is in triage mode, not study mode.
const systemX = `You are a senior <role> assistant inside an APM
tool. The operator clicked "Explain" on <thing>. You receive:
<list of fields>.

Respond in 3-5 short bullets:
  (1) one-line verdict: <list of canonical verdicts>.
  (2) <specific hazard you see, anchored to the data>.
  (3) <highest-impact remediation, one best fix not five>.
  (4) optional: <second-tier improvement>.

Anchor on the data you have. Don't speculate beyond what you
were shown. Don't hedge.`

func SystemPromptX() string { return systemX }
```

Patterns to copy:
- Lead with "You are a senior X assistant inside an APM tool."
- Enumerate the input shape so the model knows what it has.
- Constrain output to 3-5 bullets.
- Demand a one-line verdict + specific quote / clause / number.
- Demand ONE best fix, not a menu.
- Explicit "don't hedge" / "don't speculate" at the end.

### 2. `internal/api/api.go` (route)

Register the route:

```go
mux.HandleFunc("POST /api/copilot/explain-X", s.copilotExplainX)
```

Same auth gate as other Copilot endpoints (no role wrapper —
the Copilot itself is configured-or-not). If the surface needs
data the operator wouldn't otherwise see, gate it.

### 3. `internal/api/api.go` (handler)

Add the handler near the other copilotExplainX functions:

```go
func (s *Server) copilotExplainX(w http.ResponseWriter, r *http.Request) {
    if !s.copilot.Configured() {
        http.Error(w, "AI copilot not configured", http.StatusServiceUnavailable)
        return
    }
    // Read inputs — either from the request body (when the
    // frontend already has the data on hand) or from the
    // chstore (when the operator only knows a key like an id).
    var body struct {
        // … fields
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
        return
    }

    // Compose the user-prompt string from the inputs. Keep it
    // tight; cap any free-text fields (e.g. SQL, log bodies)
    // at ~4KB so the prompt + recording stay cheap.
    var sb strings.Builder
    fmt.Fprintf(&sb, "...", body.X, body.Y)

    out, err := s.copilotExplain(r, copilot.SystemPromptX(), sb.String())
    if err != nil {
        writeErr(w, err)
        return
    }
    writeJSON(w, map[string]any{"explanation": out})
}
```

**Critical:** use `s.copilotExplain(r, …)`, NOT
`s.copilot.Explain(r.Context(), …)`. The wrapper attributes the
call to the surface for `/ai` analytics + records the `ai_calls`
row. Direct calls silently break the dashboard.

### 4. `frontend/src/lib/api.ts`

Add the client method:

```ts
copilotExplainX: (body: { /* matching the handler body */ }) =>
  request<{ explanation: string }>(`/api/copilot/explain-X`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }),
```

If you need a shared type for the response, add it to
`lib/types.ts` rather than re-declaring in the component
(CLAUDE.md "Frontend type discipline").

### 5. Frontend button

Wire the button on the relevant page. Standard pattern:

```tsx
// state for explain — keyed if the page has multiple invocable rows
type ExplainState = 'idle' | 'busy' | { text: string } | { error: string };
const [explainState, setExplainState] = useState<ExplainState>('idle');

const askCopilot = async () => {
  setExplainState('busy');
  try {
    const r = await api.copilotExplainX({ /* fields */ });
    setExplainState({ text: r.explanation });
  } catch (e) {
    setExplainState({ error: e instanceof Error ? e.message : String(e) });
  }
};
```

Button shape (mirror SlowQueries.tsx for inline / explains;
mirror Slos.tsx BurnExplainButton for modal-based):

```tsx
{explainState === 'busy' ? (
  <span style={{ color: 'var(--text3)' }}>✨ Thinking…</span>
) : (
  <button className="sec" onClick={askCopilot}
    style={{ fontSize: 11, padding: '4px 10px', color: 'var(--accent2)' }}
    title="Ask Copilot for …">
    ✨ Explain
  </button>
)}
```

Render the answer inline OR in a panel, depending on the page
density. Slow-query rows render inline; SLO row → opens a Modal.
Match neighbouring patterns rather than inventing.

## Verification

After the 5 files are touched:

1. `go build ./...` — handler + system prompt compile.
2. `cd frontend && npx tsc --noEmit` — api.ts + button type-check.
3. Trigger the button manually in the running app (or simulate
   via `curl -X POST /api/copilot/explain-X -d '{…}'`).
4. **Verify `/ai` attribution.** A row should land with
   `surface = "explain-X"` + sensible token counts. If the row
   doesn't appear, you've bypassed `s.copilotExplain` somewhere
   — that's the single most common failure mode and the whole
   point of the wrapper.

## Then ship

Use `/release "add ✨ Explain X surface"` to commit + push +
rebuild. Surface attribution will start flowing on the next
operator click.

## Anti-patterns

- **Don't bypass `copilotExplain`.** Direct `s.copilot.Explain`
  calls skip the recorder, making /ai blind to the new surface.
- **Don't write a 2-paragraph system prompt.** The model
  performs better on tight, opinionated prompts than verbose
  ones. 5-15 lines is the right length.
- **Don't pass entire CH responses through.** Cap free-text
  fields. The ai_calls row caps samples at 4KB anyway — beyond
  that the data is truncated server-side, so you're paying
  prompt tokens for nothing.
- **Don't ship without the /ai surface attribution working.**
  The whole point of the wrapper is operator visibility into
  AI usage. Verify the surface name appears in /ai before
  shipping.
- **Don't add a new Copilot config setting per surface.**
  Provider/model/key all live in `system_settings` under the
  existing `copilot` key — that's what `LoadPersisted` already
  hydrates. New surfaces consume the same config, don't fan
  out new ones.
