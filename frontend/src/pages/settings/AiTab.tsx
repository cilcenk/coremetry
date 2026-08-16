import { useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button, useConfirm } from '@/components/ui';
import { api } from '@/lib/api';
import { useSettingsLoad, SettingsLoadError, Field } from './shared';
import type { AIProvider, AISettings } from '@/lib/types';
import { tuningToForm, tuningToWire } from './aiTuning';
import { IconSparkles } from '@/components/icons';
import { Link } from 'react-router-dom';

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
  const confirm = useConfirm();
  const [provider, setProvider] = useState<AIProvider>('anthropic');
  const [model, setModel] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [hasKey, setHasKey] = useState(false);
  const [apiKey, setApiKey] = useState('');
  const [skipTls, setSkipTls] = useState(false);
  // wf — master on/off toggle, distinct from hasKey. Default true so a
  // fresh / legacy backend (no "enabled" field) renders as enabled.
  const [enabled, setEnabled] = useState(true);
  // v0.9.1120 (Faz 0.5) — LLM call tuning. Held as STRINGS, not
  // numbers, and that is the whole design:
  //
  // The backend returns OVERRIDES. maxTokens 0 / temperature null /
  // timeoutS 0 all mean "no override — running the built-in default"
  // (4096 / 0.2 / 180s). A number-typed state would have to pick some
  // value for "unset", and every candidate is wrong: 0 renders a
  // nonsense "0" in the box, and the default renders a value the
  // operator never chose — which the next Save would then WRITE into
  // the blob, freezing today's default forever and silently opting
  // this install out of any future default change.
  //
  // '' is the only honest representation of unset, so the inputs are
  // strings, the defaults appear as PLACEHOLDER text, and blank round-
  // trips back to 0 / null = reset.
  const [maxTokens, setMaxTokens] = useState('');
  const [temperature, setTemperature] = useState('');
  const [timeoutS, setTimeoutS] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  // Çeviri aiTuning.ts'te ve TABLO-TESTLİ: "boş kutu = sıfırla"
  // sözleşmesinin yanlış dalı sessiz ayar donmasına yol açıyor, o
  // yüzden bileşenin içinde satır içi kalmıyor.
  const applyTuning = (s: Partial<AISettings>) => {
    const f = tuningToForm(s);
    setMaxTokens(f.maxTokens);
    setTemperature(f.temperature);
    setTimeoutS(f.timeoutS);
  };

  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.getAISettings(),
    s => {
      setProvider(s.provider || 'anthropic');
      setModel(s.model || '');
      setBaseUrl(s.baseUrl || '');
      setHasKey(s.hasKey);
      setSkipTls(s.skipTls ?? false);
      setEnabled(s.enabled ?? true);
      applyTuning(s);
    },
  );

  // tuning — the knob triple EVERY PUT must carry, including the
  // Remove-key path. The PUT body is a whole-blob replace, so a call
  // that omits these resets the operator's overrides as a side effect
  // of an unrelated action ("I removed the key and my timeout went
  // back to 180s" — invisible until the next slow local generation).
  const tuning = () => tuningToWire({ maxTokens, temperature, timeoutS });

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey, model, baseUrl, skipTls, enabled, ...tuning() });
      setHasKey(next.hasKey);
      setSkipTls(next.skipTls ?? false);
      setEnabled(next.enabled ?? true);
      // Echo the SERVER's view back: it is the authority on what the
      // stored override now is, and a rejected/normalised value must
      // not linger in the box looking saved.
      applyTuning(next);
      setApiKey('');
      setMsg({
        kind: 'ok',
        text: !next.enabled
          ? (next.hasKey ? 'Saved — CoSRE disabled (key kept).' : 'Saved — CoSRE disabled.')
          : (next.hasKey || (provider === 'openai' && baseUrl) ? 'Saved — CoSRE is live.' : 'Saved — CoSRE dormant (no key).'),
      });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const clearKey = async () => {
    if (!await confirm({
      title: 'Kayıtlı API anahtarı silinsin mi?',
      body: <>Anahtar sunucudan kaldırılacak ve <b>CoSRE düğmeleri</b> yeni bir
        anahtar girilene kadar tüm yüzeylerden kaybolacak.</>,
      confirmLabel: 'Anahtarı sil',
      danger: true,
    })) return;
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey: '', model, baseUrl, skipTls, enabled, ...tuning() });
      setHasKey(next.hasKey);
      setSkipTls(next.skipTls ?? false);
      setEnabled(next.enabled ?? true);
      applyTuning(next);
      setApiKey('');
      setMsg({ kind: 'ok', text: 'Key cleared — CoSRE is dormant.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Clear failed' });
    } finally {
      setBusy(false);
    }
  };

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
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
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>CoSRE</h2>
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
                  ? `Provider: ${providerLabel} — credentials kept, CoSRE turned off.`
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
            <div>Enable CoSRE</div>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2, lineHeight: 1.5 }}>
              Master switch. Uncheck + Save to turn CoSRE off
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

        {/* v0.9.1120 (Faz 0.5) — call tuning. Every box is an OVERRIDE:
            the built-in default lives in the placeholder, so an empty
            box is not "missing config", it is "use the default". Clear
            a box + Save = reset that knob. Bounds are enforced
            server-side (copilot.ValidateTuning) and repeated in the
            hint so a rejected save is predictable, not a surprise. */}
        <div style={{ fontSize: 11.5, fontWeight: 600, color: 'var(--text2)',
                      marginBottom: 8, letterSpacing: 0.2 }}>
          Call tuning <span style={{ fontWeight: 400, color: 'var(--text3)' }}>
            — leave a box empty to use the built-in default
          </span>
        </div>

        <Field label="Max tokens (optional)">
          <input type="number" inputMode="numeric" min={256} max={32768} step={256}
                 value={maxTokens} onChange={e => setMaxTokens(e.target.value)}
                 placeholder="4096 (default)" style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
            Completion budget per call. Range <b>256–32768</b>. Too low and
            explanations get truncated mid-sentence; too high and a chatty
            model burns quota on every ✨ button.
          </div>
        </Field>

        <Field label="Temperature (optional)">
          <input type="number" inputMode="decimal" min={0} max={2} step={0.1}
                 value={temperature} onChange={e => setTemperature(e.target.value)}
                 placeholder="0.2 (default)" style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
            Range <b>0–2</b>. Low keeps explanations reproducible, which is
            what an APM wants — two operators reading the same incident
            should get the same answer. Anthropic caps at 1 and rejects
            higher; the provider, not Coremetry, enforces that.
          </div>
        </Field>

        <Field label="Request timeout (seconds, optional)">
          <input type="number" inputMode="numeric" min={10} max={600} step={10}
                 value={timeoutS} onChange={e => setTimeoutS(e.target.value)}
                 placeholder="180 (default)" style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
            Range <b>10–600</b>. A local LLM loading a large model cold can
            need 60s+ for a first generation. Above your reverse proxy /
            ingress timeout the value is a lie — the hop in front of
            Coremetry cuts the request first.
          </div>
        </Field>

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
                  disabled={!apiKey && !hasKey && !(provider === 'openai' && !!baseUrl)} loading={busy}>
            Save
          </Button>
          {/* v0.9.1006 (M4/O6) — dolu Save'in yanında ikinci dolgu
              olmasın; kırmızı dil `ghost-danger`la korunuyor. */}
          {hasKey && (
            <Button type="button" variant="ghost-danger" onClick={clearKey} disabled={busy}>
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
            <li><b><IconSparkles /></b> column on the <Link to="/problems" style={{ color: 'var(--accent2)' }}>Problems</Link> page —
              plain-language meaning + ranked likely causes + first three things to check.</li>
          </ul>
        </div>
      )}
    </div>
  );
}


// RAG / doküman soru-cevap bölümü v0.8.491'de kendi sekmesine taşındı:
// pages/settings/KnowledgeTab.tsx (/settings/knowledge).
