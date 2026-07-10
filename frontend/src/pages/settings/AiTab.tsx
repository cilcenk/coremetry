import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { AIProvider } from '@/lib/types';
import { IconSparkles } from '@/components/icons';
import { Field2, FlashBox, Row } from './shared';

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
      <RagSection />
    </div>
  );
}


// ── RAG — doküman soru-cevap (v0.8.441) ─────────────────────────────
// Embedding endpoint'i + doküman kataloğu. Endpoint girilmedikçe RAG
// tamamen kapalı (chat bugünkü gibi çalışır); girilince yüklenen
// dokümanlar chat'te kaynak atıflı cevaplara dönüşür. Wiki/URL
// kaynağı v2'de bu panele eklenecek.
function RagSection() {
  const [cfg, setCfg] = useState<import('@/lib/types').RagConfigView | null | undefined>(undefined);
  const [docs, setDocs] = useState<import('@/lib/types').RagDocument[] | null | undefined>(undefined);
  const [apiKey, setApiKey] = useState('');
  const [sourcesText, setSourcesText] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const load = () => {
    api.getRagConfig().then(c => {
      setCfg(c);
      setSourcesText((c.sources ?? []).map(s0 =>
        s0.authHeader ? `${s0.url} | ${s0.authHeader}` : s0.url).join('\n'));
    }).catch(() => setCfg(null));
    api.listRagDocuments().then(r => setDocs(r.documents)).catch(() => setDocs(null));
  };
  useEffect(load, []);

  if (cfg === undefined) return <div style={{ marginTop: 24 }}><Spinner /></div>;
  if (cfg === null) return <div style={{ marginTop: 24 }}><Empty icon="📄" title="RAG ayarları yüklenemedi" /></div>;

  const save = async () => {
    setBusy(true); setMsg(null);
    try {
      const sources = sourcesText.split('\n')
        .map(l => l.trim()).filter(Boolean)
        .map(l => {
          const [url, hdr] = l.split('|').map(x => x.trim());
          return hdr ? { url, authHeader: hdr } : { url };
        });
      const next = await api.putRagConfig({
        endpoint: cfg.endpoint, model: cfg.model, enabled: cfg.enabled,
        topK: cfg.topK, apiKey: apiKey || undefined, sources,
      });
      setCfg(next); setApiKey('');
      setMsg({ kind: 'ok', text: 'Kaydedildi.' });
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    } finally { setBusy(false); }
  };

  const upload = async (f: File) => {
    setBusy(true); setMsg(null);
    try {
      const r = await api.uploadRagDocument(f);
      setMsg({ kind: 'ok', text: `${f.name}: ${r.chunks} parça indekslendi.` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    } finally { setBusy(false); if (fileRef.current) fileRef.current.value = ''; }
  };

  return (
    <div style={{ marginTop: 28, paddingTop: 18, borderTop: '1px solid var(--border)' }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>
        Doküman soru-cevap (RAG)
        {cfg.enabled && cfg.endpoint
          ? <span className="badge b-ok" style={{ marginLeft: 8 }}>aktif</span>
          : <span className="badge b-gray" style={{ marginLeft: 8 }}>kapalı</span>}
      </h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 12 }}>
        Runbook / prosedür / mimari dokümanlarını yükle; Copilot chat sorulara bu
        dokümanlardan <b>kaynak atıflı</b> cevap versin. OpenAI-uyumlu bir
        <code> /v1/embeddings</code> endpoint'i gerekir (vLLM/KServe'de bge-m3 önerilir).
      </p>

      <Row>
        <Field2 label="Embedding endpoint" hint="ör. http://bge-m3.ai.svc:8000/v1">
          <input value={cfg.endpoint} onChange={e => setCfg({ ...cfg, endpoint: e.target.value })}
                 placeholder="http://…/v1" style={{ width: '100%' }} />
        </Field2>
        <Field2 label="Model" small hint="ör. BAAI/bge-m3">
          <input value={cfg.model} onChange={e => setCfg({ ...cfg, model: e.target.value })}
                 style={{ width: '100%' }} />
        </Field2>
        <Field2 label="Top-K" small hint="cevaba girecek parça sayısı (1-20)">
          <input type="number" min={1} max={20} value={cfg.topK ?? 5}
                 onChange={e => setCfg({ ...cfg, topK: Number(e.target.value) })}
                 style={{ width: '100%' }} />
        </Field2>
      </Row>
      <Row>
        <Field2 label="API key (opsiyonel)" small
          hint={cfg.hasKey ? 'kayıtlı — boş bırakırsan korunur' : 'endpoint auth istemiyorsa boş bırak'}>
          <input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)}
                 placeholder={cfg.hasKey ? '********' : ''} style={{ width: '100%' }} />
        </Field2>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13, marginTop: 18 }}>
          <input type="checkbox" checked={cfg.enabled}
                 onChange={e => setCfg({ ...cfg, enabled: e.target.checked })} />
          RAG aktif
        </label>
        <Button onClick={() => { void save(); }} disabled={busy} style={{ marginTop: 12 }}>
          {busy ? 'Kaydediliyor…' : 'Kaydet'}
        </Button>
      </Row>

      {/* Wiki / URL kaynakları (v0.8.442) — satır başına bir adres;
          auth gerekiyorsa "url | Header-Adı: değer". Kayıtlı header
          '********' görünür ve değiştirilmezse korunur. */}
      <div style={{ marginTop: 16 }}>
        <h3 style={{ fontSize: 13, fontWeight: 600, margin: '0 0 6px' }}>Wiki / URL kaynakları</h3>
        <p style={{ fontSize: 11.5, color: 'var(--text3)', margin: '0 0 6px' }}>
          Satır başına bir adres (ör. <code>https://wiki.local/display/OPS</code>) —
          aynı host + path altındaki sayfalar taranır (≤200 sayfa, derinlik 3),
          30 dk'da bir otomatik senkron, değişmeyen sayfa yeniden indekslenmez.
        </p>
        <textarea value={sourcesText} onChange={e => setSourcesText(e.target.value)}
          rows={3} placeholder="https://wiki.banka.local/ops" spellCheck={false}
          style={{ width: '100%', fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
        <div style={{ display: 'flex', gap: 8, marginTop: 6, alignItems: 'center' }}>
          <Button variant="secondary" size="sm" type="button" disabled={busy}
            onClick={() => { void save(); }}>
            Kaynakları kaydet
          </Button>
          <Button variant="secondary" size="sm" type="button"
            disabled={busy || !cfg.enabled || !cfg.endpoint}
            title={!cfg.endpoint ? 'Önce embedding endpoint girip kaydet' : 'Tüm kaynakları şimdi tara'}
            onClick={async () => {
              setBusy(true); setMsg(null);
              try {
                const r = await api.syncRagSources();
                setMsg({ kind: 'ok', text: `Senkron: ${r.pages} sayfa · ${r.indexed} indekslendi · ${r.skipped} değişmemiş · ${r.pruned} silindi${r.errors?.length ? ` · ${r.errors.length} hata` : ''}` });
                load();
              } catch (e) {
                setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
              } finally { setBusy(false); }
            }}>
            ⟳ Şimdi senkronize et
          </Button>
        </div>
      </div>

      <div style={{ marginTop: 16, display: 'flex', alignItems: 'center', gap: 10 }}>
        <h3 style={{ fontSize: 13, fontWeight: 600, margin: 0 }}>Dokümanlar</h3>
        <input ref={fileRef} type="file" accept=".md,.txt" style={{ display: 'none' }}
               onChange={e => { const f = e.target.files?.[0]; if (f) void upload(f); }} />
        <Button variant="secondary" size="sm" type="button"
          disabled={busy || !cfg.enabled || !cfg.endpoint}
          title={!cfg.endpoint ? 'Önce embedding endpoint girip kaydet' : 'md / txt yükle (≤5MB)'}
          onClick={() => fileRef.current?.click()}>
          ⬆ Doküman yükle
        </Button>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          Wiki/URL kaynağı bir sonraki sürümde bu panele eklenecek.
        </span>
      </div>

      {docs === undefined && <Spinner />}
      {docs === null && <Empty icon="📄" title="Doküman listesi yüklenemedi" />}
      {docs && docs.length === 0 && (
        <p style={{ fontSize: 12, color: 'var(--text3)', marginTop: 8 }}>Henüz doküman yok.</p>
      )}
      {docs && docs.length > 0 && (
        <div className="table-wrap" style={{ marginTop: 8 }}>
          <table>
            <thead><tr><th>Doküman</th><th>Kaynak</th><th>Parça</th><th>Boyut</th><th>Yükleyen</th><th></th></tr></thead>
            <tbody>
              {docs.map(d => (
                <tr key={d.docId}>
                  <td className="mono" style={{ fontSize: 12 }}>{d.docName}</td>
                  <td><span className="badge b-gray">{d.source}</span></td>
                  <td className="mono" style={{ textAlign: 'right' }}>{d.chunks}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{(d.bytes / 1024).toFixed(1)} KB</td>
                  <td style={{ fontSize: 11, color: 'var(--text2)' }}>{d.uploadedBy || '—'}</td>
                  <td style={{ textAlign: 'right' }}>
                    <Button variant="danger" size="sm" type="button" disabled={busy}
                      onClick={async () => {
                        if (!confirm(`${d.docName} silinsin mi?`)) return;
                        try { await api.deleteRagDocument(d.docId); load(); }
                        catch (e) { setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) }); }
                      }}>
                      Sil
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
    </div>
  );
}
