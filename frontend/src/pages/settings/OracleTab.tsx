// OracleTab — Settings → "Oracle hata tablosu" (v0.10.580, AŞAMA 1;
// audit: docs/audit/oracle-error-log-2026-09-09.md §7).
//
// InfluxTab'ın anatomisi birebir: kaynak kartları listesi + kaynak başına
// "Bağlantıyı dene" (formdaki değerlerle, KAYDETMEDEN) + tek Kaydet.
// Karar mantığı sekmede DEĞİL, oracleForm.ts'te (saf + tablo-testli); bu
// dosya yalnız çizer ve hata haritasını kutuların altına dağıtır.
//
// AŞAMA 1 kapsamı: datasource tanımı, credential ve bağlantı testi. Poller,
// metrik yazımı ve Problem üretimi Aşama 2-3'te — bu yüzden "durum" şeridi
// bilerek mütevazı: uydurma bir "sağlıklı" satırı basmıyoruz, yalnız o pod'da
// koşmuş son bağlantı testinin izini ve şifre referansının çözülüp
// çözülmediğini gösteriyoruz.
//
// ŞİFRE: hiçbir yolda ekrana yazılmaz, hiçbir yolda log'a girmez. GET onu
// maskeliyor (hasPassword rozeti), kutu daima boş açılıyor ve boş kutu
// "dokunmadım" demek — `sourceForSave` `password` anahtarını gövdeye HİÇ
// koymuyor.
//
// TEST UCU SÖZLEŞMESİ: başarısızlık 200 + {ok:false} ile gelir. Bu bir HTTP
// hatası DEĞİL, operatörün sorusuna verilmiş başarılı bir cevaptır — kırmızı
// rozet + gerekçe metni olarak çizilir, "istek başarısız" olarak değil.
import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Badge, Button, Field, SelectField, TextareaField } from '@/components/ui';
import { api } from '@/lib/api';
import { fmtDateTime } from '@/lib/utils';
import { useSettingsLoad, SettingsLoadError, FlashBox } from './shared';
import {
  emptyOracleSource, sourceFromSnapshot, sourceForSave, validateOracleSource,
  hasOracleErrors, parseTypeFilter, typeFilterToText, numFromForm, numToForm,
  ORACLE_DEFAULT_PORT, ORACLE_DEFAULT_TIMESTAMP_COLUMN, ORACLE_DEFAULT_TYPE_COLUMN,
  ORACLE_DEFAULT_MAX_OPEN_CONNS, ORACLE_DEFAULT_QUERY_TIMEOUT_SEC, ORACLE_DEFAULT_INTERVAL_SEC,
  ORACLE_DEFAULT_TIMEZONE, ORACLE_MAPPING_FIELDS, ORACLE_COLUMN_DISABLED,
  type OracleFieldErrors,
} from './oracleForm';
import type {
  OraclePollStatus, OracleSource, OracleSourceSnapshot, OracleSourceStatus, OracleStatusPayload, OracleTestResult,
} from '@/lib/types';

/** Bağlantı biçimi form durumda AYRI tutulur: yalnız `dsn` doluluğundan
 *  türetilseydi, operatör dsn kutusunu boşaltır boşaltmaz form host kipine
 *  atlar ve yazdığı şey gözünün önünde kaybolurdu. */
type ConnMode = 'host' | 'dsn';

interface EditRow {
  src: OracleSource;
  mode: ConnMode;
  /** Sunucudaki karşılığı — id taşıma ve `hasPassword` rozeti için. */
  snapshot?: OracleSourceSnapshot;
}

function rowFromSnapshot(s: OracleSourceSnapshot): EditRow {
  return { src: sourceFromSnapshot(s), mode: s.dsn ? 'dsn' : 'host', snapshot: s };
}

/** ≤3 örnek satır: test cevabı 5 satıra kadar dönebiliyor ama buradaki iş
 *  "tablo gerçekten okunuyor mu"yu göstermek; Aşama 2'nin alan eşlemesi için
 *  kolon LİSTESİ zaten tam. */
const SAMPLE_ROWS = 3;

export function OracleTab() {
  const [rows, setRows] = useState<EditRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [probe, setProbe] = useState<Record<number, OracleTestResult | { pending: true }>>({});
  // Durum: sekme açılışında bir kez + elle yenile. Poll YOK — ayar sekmesi.
  const [status, setStatus] = useState<OracleStatusPayload | null>(null);
  const [statusErr, setStatusErr] = useState<string | null>(null);
  // v0.10.603 — alan eşlemesi 14 kutu; varsayılan katlı (çoğu kurulum §5 adlarını kullanır).
  const [showCols, setShowCols] = useState<Record<number, boolean>>({});

  const loadStatus = () => {
    setStatusErr(null);
    api.oracleStatus().then(setStatus)
      .catch(e => setStatusErr(e instanceof Error ? e.message : 'durum alınamadı'));
  };
  useEffect(() => { loadStatus(); }, []);

  const { loaded, error: loadErr, retry } = useSettingsLoad(
    () => api.oracleSettings(),
    s => setRows((s.sources ?? []).map(rowFromSnapshot)),
  );

  const patch = (i: number, p: Partial<OracleSource>) =>
    setRows(rs => rs.map((r, j) => (j === i ? { ...r, src: { ...r.src, ...p } } : r)));

  /** Satır silme, deneme sonuçlarını da KAYDIRIR. Probe haritası indeksle
   *  anahtarlı: 0. satır silinince 1'in sonucu 0'ın altına düşer ve operatör
   *  BAŞKA bir kaynağın cevabını bu kaynağınki sanardı. */
  const removeRow = (i: number) => {
    setRows(rs => rs.filter((_, j) => j !== i));
    setProbe(p => {
      const next: typeof p = {};
      for (const [k, v] of Object.entries(p)) {
        const idx = Number(k);
        if (idx === i) continue;
        next[idx > i ? idx - 1 : idx] = v;
      }
      return next;
    });
  };

  /** Kip değişimi karşı kipin alanlarını TEMİZLER: sunucu "ya dsn ya
   *  host/serviceName" diyor, ikisi birden dolu kalırsa kaydetme reddedilirdi
   *  ve operatör göremediği bir kutu yüzünden takılırdı. */
  const setMode = (i: number, mode: ConnMode) =>
    setRows(rs => rs.map((r, j) => {
      if (j !== i) return r;
      const src = mode === 'dsn'
        ? { ...r.src, host: '', serviceName: '', port: undefined }
        : { ...r.src, dsn: '', port: r.src.port ?? ORACLE_DEFAULT_PORT };
      return { ...r, mode, src };
    }));

  const errorsFor = (i: number): OracleFieldErrors => validateOracleSource(rows[i].src, {
    others: rows.filter((_, j) => j !== i).map(r => r.src),
    hasStoredPassword: !!rows[i].snapshot?.hasPassword,
  });

  const allErrors = rows.map((_, i) => errorsFor(i));
  const blocked = allErrors.some(hasOracleErrors);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (blocked) {
      setMsg({ kind: 'err', text: 'Kaydedilmedi — kırmızı alanları düzeltin.' });
      return;
    }
    setBusy(true); setMsg(null);
    try {
      const next = await api.putOracleSettings({
        sources: rows.map(r => sourceForSave(r.src, r.snapshot)),
      });
      setRows((next.sources ?? []).map(rowFromSnapshot));
      const on = (next.sources ?? []).filter(s => s.enabled).length;
      setMsg({ kind: 'ok', text: `Kaydedildi — ${on} kaynak etkin.` });
      loadStatus();
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Kaydetme başarısız' });
    } finally {
      setBusy(false);
    }
  };

  // Formdaki değerlerle dener; KAYDETMEZ. Başarısız bir deneme 200 + ok:false
  // ile gelir ve aşağıda cevap olarak çizilir; yalnız ağ/HTTP hatası catch'e
  // düşer ve o da aynı kabın içinde gösterilir (ok:false ile aynı şekil).
  const runTest = async (i: number) => {
    setProbe(p => ({ ...p, [i]: { pending: true } }));
    try {
      const res = await api.testOracleSource(sourceForSave(rows[i].src, rows[i].snapshot));
      setProbe(p => ({ ...p, [i]: res }));
    } catch (err) {
      setProbe(p => ({
        ...p,
        [i]: {
          ok: false, passwordResolved: false, rowCount: 0, columns: [],
          error: err instanceof Error ? err.message : 'Test başarısız',
        },
      }));
    } finally {
      loadStatus();
    }
  };

  if (loadErr) return <SettingsLoadError error={loadErr} onRetry={retry} />;
  if (!loaded) return <Spinner />;

  return (
    <div className="settings-pane">
      <h2 className="oracle-title">Oracle hata tablosu</h2>
      <p className="oracle-note">
        Bir Oracle hata tablosunu (ör. <code>&lt;ŞEMA&gt;.MCA_TERROR_LOG</code>) dış kaynak
        olarak bağlar. Satırlar <b>periyodik olarak okunur</b> (worker lideri, kalıcı
        watermark), Coremetry'de saklanır ve ilgili trace'in Logs sekmesinde <b>oracle</b>
        rozetiyle görünür; Problem üretimi sonraki aşamada. Şema, tablo ve kolon
        adları koda gömülü değildir — hepsi buradan yönetilir. Sorgu daima{' '}
        <b>salt-okunur</b>, zaman aralığıyla sınırlı ve satır tavanlıdır; değerler bind
        edilir. Şifre <b>saklanır ama geri gösterilmez</b>; alternatifi referanstır
        (<code>env:AD</code> ya da <code>file:/yol</code>).
      </p>

      <form onSubmit={save}>
        {rows.length === 0 && (
          <div className="oracle-q">Henüz kaynak yok — aşağıdan ekleyin.</div>
        )}

        {rows.map((r, i) => {
          const src = r.src;
          const err = allErrors[i];
          const pr = probe[i];
          const st: OracleSourceStatus | undefined = src.id
            ? status?.sources.find(x => x.id === src.id)
            : undefined;
          return (
            <div key={i} className={`oracle-src${src.enabled ? '' : ' is-off'}`}>
              {st && (
                <div className="oracle-status">
                  {st.lastCheckAt
                    ? <>Son bağlantı denemesi <b>{fmtDateTime(st.lastCheckAt)}</b>{' · '}
                      {st.lastCheckOK
                        ? <span className="is-ok">başarılı</span>
                        : <span className="is-err">{st.lastError || 'başarısız'}</span>}</>
                    : <span className="is-quiet">Bu sunucuda henüz bağlantı denenmedi.</span>}
                  {src.passwordRef && (
                    <> · şifre referansı{' '}
                      {st.passwordResolved
                        ? <span className="is-ok">çözüldü</span>
                        : <span className="is-err">{st.passwordError || 'çözülemedi'}</span>}</>
                  )}
                </div>
              )}

              {src.id && src.enabled && (() => {
                // v0.10.603 — işçi durumu (v0.10.601 blobu): worker lideri hangi
                // pod'da olursa olsun görünür. Yok = henüz yayın yok; uydurma
                // "sağlıklı" basılmaz.
                const ps: OraclePollStatus | undefined = status?.poll?.sources.find(x => x.sourceId === src.id);
                return (
                  <div className="oracle-status">
                    {ps ? (
                      <>
                        Son okuma <b>{fmtDateTime(ps.lastPollAt)}</b>
                        {' · '}{ps.lastRows} satır okundu, {ps.lastMapped} yazıldı
                        {ps.lastNoTimestamp > 0 && <> · <span className="is-err">{ps.lastNoTimestamp} zamansız satır düştü</span></>}
                        {ps.lastBadTraceId > 0 && <> · {ps.lastBadTraceId} geçersiz trace id</>}
                        {' · '}watermark <b>{fmtDateTime(ps.watermarkNs / 1e6)}</b>
                        {ps.capped && <> · <span className="badge b-warn">tavana çarptı — devam ediyor</span></>}
                        {ps.lastError
                          ? <> · <span className="is-err">{ps.lastError}</span></>
                          : <> · <span className="is-ok">okuma sağlıklı</span></>}
                        {status?.poll?.pod && <> · pod <code>{status.poll.pod}</code></>}
                      </>
                    ) : (
                      <span className="is-quiet">
                        İşçi bu kaynak için henüz durum yayınlamadı — kaynak kaydedildi mi, worker lideri koşuyor mu?
                      </span>
                    )}
                  </div>
                );
              })()}

              <div className="oracle-src__head">
                <Field label="Kaynak adı" value={src.name} required
                  onChange={e => patch(i, { name: e.target.value })}
                  placeholder="core-bank" error={err.name}
                  hint={err.name ? undefined : 'Kaynağı bu adla anacağız; tekil olmalı.'} />
                <label className="oracle-check">
                  <input type="checkbox" checked={src.enabled}
                    onChange={e => patch(i, { enabled: e.target.checked })} />
                  <span>Etkin</span>
                </label>
                {src.id && (
                  <Badge className="mono" title="Sunucu sahipli kimlik — yeniden adlandırma korur">
                    {src.id}
                  </Badge>
                )}
                <Button type="button" variant="ghost" size="sm" onClick={() => removeRow(i)}>
                  Kaldır
                </Button>
              </div>

              <div className="oracle-sub">Bağlantı</div>
              <div className="oracle-row">
                <SelectField label="Bağlantı biçimi" className="is-narrow" value={r.mode}
                  onChange={e => setMode(i, e.target.value as ConnMode)}
                  hint="Tek parça dsn ile host/servis üçlüsü aynı anda verilemez.">
                  <option value="host">Host / servis</option>
                  <option value="dsn">Tek parça dsn</option>
                </SelectField>
                {r.mode === 'dsn' ? (
                  <Field label="DSN" value={src.dsn ?? ''} error={err.dsn}
                    onChange={e => patch(i, { dsn: e.target.value })}
                    placeholder="oracle://kullanıcı:şifre@host:1521/servis"
                    autoComplete="off"
                    hint={err.dsn ? undefined : 'Credential dizenin içindeyse kullanıcı/şifre kutuları boş bırakılabilir.'} />
                ) : (
                  <>
                    <Field label="Host" value={src.host ?? ''} error={err.host}
                      onChange={e => patch(i, { host: e.target.value })}
                      placeholder="oradb.internal" />
                    <Field label="Port" className="is-narrow" inputMode="numeric" error={err.port}
                      value={numToForm(src.port)}
                      onChange={e => patch(i, { port: numFromForm(e.target.value) })}
                      placeholder={String(ORACLE_DEFAULT_PORT)}
                      hint={err.port ? undefined : `boş = ${ORACLE_DEFAULT_PORT}`} />
                    <Field label="Servis adı" value={src.serviceName ?? ''} error={err.serviceName}
                      onChange={e => patch(i, { serviceName: e.target.value })}
                      placeholder="ORCLPDB1" />
                  </>
                )}
              </div>

              <div className="oracle-row">
                <Field label="Kullanıcı" value={src.user} error={err.user}
                  onChange={e => patch(i, { user: e.target.value })}
                  placeholder="coremetry_ro" autoComplete="off"
                  hint={err.user ? undefined : 'Salt-okunur bir hesap yeterli.'} />
                <Field type="password" autoComplete="new-password" error={err.password}
                  label={<>Şifre{r.snapshot?.hasPassword && <span className="is-ok"> · kayıtlı</span>}</>}
                  value={src.password ?? ''}
                  onChange={e => patch(i, { password: e.target.value })}
                  placeholder={r.snapshot?.hasPassword ? '(saklı değeri korumak için boş bırakın)' : 'Oracle şifresi…'}
                  hint={err.password ? undefined : 'Saklanır, geri gösterilmez. Rotasyon: yenisini yazıp Kaydet.'} />
                <Field label="…ya da şifre referansı" value={src.passwordRef ?? ''}
                  onChange={e => patch(i, { passwordRef: e.target.value })}
                  placeholder="env:COREMETRY_ORACLE_PW · file:/var/run/secrets/oracle/pw"
                  error={err.passwordRef ?? r.snapshot?.passwordError}
                  autoComplete="off"
                  hint={err.passwordRef ? undefined
                    : 'Doluysa saklı şifre yerine bu kullanılır ve her kullanımda çözülür.'} />
                {src.passwordRef && r.snapshot?.passwordRef === src.passwordRef && (
                  <span className="oracle-check">
                    {r.snapshot.passwordResolved
                      ? <Badge tone="success">referans çözüldü</Badge>
                      : <Badge tone="danger">referans çözülemedi</Badge>}
                  </span>
                )}
              </div>

              <div className="oracle-sub">Tablo</div>
              <div className="oracle-row">
                <Field label="Şema" value={src.schema} error={err.schema}
                  onChange={e => patch(i, { schema: e.target.value })}
                  placeholder="BANKAPP" />
                <Field label="Tablo" value={src.table} error={err.table}
                  onChange={e => patch(i, { table: e.target.value })}
                  placeholder="MCA_TERROR_LOG" />
                <Field label="Zaman kolonu" value={src.timestampColumn ?? ''} error={err.timestampColumn}
                  onChange={e => patch(i, { timestampColumn: e.target.value })}
                  placeholder={ORACLE_DEFAULT_TIMESTAMP_COLUMN}
                  hint={err.timestampColumn ? undefined : `boş = ${ORACLE_DEFAULT_TIMESTAMP_COLUMN}`} />
                <Field label="Tip kolonu" value={src.typeColumn ?? ''} error={err.typeColumn}
                  onChange={e => patch(i, { typeColumn: e.target.value })}
                  placeholder={ORACLE_DEFAULT_TYPE_COLUMN}
                  hint={err.typeColumn ? undefined : `boş = ${ORACLE_DEFAULT_TYPE_COLUMN}`} />
              </div>
              <div className="oracle-row">
                <Field label="Tip süzgeci" error={err.typeFilter}
                  value={typeFilterToText(src.typeFilter)}
                  onChange={e => patch(i, { typeFilter: parseTypeFilter(e.target.value) })}
                  placeholder="T"
                  hint={err.typeFilter ? undefined
                    : 'Virgülle ayrılmış tip kolonu değerleri (bind edilir); boş = T.'} />
              </div>
              <div className="oracle-q">
                <TextareaField label="Ek koşul (WHERE'e AND ile eklenir)" rows={2}
                  value={src.extraWhere ?? ''} error={err.extraWhere}
                  onChange={e => patch(i, { extraWhere: e.target.value })}
                  placeholder="MCA_ERR_CODE NOT IN ('BSA_020')"
                  hint={err.extraWhere ? undefined
                    : "Serbest ifade; `;` `--` `/*` yasak — bunlar sorgunun zaman yüklemini ve satır tavanını susturur."} />
              </div>

              <div className="oracle-sub">Zaman</div>
              <div className="oracle-row">
                <Field label="Zaman dilimi" value={src.timezone ?? ''} error={err.timezone}
                  onChange={e => patch(i, { timezone: e.target.value })}
                  placeholder={ORACLE_DEFAULT_TIMEZONE}
                  hint={err.timezone ? undefined
                    : `Dilimsiz TIMESTAMP bu dilimde okunur; boş = ${ORACLE_DEFAULT_TIMEZONE}. Yanlış dilim = sabit saat kayması.`} />
                <label className="oracle-check">
                  <input type="checkbox" checked={!!src.timestampHasZone}
                    onChange={e => patch(i, { timestampHasZone: e.target.checked })} />
                  <span>Zaman kolonu dilimli (TIMESTAMP WITH TIME ZONE)</span>
                </label>
              </div>

              <div className="oracle-sub">
                Alan eşlemesi{' '}
                <Button type="button" variant="ghost" size="sm"
                  onClick={() => setShowCols(sc => ({ ...sc, [i]: !sc[i] }))}>
                  {showCols[i] ? 'gizle' : 'göster'}
                </Button>
              </div>
              {showCols[i] && (
                <>
                  <div className="oracle-q">
                    Boş kutu = varsayılan kolon; <code>{ORACLE_COLUMN_DISABLED}</code> = bu alan
                    tabloda yok. Zaman ve tip kolonları yukarıdaki kutulardan. Tüketilmeyen her
                    kolon satırda olduğu gibi attribute olarak kalır.
                  </div>
                  {err.columns && <div className="oracle-q is-err">{err.columns}</div>}
                  <div className="oracle-row">
                    {ORACLE_MAPPING_FIELDS.map(f => (
                      <Field key={f.field} label={f.target} className="is-narrow"
                        value={src.columns?.[f.field] ?? ''}
                        onChange={e => patch(i, { columns: { ...(src.columns ?? {}), [f.field]: e.target.value } })}
                        placeholder={f.column} />
                    ))}
                  </div>
                </>
              )}

              <div className="oracle-sub">Sınırlar</div>
              <div className="oracle-row">
                <Field label="Bağlantı havuzu" className="is-narrow" inputMode="numeric" error={err.maxOpenConns}
                  value={numToForm(src.maxOpenConns)}
                  onChange={e => patch(i, { maxOpenConns: numFromForm(e.target.value) })}
                  placeholder={String(ORACLE_DEFAULT_MAX_OPEN_CONNS)}
                  hint={err.maxOpenConns ? undefined : `1-16; boş = ${ORACLE_DEFAULT_MAX_OPEN_CONNS}`} />
                <Field label="Sorgu zaman aşımı (sn)" className="is-narrow" inputMode="numeric" error={err.queryTimeoutSec}
                  value={numToForm(src.queryTimeoutSec)}
                  onChange={e => patch(i, { queryTimeoutSec: numFromForm(e.target.value) })}
                  placeholder={String(ORACLE_DEFAULT_QUERY_TIMEOUT_SEC)}
                  hint={err.queryTimeoutSec ? undefined : `5-120; boş = ${ORACLE_DEFAULT_QUERY_TIMEOUT_SEC}`} />
                <Field label="Okuma aralığı (sn)" className="is-narrow" inputMode="numeric" error={err.intervalSec}
                  value={numToForm(src.intervalSec)}
                  onChange={e => patch(i, { intervalSec: numFromForm(e.target.value) })}
                  placeholder={String(ORACLE_DEFAULT_INTERVAL_SEC)}
                  hint={err.intervalSec ? undefined : `10-3600; boş = ${ORACLE_DEFAULT_INTERVAL_SEC} (poll aralığı)`} />
              </div>

              <div className="oracle-actions">
                <Button type="button" variant="accent" size="sm"
                  disabled={busy || !!(pr && 'pending' in pr)}
                  onClick={() => runTest(i)}>
                  Bağlantıyı dene
                </Button>
                {pr && 'pending' in pr && <span className="is-quiet">deneniyor…</span>}
              </div>

              {pr && !('pending' in pr) && (
                <div className="oracle-probe">
                  <FlashBox kind={pr.ok ? 'ok' : 'err'}>
                    {pr.ok
                      ? `Bağlantı kuruldu — son 15 dakikada ${pr.rowCount} satır okundu.`
                      : (pr.error || 'Başarısız')}
                    {' · '}şifre {pr.passwordResolved ? 'çözüldü' : 'çözülemedi'}
                    {pr.latencyMs !== undefined && <> · {pr.latencyMs} ms</>}
                  </FlashBox>
                  {(pr.columns?.length ?? 0) > 0 && (
                    <div className="oracle-scroll">
                      <div className="oracle-sub">Kolonlar ({pr.columns!.length})</div>
                      <code>{pr.columns!.join(', ')}</code>
                    </div>
                  )}
                  {(pr.sample?.length ?? 0) > 0 && (
                    <div className="oracle-scroll">
                      <table>
                        <thead>
                          <tr>{(pr.columns ?? []).map(c => <th key={c}>{c}</th>)}</tr>
                        </thead>
                        <tbody>
                          {pr.sample!.slice(0, SAMPLE_ROWS).map((row, ri) => (
                            <tr key={ri}>
                              {(pr.columns ?? []).map(c => (
                                <td key={c} className="mono">{row[c] ?? ''}</td>
                              ))}
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                  {pr.query && <pre className="oracle-sql">{pr.query}</pre>}
                </div>
              )}
            </div>
          );
        })}

        <div className="oracle-actions">
          <Button type="button" variant="secondary" size="sm"
            onClick={() => setRows(rs => [...rs, { src: emptyOracleSource(), mode: 'host' }])}>
            + Kaynak ekle
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={loadStatus}>
            Durumu yenile
          </Button>
          {statusErr && <span className="is-err">{statusErr}</span>}
          <Button type="submit" variant="primary" size="sm" loading={busy} disabled={blocked}>
            Kaydet
          </Button>
          {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
        </div>
      </form>
    </div>
  );
}
