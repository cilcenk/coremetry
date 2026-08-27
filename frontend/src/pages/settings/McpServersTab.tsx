import { useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { McpServerInput, McpServerStatus, McpServerTestResult } from '@/lib/types';
import { SettingRow, SettingsLoadError, useSettingsLoad } from './shared';

// McpServersTab — dış MCP sunucu listesi (v0.10.87, MCP istemci dilim ②).
//
// Sohbet döngüsüne dış tool katmanının AYAR yarısı: operatör izin
// listesine sunucu yazar (http ya da stdio), token'ı saklanır ve asla
// geri okunmaz (hasToken + "********" sentineli — DevOps sekmesinin
// sözleşmesi). "Sına" kaydetmeden TEK sunucuyu prova eder; başarısız
// bağlantı {ok:false} ile döner, HTTP hatası değil.
//
// Köprünün kendisi (tool'ların sohbete girmesi) dilim ③ — bu sekme
// kimlikleri ve erişilebilirliği ondan bağımsız oturtur.

const SECRET_KEPT = '********';

interface Row extends McpServerInput {
  hasToken: boolean;
}

const emptyRow = (): Row => ({
  name: '', transport: 'http', url: '', token: '', command: '', args: [],
  enabled: true, hasToken: false,
});

export function McpServersTab() {
  const [rows, setRows] = useState<Row[]>([]);
  const [status, setStatus] = useState<McpServerStatus[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [testing, setTesting] = useState<number | null>(null);
  const [testRes, setTestRes] = useState<Record<number, McpServerTestResult>>({});

  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.getMcpServers(),
    s => {
      setRows((s.servers || []).map(sv => ({
        name: sv.name, transport: sv.transport, url: sv.url || '',
        // Saklı token geri OKUNMAZ; sentinel round-trip'i korur.
        token: sv.hasToken ? SECRET_KEPT : '',
        command: sv.command || '', args: sv.args || [],
        enabled: sv.enabled, hasToken: sv.hasToken,
        allowTools: sv.allowTools, denyTools: sv.denyTools,
        insecureSkipVerify: sv.insecureSkipVerify,
      })));
      setStatus(s.status || []);
    },
  );

  const upd = (i: number, patch: Partial<Row>) =>
    setRows(rs => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  const toInput = (r: Row): McpServerInput => ({
    name: r.name, transport: r.transport, url: r.url || undefined,
    token: r.token || undefined, command: r.command || undefined,
    args: r.args?.length ? r.args : undefined, enabled: r.enabled,
    allowTools: r.allowTools?.length ? r.allowTools : undefined,
    denyTools: r.denyTools?.length ? r.denyTools : undefined,
    insecureSkipVerify: r.insecureSkipVerify || undefined,
  });

  const save = async () => {
    setBusy(true); setMsg(null);
    try {
      const s = await api.putMcpServers(rows.map(toInput));
      setMsg({ kind: 'ok', text: `Kaydedildi — ${s.servers.length} sunucu.` });
      setStatus(s.status || []);
      setRows(rs => rs.map(r => ({ ...r, hasToken: r.token !== '' || r.hasToken })));
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Kaydedilemedi' });
    } finally {
      setBusy(false);
    }
  };

  const test = async (i: number) => {
    setTesting(i);
    try {
      const r = await api.testMcpServer(toInput(rows[i]));
      setTestRes(t => ({ ...t, [i]: r }));
    } catch (err) {
      setTestRes(t => ({ ...t, [i]: {
        ok: false, tools: 0,
        error: err instanceof Error ? err.message : 'sınama başarısız',
      } }));
    } finally {
      setTesting(null);
    }
  };

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
  if (!loaded) return <Spinner />;

  const enabledCount = rows.filter(r => r.enabled).length;

  return (
    <div style={{ maxWidth: 720 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>MCP sunucuları</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Sohbet asistanının çağırabileceği <b>dış</b> MCP sunucuları — izin
        listesi. Tool'lar sohbete <code>{'<sunucu>__<tool>'}</code> adıyla
        girer; yalnız burada tanımlı sunuculara bağlanılır. stdio taşıması
        bir alt süreç başlatır; kısıtlı ortamlarda http kullanın.
      </p>

      <div className={`status-banner status-banner-${enabledCount ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${enabledCount ? 'operational' : 'degraded'}`}>
          {enabledCount ? `${enabledCount} ETKİN` : 'KAPALI'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {enabledCount
            ? 'Etkin sunucuların tool katalogları sohbet döngüsüne eklenecek.'
            : "Dış sunucu yok — sohbet yalnız yerli tool'larla çalışır."}
        </span>
      </div>

      {rows.map((r, i) => {
        const st = status.find(s => s.server === r.name.toLowerCase().replace(/[^a-z0-9-]+/g, '-'));
        const tr = testRes[i];
        return (
          <div key={i} style={{
            marginTop: 14, padding: 14, borderRadius: 8,
            background: 'var(--bg2)', border: '1px solid var(--border)',
          }}>
            <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginBottom: 10 }}>
              <input value={r.name} placeholder="ad (ör. runbook-kb)"
                onChange={e => upd(i, { name: e.target.value })}
                style={{ flex: 1 }} />
              <select value={r.transport}
                onChange={e => upd(i, { transport: e.target.value })}>
                <option value="http">http</option>
                <option value="stdio">stdio</option>
              </select>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13 }}>
                <input type="checkbox" checked={r.enabled}
                  onChange={e => upd(i, { enabled: e.target.checked })} />
                etkin
              </label>
              <Button variant="ghost" size="sm"
                onClick={() => setRows(rs => rs.filter((_, j) => j !== i))}>
                Sil
              </Button>
            </div>

            {r.transport === 'http' ? (
              <>
                <SettingRow label="URL">
                  <input value={r.url} placeholder="https://mcp.internal/endpoint"
                    onChange={e => upd(i, { url: e.target.value })}
                    style={{ width: '100%' }} />
                </SettingRow>
                <SettingRow
                  label="Token"
                  hint={r.hasToken
                    ? <>Kayıtlı bir token var — boş bırakmak ya da {SECRET_KEPT} korur.</>
                    : <>Opsiyonel; Authorization: Bearer olarak gider.</>}>
                  <input type="password" value={r.token}
                    placeholder={r.hasToken ? SECRET_KEPT : ''}
                    onChange={e => upd(i, { token: e.target.value })}
                    style={{ width: '100%' }} />
                </SettingRow>
              </>
            ) : (
              <SettingRow label="Komut"
                hint={<>Alt süreç yolu + argümanlar (boşlukla). Yalnız buraya yazılan komut çalıştırılır.</>}>
                <input value={[r.command, ...(r.args || [])].filter(Boolean).join(' ')}
                  placeholder="/opt/mcp/fs-server --root /data"
                  onChange={e => {
                    const parts = e.target.value.split(/\s+/).filter(Boolean);
                    upd(i, { command: parts[0] || '', args: parts.slice(1) });
                  }}
                  style={{ width: '100%' }} />
              </SettingRow>
            )}

            <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
              <Button variant="secondary" size="sm" loading={testing === i}
                onClick={() => test(i)}>
                Sına
              </Button>
              {tr && (
                <span style={{ fontSize: 12, color: tr.ok ? 'var(--ok)' : 'var(--err)' }}>
                  {tr.ok
                    ? `✓ bağlandı — ${tr.tools} tool${tr.truncated ? ' (katalog kesildi)' : ''}`
                    : `✗ ${tr.error || 'bağlanamadı'}`}
                </span>
              )}
              {!tr && st && (
                <span style={{ fontSize: 12, color: st.err ? 'var(--warn)' : 'var(--text3)' }}>
                  {st.err ? `son durum: ${st.err}` : `katalog: ${st.tools} tool`}
                </span>
              )}
            </div>
          </div>
        );
      })}

      <div style={{ marginTop: 14, display: 'flex', gap: 10, alignItems: 'center' }}>
        <Button variant="secondary" onClick={() => setRows(rs => [...rs, emptyRow()])}
          disabled={rows.length >= 8}>
          + Sunucu ekle
        </Button>
        <Button variant="primary" loading={busy} onClick={save}>
          Kaydet
        </Button>
        {msg && (
          <span style={{ fontSize: 12, color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </span>
        )}
      </div>
    </div>
  );
}
