import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { AIProvider } from '@/lib/types';
import { IconSparkles } from '@/components/icons';

// AITab — editable AI Copilot configuration. Admin picks a provider,
// pastes their key, optionally sets a model, hits Save. Server stores
// the override in system_settings and updates the live service so the
// next Explain call uses the new creds without restart.
//
// Two providers:
//   - Anthropic: classic sk-ant-… key.
//   - GitHub Copilot: GitHub OAuth token (ghu_…) with Copilot access;
//     server exchanges it for a session token and calls
//     api.githubcopilot.com (OpenAI-compatible).
export function AITab() {
  const [loaded, setLoaded] = useState(false);
  const [provider, setProvider] = useState<AIProvider>('anthropic');
  const [model, setModel] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [hasKey, setHasKey] = useState(false);
  const [apiKey, setApiKey] = useState('');
  const [skipTls, setSkipTls] = useState(false);
  // wf — master on/off toggle, distinct from hasKey. Default true so a
  // fresh / legacy backend (no "enabled" field) renders as enabled.
  const [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAISettings().then(s => {
      setProvider(s.provider || 'anthropic');
      setModel(s.model || '');
      setBaseUrl(s.baseUrl || '');
      setHasKey(s.hasKey);
      setSkipTls(s.skipTls ?? false);
      setEnabled(s.enabled ?? true);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey, model, baseUrl, skipTls, enabled });
      setHasKey(next.hasKey);
      setSkipTls(next.skipTls ?? false);
      setEnabled(next.enabled ?? true);
      setApiKey('');
      setMsg({
        kind: 'ok',
        text: !next.enabled
          ? (next.hasKey ? 'Saved — Copilot disabled (key kept).' : 'Saved — Copilot disabled.')
          : (next.hasKey || (provider === 'openai' && baseUrl) ? 'Saved — Copilot is live.' : 'Saved — Copilot dormant (no key).'),
      });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const clearKey = async () => {
    if (!confirm('Remove the saved API key? Copilot buttons will disappear until a new key is set.')) return;
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey: '', model, baseUrl, skipTls, enabled });
      setHasKey(next.hasKey);
      setSkipTls(next.skipTls ?? false);
      setEnabled(next.enabled ?? true);
      setApiKey('');
      setMsg({ kind: 'ok', text: 'Key cleared — Copilot is dormant.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Clear failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  // Per-provider hint shown under the key field — explains where to
  // get the token + what shape it has, so users don't paste the wrong
  // thing.
  const keyHint = provider === 'github' ? (
    <>
      Paste a GitHub OAuth token with Copilot access (starts with{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>ghu_</code>).
      You can copy it from{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>~/.config/github-copilot/hosts.json</code>{' '}
      or run your own OAuth flow. Coremetry exchanges it for a Copilot session token automatically.
    </>
  ) : provider === 'openai' ? (
    <>
      Drives any OpenAI-compatible <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>/v1/chat/completions</code> endpoint —
      real OpenAI, Ollama, LM Studio, vLLM, llama.cpp server, LocalAI, OpenWebUI.
      Set <b>Base URL</b> below to your endpoint (e.g. <code>http://ollama:11434/v1</code>).
      API key is optional for local endpoints that don't gate on it (Ollama default).
    </>
  ) : (
    <>
      Paste your Anthropic API key (starts with{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>sk-ant-</code>).
      Get one at{' '}
      <a href="https://console.anthropic.com/settings/keys" target="_blank" rel="noopener"
         style={{ color: 'var(--accent2)' }}>console.anthropic.com</a>.
    </>
  );

  const modelPlaceholder =
    provider === 'github' ? 'gpt-4o (default)' :
    provider === 'openai' ? 'gpt-4o-mini / llama3.1 / qwen2.5-coder …' :
    'claude-sonnet-4-6 (default)';

  const providerLabel =
    provider === 'github' ? 'GitHub Copilot' :
    provider === 'openai' ? 'OpenAI-compatible' :
    'Anthropic';

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>AI Copilot</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Inline natural-language explanations for traces, Problems and exceptions.
        Pick a provider, paste your key, save — buttons appear automatically on the
        trace detail page and the Problems table. The OpenAI-compatible provider
        targets self-hosted local LLMs (Ollama / LM Studio / vLLM …) so traces
        never leave your perimeter.
      </p>

      {(() => {
        // Live state in three tiers: configured-and-enabled (active),
        // configured-but-disabled (creds kept, AI off), or not
        // configured. wf: the disabled tier is the whole point of the
        // toggle — show it distinctly so the operator sees AI is off
        // without thinking the key was lost.
        const configured = hasKey || (provider === 'openai' && !!baseUrl);
        const active = configured && enabled;
        const tier = active ? 'operational' : 'degraded';
        const label = active ? 'ACTIVE' : configured ? 'DISABLED' : 'NOT CONFIGURED';
        return (
          <div className={`status-banner status-banner-${tier}`}>
            <span className={`status-pill status-pill-${tier}`}>{label}</span>
            <span style={{ fontWeight: 600, fontSize: 14 }}>
              {active
                ? (hasKey
                    ? `Provider: ${providerLabel} — ready.`
                    : `Provider: ${providerLabel} (no auth) — ready at ${baseUrl}.`)
                : configured
                  ? `Provider: ${providerLabel} — credentials kept, AI Copilot turned off.`
                  : 'Not configured. Paste a key (or set a local endpoint URL) below.'}
            </span>
          </div>
        );
      })()}

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        {/* Master on/off toggle (wf). Disabling stops the background
            problem-explainer, hides the in-app AI affordances, and
            503s the AI endpoints — all WITHOUT touching the stored
            key, so re-enabling is one click. Same checkbox markup as
            Skip-TLS below so the controls read as one family. */}
        <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8,
                        marginBottom: 12, fontSize: 12, color: 'var(--text2)' }}>
          <input type="checkbox" checked={enabled}
                 onChange={e => setEnabled(e.target.checked)}
                 style={{ marginTop: 2 }} />
          <div>
            <div>Enable AI Copilot</div>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2, lineHeight: 1.5 }}>
              Master switch. Uncheck + Save to turn AI Copilot off
              without removing the stored key — the background
              problem-explainer stops, the ✨ Explain buttons hide,
              and AI endpoints return 503. Re-check + Save to resume.
            </div>
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Provider</div>
          <select value={provider}
                  onChange={e => setProvider(e.target.value as AIProvider)}
                  style={{ width: '100%' }}>
            <option value="anthropic">Anthropic (Claude)</option>
            <option value="github">GitHub Copilot</option>
            <option value="openai">OpenAI-compatible (Ollama / LM Studio / vLLM / OpenAI)</option>
          </select>
        </label>

        {/* Base URL — only meaningful for the openai provider. The
            field is rendered for all providers but the openai branch
            is the only one that consumes it server-side; harmless
            otherwise (saved + ignored). Keeps the form layout
            stable when switching providers. */}
        {provider === 'openai' && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              Base URL
            </div>
            <input value={baseUrl} onChange={e => setBaseUrl(e.target.value)}
                   placeholder="http://ollama:11434/v1   (or https://api.openai.com/v1 for real OpenAI)"
                   autoComplete="off" style={{ width: '100%', fontFamily: 'monospace' }} />
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
              Endpoint must serve <code>/chat/completions</code> in OpenAI's request shape.
              Common paths: Ollama → <code>http://&lt;host&gt;:11434/v1</code>,
              LM Studio → <code>http://&lt;host&gt;:1234/v1</code>,
              vLLM → <code>http://&lt;host&gt;:8000/v1</code>.
            </div>
          </label>
        )}

        {/* TLS verification toggle (v0.5.360). Matches the same
            opt-in pattern the Tempo + LDAP integrations expose
            for self-hosted endpoints fronted by an internal CA
            Go's default trust store doesn't know about. Off by
            default — operator flips it deliberately. */}
        <label style={{ display: 'flex', alignItems: 'flex-start', gap: 8,
                        marginBottom: 12, fontSize: 12, color: 'var(--text2)' }}>
          <input type="checkbox" checked={skipTls}
                 onChange={e => setSkipTls(e.target.checked)}
                 style={{ marginTop: 2 }} />
          <div>
            <div>Skip TLS verification</div>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2, lineHeight: 1.5 }}>
              Disables certificate verification on the outbound HTTPS
              call to the AI provider. Useful for self-hosted LLMs
              behind an enterprise CA. Leave off for public endpoints
              (Anthropic, GitHub Copilot, OpenAI).
            </div>
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 6 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            API key {hasKey && <span style={{ color: 'var(--text3)' }}>(saved — leave empty to keep current)</span>}
            {provider === 'openai' && (
              <span style={{ color: 'var(--text3)' }}> (optional for local endpoints)</span>
            )}
          </div>
          <input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)}
                 placeholder={hasKey ? '••••••••••••••••' :
                   provider === 'github' ? 'ghu_…' :
                   provider === 'openai' ? 'sk-… (optional)' : 'sk-ant-…'}
                 autoComplete="off" style={{ width: '100%' }} />
        </label>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14, lineHeight: 1.5 }}>
          {keyHint}
        </div>

        <label style={{ display: 'block', marginBottom: 14 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Model (optional)</div>
          <input value={model} onChange={e => setModel(e.target.value)}
                 placeholder={modelPlaceholder} style={{ width: '100%' }} />
        </label>

        {msg && (
          <div style={{
            marginBottom: 12, padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          {/* Save is actionable whenever there's something to persist:
              a new key, an already-stored key, or an openai endpoint
              with no key. The last clause keeps the Enable toggle
              actionable on a no-auth-local install. */}
          <Button type="submit" variant="primary"
                  disabled={busy || (!apiKey && !hasKey && !(provider === 'openai' && !!baseUrl))}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
          {hasKey && (
            <Button type="button" variant="danger" onClick={clearKey} disabled={busy}>
              Remove key
            </Button>
          )}
        </div>
      </form>

      {hasKey && (
        <div style={{ marginTop: 18, padding: 16, borderRadius: 8,
          background: 'var(--bg2)', border: '1px solid var(--border)' }}>
          <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 8 }}>What it does</h3>
          <ul style={{ fontSize: 13, lineHeight: 1.7, color: 'var(--text)', paddingLeft: 18 }}>
            <li><b><IconSparkles /> Explain this trace</b> — on any trace detail page.</li>
            <li><b><IconSparkles /></b> column on the <a href="/problems" style={{ color: 'var(--accent2)' }}>Problems</a> page —
              plain-language meaning + ranked likely causes + first three things to check.</li>
          </ul>
        </div>
      )}
    </div>
  );
}


// RAG / doküman soru-cevap bölümü v0.8.491'de kendi sekmesine taşındı:
// pages/settings/KnowledgeTab.tsx (/settings/knowledge).
