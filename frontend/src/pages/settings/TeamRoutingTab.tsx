import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button, useConfirm } from '@/components/ui';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { teamOptionsCI } from '@/lib/teamOptions';
import type { TeamContacts } from '@/lib/types';
import { Field, FlashBox, humanize } from './shared';

// ── Team routing tab (v0.8.429) ─────────────────────────────────────────────
// Operator ask: "yeni bir problem ilk defa geldiğinde ilgili sy ve ug
// team'e bildirim gönderilsin — mailleri katalogdan alsın." The catalog
// names the teams; this tab maps those names to e-mail addresses and
// arms the automatic problem-open mail (one per problem, notification_log
// dedup, template identical to a hand-configured email channel).
//
// Rows are pre-seeded from the catalog's owner/SRE team sets
// (teamOptionsCI — the same case-insensitive dedup the pickers use) so
// the operator fills blanks instead of retyping team names; teams
// without an address show a warning chip and are skipped silently at
// send time.

export function TeamRoutingTab() {
  const [tc, setTc] = useState<TeamContacts | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [extraTeam, setExtraTeam] = useState('');

  const load = () => {
    setTc(undefined);
    api.getTeamContacts().then(setTc).catch(() => setTc(null));
  };
  useEffect(load, []);

  // Catalog-known team names (owner + SRE, case-insensitive dedup).
  const catalogQ = useServicesMetadata();
  const catalogTeams = useMemo(() => {
    const metas = Object.values(catalogQ.data ?? {});
    return teamOptionsCI([
      ...metas.map(m => m.ownerTeam),
      ...metas.map(m => m.sreTeam),
    ]);
  }, [catalogQ.data]);

  // Render rows = union of catalog teams and already-saved keys (a
  // saved contact for a team that later left the catalog must stay
  // visible/editable, not silently vanish).
  const rows = useMemo(() => {
    if (!tc) return [];
    const seen = new Set<string>();
    const out: string[] = [];
    for (const t of [...catalogTeams, ...Object.keys(tc.contacts)]) {
      const key = t.trim().toLowerCase();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(t);
    }
    return out.sort((a, b) => a.localeCompare(b));
  }, [tc, catalogTeams]);

  if (tc === undefined) return <Spinner />;
  if (tc === null) return <Empty icon="⚠" title="Failed to load team routing settings" />;

  const contactFor = (team: string): string => {
    for (const [k, v] of Object.entries(tc.contacts)) {
      if (k.trim().toLowerCase() === team.trim().toLowerCase()) return v;
    }
    return '';
  };
  const setContact = (team: string, email: string) => {
    const next = { ...tc.contacts };
    // Rewrite under a single canonical key (the displayed one) so a
    // mixed-case duplicate can't linger with a stale value.
    for (const k of Object.keys(next)) {
      if (k.trim().toLowerCase() === team.trim().toLowerCase()) delete next[k];
    }
    if (email.trim() !== '') next[team] = email;
    setTc({ ...tc, contacts: next });
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putTeamContacts(tc);
      setTc(next);
      setMsg({ kind: 'ok', text: 'Saved.' });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const missing = rows.filter(t => contactFor(t).trim() === '').length;

  return (
    <>
    <form onSubmit={save} style={{ maxWidth: 720 }}>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Yeni bir problem <b>ilk kez</b> açıldığında, servisin katalogdaki owner
        (ug) ve SRE (sy) takımlarına otomatik e-posta gönderilir — problem
        başına bir kez, SMTP ayarları üzerinden. Adresler virgülle
        çoğaltılabilir; adresi boş takımlar sessizce atlanır.
      </p>

      {/* v0.9.828 — DÜRÜSTLÜK NOTU.
          Bu kart "Team routing" adını taşıyor ve operatörün takım-bazlı
          YÖNLENDİRMEYİ buradan kuracağını sanması çok doğal. Oysa burası
          yalnız E-POSTA gönderiyor; Slack/Teams/Zoom/webhook'u takıma
          göre yönlendirme yeteneği BAŞKA bir yerde ve ZATEN ÇALIŞIYOR
          (kanal kurallarındaki "Match SRE/owner teams" alanları). Eksik
          olan yetenek değil, o yeteneğin görünürlüğüydü. */}
      <p style={{
        color: 'var(--text2)', fontSize: 12, marginBottom: 16, lineHeight: 1.55,
        padding: '8px 10px', border: '1px solid var(--border)', borderRadius: 4,
      }}>
        <b>Kapsam:</b> bu kart yalnız <b>e-posta</b> gönderir. Slack, Teams, Zoom
        Chat veya webhook&apos;u takıma göre yönlendirmek için
        <b> Bildirim kanalları</b>&apos;nda kanalı düzenleyip <b>Match SRE teams</b> /
        <b> Match owner teams</b> alanlarını doldurun &mdash; o yönlendirme
        katalogdaki takım adlarına göre çalışır ve buradaki adres listesinden
        bağımsızdır.
      </p>

      <div style={{ display: 'flex', gap: 16, alignItems: 'center', marginBottom: 14 }}>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
          <input type="checkbox" checked={tc.enabled}
            onChange={e => setTc({ ...tc, enabled: e.target.checked })} />
          Team routing aktif
        </label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
          Minimum severity:
          <select value={tc.minSeverity ?? 'warning'}
            onChange={e => setTc({ ...tc, minSeverity: e.target.value as TeamContacts['minSeverity'] })}>
            <option value="info">info</option>
            <option value="warning">warning</option>
            <option value="critical">critical</option>
          </select>
        </label>
        {missing > 0 && (
          <span className="badge b-warn" title="Bu takımlar problem açılışında maillenmez — adres girin.">
            {missing} takımın e-postası eksik
          </span>
        )}
      </div>

      {rows.length === 0 ? (
        <Empty icon="👥" title="Katalogda takım yok">
          Service catalog'a owner/SRE team girildiğinde takımlar burada listelenir.
        </Empty>
      ) : (
        <div className="table-wrap" style={{ marginBottom: 14 }}>
          <table>
            <thead>
              <tr><th>Takım</th><th>E-posta adres(ler)i</th></tr>
            </thead>
            <tbody>
              {rows.map(team => {
                const v = contactFor(team);
                return (
                  <tr key={team.toLowerCase()}>
                    <td className="mono" style={{ whiteSpace: 'nowrap' }}>
                      {team}
                      {v.trim() === '' && (
                        <span className="badge b-warn" style={{ marginLeft: 8, fontSize: 9 }}>eksik</span>
                      )}
                    </td>
                    <td>
                      <input value={v}
                        onChange={e => setContact(team, e.target.value)}
                        placeholder="team@example.com, oncall@example.com"
                        aria-label={`${team} e-posta`}
                        style={{ width: '100%', fontSize: 12 }} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Katalog dışı takım ekleme — ör. henüz derive edilmemiş bir takım. */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end', marginBottom: 14 }}>
        <Field label="Katalog dışı takım ekle">
          <input value={extraTeam} onChange={e => setExtraTeam(e.target.value)}
            placeholder="takım adı" style={{ width: 220 }} />
        </Field>
        <Button variant="secondary" size="sm" type="button"
          disabled={!extraTeam.trim()}
          onClick={() => {
            const t = extraTeam.trim();
            // Seed an empty row — the operator types the address next.
            setTc(prev => prev
              ? { ...prev, contacts: { ...prev.contacts, [t]: prev.contacts[t] ?? '' } }
              : prev);
            setExtraTeam('');
          }}>
          Ekle
        </Button>
      </div>

      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
      <Button variant="primary" type="submit" loading={busy}>Save</Button>
    </form>
    {/* v0.9.427 — takım alias tablosu, routing'in doğal komşusu. */}
    <TeamAliasesCard />
    </>
  );
}

// ── Takım eşleştirme (v0.9.427, operatör istegi) ────────────────────────────
// LDAP takım adı ("SY-Dijital Bankacılık") ile telemetri metadata'sındaki
// ad ("dijitalsy", "avengersy") aynı takımın farklı yazımları olabiliyor —
// eşleme operatör tablosudur; my_services/my_problems soruları, inbox ve
// /problems owner/SRE filtreleri bu tablo üzerinden eşleşir. Karşılaştırma
// büyük/küçük harf + Türkçe İ/ı duyarsızdır; buradaki yazım yalnız gösterim.
export function TeamAliasesCard() {
  const confirm = useConfirm();
  const [ta, setTa] = useState<import('@/lib/types').TeamAliases | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [newAlias, setNewAlias] = useState('');
  const [newCanon, setNewCanon] = useState('');

  useEffect(() => {
    api.getTeamAliases().then(setTa).catch(() => setTa(null));
  }, []);

  const save = async (next: Record<string, string>) => {
    setBusy(true); setMsg(null);
    try {
      const saved = await api.putTeamAliases({ aliases: next });
      setTa(saved);
      setMsg({ kind: 'ok', text: 'Kaydedildi.' });
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const add = (e: FormEvent) => {
    e.preventDefault();
    const a = newAlias.trim(), c = newCanon.trim();
    if (!a || !c || !ta) return;
    void save({ ...ta.aliases, [a]: c });
    setNewAlias(''); setNewCanon('');
  };

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="ov-card-h">
        <h3>Takım eşleştirme (alias)</h3>
        <span className="ov-sub">LDAP adı ↔ telemetri adı — "dijitalsy" → "SY-Dijital Bankacılık"</span>
      </div>
      <div className="ov-card-b">
        {ta === undefined && <Spinner />}
        {ta === null && <Empty icon="✗" title="Alias tablosu okunamadı" />}
        {ta && (
          <>
            {Object.keys(ta.aliases).length === 0 && (
              <div style={{ color: 'var(--text3)', fontSize: 12, marginBottom: 10 }}>
                Henüz eşleme yok. Telemetrideki takım adını kanonik (LDAP) ada bağla.
              </div>
            )}
            {Object.entries(ta.aliases).map(([alias, canon]) => (
              <div key={alias} style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 6 }}>
                <span className="mono" style={{ fontSize: 12, minWidth: 180 }}>{alias}</span>
                <span style={{ color: 'var(--text3)' }}>→</span>
                <span className="mono" style={{ fontSize: 12, flex: 1 }}>{canon}</span>
                {/* v0.9.1010 (C4 + O10) — burası ONAYSIZ ve `ghost`tu:
                    tıklama ANINDA persist ediyordu (`void save(next)`),
                    yani tıkla-ve-gitti. Hem onay hem kırmızı dil geldi. */}
                <Button variant="ghost-danger" size="sm" disabled={busy}
                  onClick={async () => {
                    if (!await confirm({
                      title: 'Takım eşlemesi silinsin mi?',
                      body: <><code>{alias}</code> → <code>{canon}</code> eşlemesi
                        kaldırılacak. Telemetride <code>{alias}</code> yazan
                        kayıtlar artık kanonik takıma bağlanmaz ve sahipsiz
                        görünür.</>,
                      confirmLabel: 'Eşlemeyi sil',
                      danger: true,
                    })) return;
                    const next = { ...ta.aliases };
                    delete next[alias];
                    void save(next);
                  }}>Sil</Button>
              </div>
            ))}
            <form onSubmit={add} style={{ display: 'flex', gap: 8, marginTop: 10 }}>
              <input value={newAlias} onChange={e => setNewAlias(e.target.value)}
                placeholder="telemetri adı (örn. avengersy)" disabled={busy}
                style={{ flex: 1, padding: '6px 9px', fontSize: 12, background: 'var(--bg)',
                  color: 'var(--text)', border: '1px solid var(--border)', borderRadius: 6 }} />
              <input value={newCanon} onChange={e => setNewCanon(e.target.value)}
                placeholder="kanonik ad (örn. SY-Krediler ve Sigorta)" disabled={busy}
                style={{ flex: 1, padding: '6px 9px', fontSize: 12, background: 'var(--bg)',
                  color: 'var(--text)', border: '1px solid var(--border)', borderRadius: 6 }} />
              <Button variant="primary" type="submit" disabled={busy || !newAlias.trim() || !newCanon.trim()}>Ekle</Button>
            </form>
            {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
          </>
        )}
      </div>
    </div>
  );
}
