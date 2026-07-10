import { useEffect, useState } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { APIToken } from '@/lib/types';
import { Field2, FlashBox, Row } from './shared';
import { tsLong } from '@/lib/utils';

// ApiTokensTab — v0.8.444. Harici agent platformlarının (GenAI Studio
// vb.) MCP/REST erişimi için servis token'ları. Düz token create
// yanıtında BİR KEZ gösterilir (SMTP-secret sözleşmesi); listede yalnız
// prefix durur. Revoke tombstone'dur — kayıt audit izi olarak kalır.

export function ApiTokensTab() {
  const [tokens, setTokens] = useState<APIToken[] | null | undefined>(undefined);
  const [name, setName] = useState('');
  const [role, setRole] = useState('viewer');
  const [fresh, setFresh] = useState<string | null>(null); // tek seferlik düz token
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const load = () => {
    api.listAPITokens().then(r => setTokens(r.tokens ?? [])).catch(() => setTokens(null));
  };
  useEffect(load, []);

  const create = async () => {
    setBusy(true); setMsg(null); setFresh(null);
    try {
      const r = await api.createAPIToken(name.trim(), role);
      setFresh(r.token);
      setName('');
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    } finally { setBusy(false); }
  };

  return (
    <div style={{ maxWidth: 720 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>API Tokens</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Harici sistemlerin (GenAI Studio agent'ları, otomasyonlar) Coremetry
        API'sine ve MCP'ye (<code>/api/mcp/sse</code>) kimlikle bağlanması için
        uzun ömürlü token'lar. <code>Authorization: Bearer cmk_…</code> ile
        kullanılır; agent'lar için <b>viewer</b> rolü önerilir (tüm MCP
        tool'ları salt okuma).
      </p>

      <Row>
        <Field2 label="Ad" small hint="ör. genai-studio-sre-agent">
          <input value={name} onChange={e => setName(e.target.value)} style={{ width: '100%' }} />
        </Field2>
        <Field2 label="Rol" small>
          <select value={role} onChange={e => setRole(e.target.value)} style={{ width: '100%' }}>
            <option value="viewer">viewer (önerilen)</option>
            <option value="editor">editor</option>
            <option value="admin">admin</option>
          </select>
        </Field2>
        <Button onClick={() => { void create(); }} disabled={busy || !name.trim()} style={{ marginTop: 18 }}>
          {busy ? 'Üretiliyor…' : '+ Token üret'}
        </Button>
      </Row>

      {fresh && (
        <div style={{
          margin: '12px 0', padding: '10px 12px', borderRadius: 6,
          background: 'color-mix(in srgb, var(--warn) 8%, transparent)',
          border: '1px solid color-mix(in srgb, var(--warn) 35%, transparent)',
          fontSize: 12,
        }}>
          <b>Token'ı ŞİMDİ kopyala — bir daha gösterilmeyecek:</b>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 6 }}>
            <code className="mono" style={{
              padding: '4px 8px', background: 'var(--bg2)', borderRadius: 4,
              overflowWrap: 'anywhere', flex: 1,
            }}>{fresh}</code>
            <Button variant="secondary" size="sm"
              onClick={() => { void navigator.clipboard?.writeText(fresh); }}>
              Kopyala
            </Button>
          </div>
        </div>
      )}

      {tokens === undefined && <Spinner />}
      {tokens === null && <Empty icon="⚠" title="Token listesi yüklenemedi" />}
      {tokens && tokens.length === 0 && (
        <Empty icon="🔑" title="Henüz token yok">
          Yukarıdan bir ad verip üret — GenAI Studio agent'ının MCP bağlantısına yapıştır.
        </Empty>
      )}
      {tokens && tokens.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Ad</th><th>Token</th><th>Rol</th><th>Üreten</th><th>Tarih</th><th>Durum</th><th></th></tr></thead>
            <tbody>
              {tokens.map(t => (
                <tr key={t.id} style={{ opacity: t.revoked ? 0.55 : 1 }}>
                  <td style={{ fontWeight: 600 }}>{t.name}</td>
                  <td className="mono" style={{ fontSize: 11 }}>{t.prefix}</td>
                  <td><span className="badge b-gray">{t.role}</span></td>
                  <td style={{ fontSize: 11, color: 'var(--text2)' }}>{t.createdBy || '—'}</td>
                  <td className="mono" style={{ fontSize: 11 }}>{tsLong(t.createdAt)}</td>
                  <td>{t.revoked
                    ? <span className="badge b-gray">REVOKED</span>
                    : <span className="badge b-ok">ACTIVE</span>}</td>
                  <td style={{ textAlign: 'right' }}>
                    {!t.revoked && (
                      <Button variant="danger" size="sm"
                        onClick={async () => {
                          if (!confirm(`"${t.name}" iptal edilsin mi? Bu token'ı kullanan sistemler ANINDA erişimi kaybeder.`)) return;
                          try { await api.revokeAPIToken(t.id); load(); }
                          catch (e) { setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) }); }
                        }}>
                        İptal et
                      </Button>
                    )}
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
