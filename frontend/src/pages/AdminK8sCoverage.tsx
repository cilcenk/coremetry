import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Spinner, Empty } from '@/components/Spinner';
import { Card } from '@/components/ui/Card';
import {
  COVERAGE_FIELDS, fieldSeen, fieldState, fieldPct, fleetSummary,
  podSeenWindow, podStabilityWarning,
} from '@/pages/k8s/coverageRows';
import type { K8sCoverageRow, PodRow } from '@/lib/types';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// AdminK8sCoverage — K8s bağlam kapsama kartı (v0.10.36, entity Faz 0).
//
// ── NİYE VAR ────────────────────────────────────────────────────────────
//
// Entity katmanının asıl adımı (k8sattributes processor + RBAC) prod'da
// collector restart'ı istiyor ve collector pod bounce'ta wedge oluyor. O
// riski ÖLÇÜLMEMİŞ gerekçeyle almak yanlış sıra: bugün elimizde yalnız
// TEK bir prod span'inin resource seti var, filo geneli bilinmiyor.
//
// Bu sayfa o ölçümü veriyor ve sonraki fazın KABUL TESTİ oluyor —
// değişiklikten önce ve sonra aynı tablo.
//
// ── DÜRÜSTLÜK ───────────────────────────────────────────────────────────
//
// Sayılar ÖRNEKLEM üzerinden. "Ölçülmedi" ile "alan yok" ayrı renkte ve
// ayrı sayılıyor; ikisini karıştırmak, kartın kendi amacını bozar.

const TONE: Record<string, { bg: string; fg: string; text: string }> = {
  full: { bg: 'var(--ok-bg, #0f3)', fg: 'var(--ok)', text: 'var' },
  partial: { bg: 'transparent', fg: 'var(--warn)', text: 'kısmi' },
  none: { bg: 'transparent', fg: 'var(--err)', text: 'yok' },
  unknown: { bg: 'transparent', fg: 'var(--text3)', text: '—' },
};

// v0.10.36 — CLAUDE.md sert kısıtı: her veri tablosu useDataTable
// (sıralanabilir + yeniden boyutlandırılabilir, genişlikler kalıcı).
// Sıralama burada gerçekten işe yarıyor: "hangi servis uid yaymıyor"
// sorusu bir sütun tıkıyla cevaplanıyor.
const COVERAGE_COLS: DataTableColumn<K8sCoverageRow>[] = [
  { id: 'service', label: 'Servis',   sortValue: r => r.service, naturalDir: 'asc', width: 260 },
  { id: 'sampled', label: 'Örneklem', sortValue: r => r.sampled, numeric: true, naturalDir: 'desc', width: 110 },
  ...COVERAGE_FIELDS.map(f => ({
    id: f.key,
    label: f.label,
    // Kapsama ORANINA göre sırala: ham sayı servisler arası
    // karşılaştırılamaz (örneklem boyları farklı).
    sortValue: (r: K8sCoverageRow) => fieldPct(fieldSeen(r, f.key), r.sampled) ?? -1,
    numeric: true,
    naturalDir: 'asc' as const,
    width: 96,
  })),
];

export default function AdminK8sCoveragePage() {
  const q = useQuery({
    queryKey: ['k8s-coverage'],
    queryFn: () => api.k8sCoverage(3600, 200),
    staleTime: 60_000,
  });
  const data = q.data;
  const rows = data?.rows ?? [];
  // ⚠ HOOK'LAR ERKEN DÖNÜŞTEN ÖNCE. İlk yazımda useDataTable'ı
  // `if (q.isPending) return` satırlarının ALTINA koymuştum — React
  // hook kuralı ihlali ve yükleme→veri geçişinde çalışma zamanında
  // patlardı. tsc bunu görmez.
  const dt = useDataTable<K8sCoverageRow>({
    storageKey: 'admin-k8s-coverage',
    columns: COVERAGE_COLS,
    rows,
    // Varsayılan: en az kapsayan üstte — kartın sorusu "kim yaymıyor".
    initialSort: { id: 'podUid', dir: 'asc' },
  });

  // v0.10.41 — pod envanteri (Faz 1 okuma yarısı). Hook'lar erken
  // dönüşten ÖNCE: 10.36'da bu kuralı bir kez ihlal etmiştim.
  const pq = useQuery({
    queryKey: ['k8s-pods'],
    queryFn: () => api.k8sPods(3600, 300),
    staleTime: 60_000,
  });
  const pods = pq.data?.rows ?? [];

  if (q.isPending) return <Spinner />;
  if (q.isError) {
    return (
      <Empty icon="⚠" title="Kapsama okunamadı">
        /api/k8s/coverage isteği hata verdi.
      </Empty>
    );
  }
  const fleet = fleetSummary(rows);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {/* Örneklem uyarısı ÜSTTE ve her zaman: bu sayfanın tüm yargısı
          onun üstünde duruyor. Dipnota atmak, okunmaması demek. */}
      <div style={{ fontSize: 11.5, color: 'var(--text3)' }}>
        Son {Math.round((data?.windowSec ?? 3600) / 60)} dakika ·{' '}
        <b>{data?.sampleRows?.toLocaleString()}</b> satırlık ÖRNEKLEM üzerinden.
        Seyrek yayan bir servis örnekleme düşebilir; “—” ölçülmedi demek,
        “yok” demek DEĞİL.
      </div>

      {/* v0.10.62 — DIŞ TAVAN ISIRDIYSA FİLO EKSİK.
          Kartın "ölçülmedi ≠ yok" sözleşmesi satır düzeyinde ifade
          EDİLEMİYOR: GROUP BY sıfır satırlı grup için hiç satır üretmez,
          yani örnekleme girmeyen servis "unknown" olarak değil, HİÇ
          görünmüyor. Ölçülebilir olan tek işaret bu: tavan doldu mu. */}
      {data?.capped && (
        <div className="badge b-warn" style={{ fontSize: 11, alignSelf: 'flex-start' }}>
          ⚠ Örneklem tavanı doldu — bazı servisler bu tabloya HİÇ girmemiş
          olabilir. Aşağıdaki filo sayıları eksik bir küme üzerinden.
        </div>
      )}

      {/* Filo resmi ÖNCE: operatörün sorusu "filonun ne kadarı bu alanı
          yayıyor". Servis tablosu ikincil, 200 satırda kaybolur. */}
      <Card header="Filo kapsaması">
        <div className="table-wrap is-fit">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <thead>
              <tr>
                <th>alan</th>
                <th style={{ textAlign: 'right' }}>tam</th>
                <th style={{ textAlign: 'right' }}>kısmi</th>
                <th style={{ textAlign: 'right' }}>yok</th>
              </tr>
            </thead>
            <tbody>
              {fleet.map(f => (
                <tr key={f.field}>
                  <td className="mono">k8s.{f.label}</td>
                  <td style={{ textAlign: 'right', color: f.full > 0 ? 'var(--ok)' : 'var(--text3)' }}>{f.full}</td>
                  <td style={{ textAlign: 'right', color: f.partial > 0 ? 'var(--warn)' : 'var(--text3)' }}>{f.partial}</td>
                  <td style={{ textAlign: 'right', color: f.none > 0 ? 'var(--err)' : 'var(--text3)' }}>{f.none}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      <Card header={`Servis bazında (${rows.length})`}>
        {rows.length === 0 ? (
          <Empty icon="∅" title="Pencerede span yok">
            Örneklemde hiç span görülmedi — pencereyi genişletin.
          </Empty>
        ) : (
          <div className="table-wrap is-fit">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(r => (
                  <tr key={r.service} style={{ contentVisibility: 'auto' }}>
                    <td className="mono">{r.service}</td>
                    <td style={{ textAlign: 'right', color: 'var(--text3)' }}>
                      {r.sampled.toLocaleString()}
                    </td>
                    {COVERAGE_FIELDS.map(f => {
                      const seen = fieldSeen(r, f.key);
                      const st = fieldState(seen, r.sampled);
                      const pct = fieldPct(seen, r.sampled);
                      return (
                        <td key={f.key} style={{ color: TONE[st].fg, fontSize: 11 }}
                            title={pct === null ? 'ölçülmedi' : `${seen}/${r.sampled} (%${pct})`}>
                          {st === 'full' ? '✓' : st === 'none' ? '✗' : st === 'unknown' ? '—' : `%${pct}`}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* v0.10.41 — POD ENVANTERİ. Kimlik (namespace, pod adı); uid
          prod'da gelmiyor ve beklenmiyor (operatör kararı). */}
      <Card header={`Pod envanteri (${pods.length})`}>
        {pq.isPending ? <Spinner /> : pods.length === 0 ? (
          <Empty icon="∅" title="Örneklemde pod görülmedi">
            Span'ler k8s.pod.name taşımıyor olabilir — üstteki kapsama
            tablosu hangi servisin yaydığını söylüyor.
          </Empty>
        ) : (
          <div className="table-wrap is-fit">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <thead>
                <tr>
                  <th style={{ width: 140 }}>namespace</th>
                  <th>pod</th>
                  <th style={{ width: 170 }}>servis</th>
                  <th style={{ width: 150 }}>node</th>
                  <th style={{ width: 90, textAlign: 'right' }}>span</th>
                  <th style={{ width: 170 }}>görülme</th>
                </tr>
              </thead>
              <tbody>
                {pods.map((r: PodRow) => {
                  const warn = podStabilityWarning(r);
                  return (
                    <tr key={`${r.namespace}/${r.pod}`} style={{ contentVisibility: 'auto' }}>
                      <td className="mono" style={{ color: 'var(--text3)' }}>{r.namespace || '—'}</td>
                      <td className="mono">
                        {r.pod}
                        {/* Uyarı satırın İÇİNDE: dipnota atmak, birleşmiş
                            ömrün sessiz kalması demek olurdu. */}
                        {warn && (
                          <span className="badge b-warn" title={warn}
                                style={{ fontSize: 9, marginLeft: 6 }}>
                            ömür belirsiz
                          </span>
                        )}
                      </td>
                      <td className="mono" style={{ fontSize: 11 }}>{r.service}</td>
                      <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                        {r.node || '—'}
                      </td>
                      <td style={{ textAlign: 'right', color: 'var(--text3)' }}>
                        {r.spans.toLocaleString()}
                      </td>
                      <td style={{ fontSize: 11, color: 'var(--text3)' }}>{podSeenWindow(r)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
