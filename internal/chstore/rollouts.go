package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cilcenk/coremetry/internal/rollout"
)

// rollouts.go — ROLLOUTS CH kapısı (v0.10.199, Faz 2; rollout_schema.go şeması).
//
// Okuma: workload_revision_activity_1m → KARAR kovasına (bucket, çağıranın
// vidası) SQL'de hizalanır (toStartOfInterval), zaman sınırlı + LIMIT +
// max_execution_time (ev kuralı). Yazım: tam-satır INSERT (RMT(version),
// invariant #4: her alan taşınır), PrepareBatch senkron; satır sayısı tik
// başına küçük. Mevcut satırlar FINAL ile, ORDER BY öneki (cluster_id,…)
// süzgeçsiz ama started_at zaman sınırlı.
//
// Not: MV'nin `cluster` kolonu span cluster DEĞERİdir; reconciler bunu
// Remote Cluster id'sine eşler (entity.GroupSeenByCluster deseni) ve
// workload_rollouts.cluster_id olarak yazar.

const rolloutSettingsKey = rollout.SettingsKey

func (s *Store) GetRolloutSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, rolloutSettingsKey)
}

func (s *Store) PutRolloutSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, rolloutSettingsKey, raw)
}

// rolloutActivityLimit — tik başına satır tavanı: 1000 workload × 3 revizyon
// × 2 servis × 72 kova (6 s / 5 dk) ≈ 432k → 500k; aşımı çağıran ilan eder.
const rolloutActivityLimit = 500_000

// RolloutActivity — [since, now] etkinliği, bucket'a hizalı.
// RolloutActivity — MV → karar kovası toplamı; service_name boyutu YOK
// (durum makinesi okumuyor; MV kolonu Faz 5 servis haritası için kalır —
// tik yolunda satır çoğaltmasın). ORDER BY = GROUP BY anahtarı → toplam sıra;
// LIMIT+1 nöbetçisi "tam tavanda" ile "kesildi"yi ayırır. Kesildiyse SON iş
// yükü grubu (yarım) düşer ve adı `cut` ile döner — yarım etkinlik sahte
// "çekildi/tamamlandı" üretmesin; çağıran koşuyu partial işaretler.
func (s *Store) RolloutActivity(ctx context.Context, since time.Time, bucket time.Duration) ([]rollout.ActivityRow, string, error) {
	if bucket < time.Minute {
		bucket = 5 * time.Minute
	}
	sec := int64(bucket / time.Second)
	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT cluster, k8s_namespace, workload, anyLast(workload_kind) AS kind, revision,
		       toStartOfInterval(bucket, INTERVAL %d SECOND) AS b,
		       toInt64(countMerge(span_count_state)) AS spans,
		       minMerge(first_seen_state) AS first_seen, maxMerge(last_seen_state) AS last_seen,
		       anyLast(image) AS image, anyLast(image_tag) AS image_tag
		FROM workload_revision_activity_1m
		WHERE bucket >= toStartOfInterval(toDateTime(?, 'UTC'), INTERVAL %d SECOND) AND bucket <= now()
		GROUP BY cluster, k8s_namespace, workload, revision, b
		ORDER BY cluster, k8s_namespace, workload, revision, b
		LIMIT %d
		SETTINGS max_execution_time = 20`, sec, sec, rolloutActivityLimit+1), chDateTimeArg(since))
	if err != nil {
		return nil, "", fmt.Errorf("rollout activity: %w", err)
	}
	defer rows.Close()
	out := []rollout.ActivityRow{}
	for rows.Next() {
		var r rollout.ActivityRow
		if err := rows.Scan(&r.ClusterValue, &r.Namespace, &r.Workload, &r.Kind, &r.Revision, &r.Bucket, &r.Spans, &r.FirstSeen, &r.LastSeen, &r.Image, &r.ImageTag); err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	cut := ""
	if len(out) > rolloutActivityLimit {
		out = out[:rolloutActivityLimit]
		last := out[len(out)-1]
		cut = last.ClusterValue + "/" + last.Namespace + "/" + last.Workload
		i := len(out)
		for i > 0 && out[i-1].ClusterValue == last.ClusterValue && out[i-1].Namespace == last.Namespace && out[i-1].Workload == last.Workload {
			i--
		}
		out = out[:i]
	}
	return out, cut, rows.Err()
}

const rolloutRowCols = `cluster_id, namespace, workload, workload_kind, revision, started_at, status,
	prev_revision, image, image_tag, prev_image, prev_image_tag,
	first_span_at, traffic_confirmed_at, ksm_started_at, pods_ready_at, ksm_not_ready_since, completed_at,
	detected_by, span_count, note, updated_at`

// zeroIfEpoch / epochIfZero — CH DateTime64 "yok" değeri 1970 (0), Go'da
// time.Time{} (yıl 1). Okumada 1970 → sıfır (isOpen/CompletedAt.IsZero
// çalışsın — inceleme 3. tur BLOCKER), yazımda sıfır → 1970 (yıl 1 DateTime64
// aralığı dışı).
func zeroIfEpoch(t time.Time) time.Time {
	if t.Unix() <= 0 {
		return time.Time{}
	}
	return t
}

func epochIfZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t
}

func scanRollout(rows interface{ Scan(...any) error }) (rollout.Rollout, time.Time, error) {
	var r rollout.Rollout
	var updated time.Time
	// span_count UInt64 (DDL) — clickhouse-go *int64'e taramayı REDDEDER
	// ("converting UInt64 to *int64 is unsupported"); ilk satır yazılana dek
	// hiçbir okuma bu satıra gelmediği için gate'ler yeşildi, canlı smoke
	// yakaladı (v0.10.202). Upsert zaten uint64(r.SpanCount) yazar.
	var spanCount uint64
	err := rows.Scan(&r.ClusterID, &r.Namespace, &r.Workload, &r.Kind, &r.Revision, &r.StartedAt, &r.Status,
		&r.PrevRevision, &r.Image, &r.ImageTag, &r.PrevImage, &r.PrevImageTag,
		&r.FirstSpanAt, &r.TrafficConfirmedAt, &r.KSMStartedAt, &r.PodsReadyAt, &r.KSMNotReadySince, &r.CompletedAt,
		&r.DetectedBy, &spanCount, &r.Note, &updated)
	r.SpanCount = int64(spanCount)
	r.FirstSpanAt, r.TrafficConfirmedAt, r.CompletedAt = zeroIfEpoch(r.FirstSpanAt), zeroIfEpoch(r.TrafficConfirmedAt), zeroIfEpoch(r.CompletedAt)
	r.KSMStartedAt, r.PodsReadyAt, r.KSMNotReadySince = zeroIfEpoch(r.KSMStartedAt), zeroIfEpoch(r.PodsReadyAt), zeroIfEpoch(r.KSMNotReadySince)
	return r, updated, err
}

// rolloutFirstSeenLimit — ufuk tavanı; aşılırsa HATA (kesik ufuk "bilinen
// revizyonu yeni" gösterir ve sahte olay doğurur — sessiz kesme yok).
const rolloutFirstSeenLimit = 500_000

// RolloutFirstSeen — ufukta revizyonun ilk kovası (cluster = span DEĞERİ).
// MV'de state kolonu okunmaz (bucket düz kolon) → ucuz GROUP BY.
func (s *Store) RolloutFirstSeen(ctx context.Context, since time.Time) ([]rollout.FirstSeenRow, error) {
	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT cluster, k8s_namespace, workload, revision, min(bucket) AS first
		FROM workload_revision_activity_1m
		WHERE bucket >= toDateTime(?, 'UTC') AND bucket <= now()
		GROUP BY cluster, k8s_namespace, workload, revision
		LIMIT %d
		SETTINGS max_execution_time = 20`, rolloutFirstSeenLimit+1), chDateTimeArg(since))
	if err != nil {
		return nil, fmt.Errorf("rollout first-seen: %w", err)
	}
	defer rows.Close()
	out := []rollout.FirstSeenRow{}
	for rows.Next() {
		var r rollout.FirstSeenRow
		if err := rows.Scan(&r.ClusterValue, &r.Namespace, &r.Workload, &r.Revision, &r.First); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) > rolloutFirstSeenLimit {
		return nil, fmt.Errorf("rollout first-seen: ufuk tavanı (%d) aşıldı — kesik ufuktan giriş kararı verilmez", rolloutFirstSeenLimit)
	}
	return out, rows.Err()
}

// RolloutRecentRows — reconciler'ın "önceki durum"u: started_at ≥ since (lookback
// + tamamlanmış olayların yeniden girişini yakalamak için 30 gün) — FINAL.
// rolloutPrevPage — önceki durum keyset sayfası; sayfalar kimlik demetiyle
// ilerler. Emniyet tavanı rolloutPrevHardCap: aşılırsa HATA (ölçüldü: 1M
// rollout.Rollout ≈ 650 MB heap; 300k ≈ 200 MB) — kesik geçmişten yazım yok.
const (
	rolloutPrevPage    = 50_000
	rolloutPrevHardCap = 300_000
)

func (s *Store) RolloutRecentRows(ctx context.Context, since time.Time) ([]rollout.Rollout, error) {
	out := []rollout.Rollout{}
	var cur rollout.Rollout // keyset kursörü (boş = baştan)
	for len(out) <= rolloutPrevHardCap {
		rows, err := s.conn.Query(ctx, fmt.Sprintf(`
			SELECT `+rolloutRowCols+`
			FROM workload_rollouts FINAL
			WHERE started_at >= toDateTime64(?, 3, 'UTC')
			  AND (cluster_id, namespace, workload, revision, started_at) > (?, ?, ?, ?, toDateTime64(?, 3, 'UTC'))
			ORDER BY cluster_id, namespace, workload, revision, started_at
			LIMIT %d
			SETTINGS max_execution_time = 15`, rolloutPrevPage),
			chDateTime64Arg(since), cur.ClusterID, cur.Namespace, cur.Workload, cur.Revision, chDateTime64Arg(cur.StartedAt))
		if err != nil {
			return nil, fmt.Errorf("rollout rows: %w", err)
		}
		n := 0
		for rows.Next() {
			r, _, err := scanRollout(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
			cur = r
			n++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if n < rolloutPrevPage {
			break
		}
	}
	if len(out) > rolloutPrevHardCap {
		// Kesik önceki durumdan türetilen yazım güvensiz → hata (koşu failed, yazım yok).
		return nil, fmt.Errorf("rollout rows: önceki durum tavanı (%d) aşıldı — kesik geçmişten yazım yapılmaz", rolloutPrevHardCap)
	}
	return out, nil
}

// RolloutUpsert — tam satır INSERT (RMT(version) — her alan taşınır; version
// DEFAULT now64(9) motorda). updated_at = now64(3) (tail kursörü).
func (s *Store) RolloutUpsert(ctx context.Context, rows []rollout.Rollout) error {
	if len(rows) == 0 {
		return nil
	}
	b, err := s.conn.PrepareBatch(ctx, `INSERT INTO workload_rollouts (`+rolloutRowCols+`)`)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, r := range rows {
		if err := b.Append(r.ClusterID, r.Namespace, r.Workload, r.Kind, r.Revision, r.StartedAt, r.Status,
			r.PrevRevision, r.Image, r.ImageTag, r.PrevImage, r.PrevImageTag,
			epochIfZero(r.FirstSpanAt), epochIfZero(r.TrafficConfirmedAt), epochIfZero(r.KSMStartedAt), epochIfZero(r.PodsReadyAt), epochIfZero(r.KSMNotReadySince), epochIfZero(r.CompletedAt),
			r.DetectedBy, uint64(r.SpanCount), r.Note, now); err != nil {
			return err
		}
	}
	return b.Send()
}

// RolloutRecordRun — rollout_reconcile_runs satırı.
func (s *Store) RolloutRecordRun(ctx context.Context, run rollout.Run) error {
	b, err := s.conn.PrepareBatch(ctx, `INSERT INTO rollout_reconcile_runs
		(started_at, host, finished_at, status, clusters, rollouts_written, span_ms, ksm_ms, error)`)
	if err != nil {
		return err
	}
	fin := run.FinishedAt
	if fin.IsZero() {
		fin = time.Now()
	}
	if err := b.Append(run.StartedAt, run.Host, fin, run.Status, uint16(run.Clusters), uint32(run.RolloutsWritten), uint32(run.SpanMs), uint32(run.KSMMs), run.Error); err != nil {
		return err
	}
	return b.Send()
}

// RolloutLastRun — /api/health + UI için son koşu.
func (s *Store) RolloutLastRun(ctx context.Context) (*rollout.Run, error) {
	rows, err := s.conn.Query(ctx, `SELECT started_at, host, finished_at, status, clusters, rollouts_written, span_ms, ksm_ms, error
		FROM rollout_reconcile_runs FINAL WHERE started_at >= now() - INTERVAL 1 DAY ORDER BY started_at DESC LIMIT 1 SETTINGS max_execution_time = 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var r rollout.Run
	var clusters uint16
	var written, spanMs, ksmMs uint32
	if err := rows.Scan(&r.StartedAt, &r.Host, &r.FinishedAt, &r.Status, &clusters, &written, &spanMs, &ksmMs, &r.Error); err != nil {
		return nil, err
	}
	r.Clusters, r.RolloutsWritten, r.SpanMs, r.KSMMs = int(clusters), int(written), int(spanMs), int(ksmMs)
	return &r, nil
}

// ── Okuma yüzeyi (API) — v0.10.200 ────────────────────────────────────────

// RolloutFilter — liste süzgeci (hepsi opsiyonel; cluster = EffectiveID).
type RolloutFilter struct {
	ClusterID, Namespace, Workload, Status, Kind string
}

// RolloutID — bileşik kimlik (ORDER BY öneki + started_at).
type RolloutID struct {
	ClusterID, Namespace, Workload, Revision string
	StartedAt                                time.Time
}

// RolloutRow — API satırı: Rollout + updated_at (tail kursörü / FE upsert monotonluğu).
type RolloutRow struct {
	rollout.Rollout
	UpdatedAt time.Time `json:"updatedAt"`
}

// MarshalJSON — camelCase alanlar (lib/types.ts aynası); sıfır zamanlar 0.
func (r RolloutRow) MarshalJSON() ([]byte, error) {
	ms := func(t time.Time) int64 {
		if t.IsZero() || t.Unix() <= 0 {
			return 0
		}
		return t.UnixMilli()
	}
	return json.Marshal(map[string]any{
		"clusterId": r.ClusterID, "namespace": r.Namespace, "workload": r.Workload, "kind": r.Kind, "revision": r.Revision,
		"startedAt": ms(r.StartedAt), "status": r.Status, "prevRevision": r.PrevRevision,
		"image": r.Image, "imageTag": r.ImageTag, "prevImage": r.PrevImage, "prevImageTag": r.PrevImageTag,
		"firstSpanAt": ms(r.FirstSpanAt), "trafficConfirmedAt": ms(r.TrafficConfirmedAt), "ksmStartedAt": ms(r.KSMStartedAt),
		"podsReadyAt": ms(r.PodsReadyAt), "ksmNotReadySince": ms(r.KSMNotReadySince), "completedAt": ms(r.CompletedAt),
		"detectedBy": r.DetectedBy, "spanCount": r.SpanCount, "note": r.Note, "updatedAt": ms(r.UpdatedAt),
	})
}

func rolloutWhere(f RolloutFilter, from, to time.Time) (string, []any) {
	where := "started_at >= toDateTime64(?, 3, 'UTC') AND started_at <= toDateTime64(?, 3, 'UTC')"
	args := []any{chDateTime64Arg(from), chDateTime64Arg(to)}
	add := func(col, v string) {
		if v != "" {
			where += " AND " + col + " = ?"
			args = append(args, v)
		}
	}
	add("cluster_id", f.ClusterID)
	add("namespace", f.Namespace)
	add("workload", f.Workload)
	add("status", f.Status)
	add("workload_kind", f.Kind)
	return where, args
}

// clampRolloutLimit — store kendi sınırını koyar (çağırandan bağımsız):
// ≤0 → varsayılan, > max → max.
func clampRolloutLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

// RolloutList — FINAL, started_at DESC + kimlik (toplam sıra), LIMIT (store
// kelepçeler: 1..1000, varsayılan 200).
func (s *Store) RolloutList(ctx context.Context, f RolloutFilter, from, to time.Time, limit int) ([]RolloutRow, error) {
	limit = clampRolloutLimit(limit, 200, 1000)
	where, args := rolloutWhere(f, from, to)
	args = append(args, limit)
	rows, err := s.conn.Query(ctx, `SELECT `+rolloutRowCols+` FROM workload_rollouts FINAL WHERE `+where+`
		ORDER BY started_at DESC, cluster_id, namespace, workload, revision LIMIT ? SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, fmt.Errorf("rollout list: %w", err)
	}
	defer rows.Close()
	out := []RolloutRow{}
	for rows.Next() {
		r, upd, err := scanRollout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, RolloutRow{Rollout: r, UpdatedAt: upd})
	}
	return out, rows.Err()
}

// RolloutByID — tekil (nil = yok).
func (s *Store) RolloutByID(ctx context.Context, id RolloutID) (*RolloutRow, error) {
	rows, err := s.conn.Query(ctx, `SELECT `+rolloutRowCols+` FROM workload_rollouts FINAL
		WHERE cluster_id = ? AND namespace = ? AND workload = ? AND revision = ? AND started_at = toDateTime64(?, 3, 'UTC')
		LIMIT 1 SETTINGS max_execution_time = 5`, id.ClusterID, id.Namespace, id.Workload, id.Revision, chDateTime64Arg(id.StartedAt))
	if err != nil {
		return nil, fmt.Errorf("rollout by id: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	r, upd, err := scanRollout(rows)
	if err != nil {
		return nil, err
	}
	return &RolloutRow{Rollout: r, UpdatedAt: upd}, nil
}

// RolloutCursor — tail kursörü: TAM sıra (updated_at + kimlik). Bir tikin
// upsert'i tüm satırlara AYNI updated_at'i basar; salt updated_at kursörü
// bir batch'i geçemez (LIMIT'i aşan eşit damgalı satırlar ya kaybolur ya
// sonsuza dek yinelenir — inceleme). Keyset: (updated_at, cluster_id,
// namespace, workload, revision, started_at) > kursör.
type RolloutCursor struct {
	UpdatedAt time.Time
	ClusterID string
	Namespace string
	Workload  string
	Revision  string
	StartedAt time.Time
}

// RolloutTail — kursörden sonrası (watermark: updated_at ≤ now − wm; geç
// gelen replika satırı için), FINAL + LIMIT; yeni kursör = son satırın
// anahtarı (kayıt yoksa updated_at = now − wm, kimlik boş).
func (s *Store) RolloutTail(ctx context.Context, cursor RolloutCursor, wm time.Duration, limit int) ([]RolloutRow, RolloutCursor, error) {
	limit = clampRolloutLimit(limit, 500, 5000)
	hi := time.Now().Add(-wm)
	rows, err := s.conn.Query(ctx, `SELECT `+rolloutRowCols+` FROM workload_rollouts FINAL
		WHERE updated_at >= toDateTime64(?, 3, 'UTC')
		  AND (updated_at, cluster_id, namespace, workload, revision, started_at) > (toDateTime64(?, 3, 'UTC'), ?, ?, ?, ?, toDateTime64(?, 3, 'UTC'))
		  AND updated_at <= toDateTime64(?, 3, 'UTC') AND started_at >= now() - INTERVAL 30 DAY
		ORDER BY updated_at, cluster_id, namespace, workload, revision, started_at
		LIMIT ? SETTINGS max_execution_time = 5`,
		chDateTime64Arg(cursor.UpdatedAt), // alt sınır: parça minmax budaması (updated_at ORDER BY'da değil)
		chDateTime64Arg(cursor.UpdatedAt), cursor.ClusterID, cursor.Namespace, cursor.Workload, cursor.Revision, chDateTime64Arg(cursor.StartedAt), chDateTime64Arg(hi), limit)
	if err != nil {
		return nil, cursor, fmt.Errorf("rollout tail: %w", err)
	}
	defer rows.Close()
	out := []RolloutRow{}
	next := cursor
	for rows.Next() {
		r, upd, err := scanRollout(rows)
		if err != nil {
			return nil, cursor, err
		}
		out = append(out, RolloutRow{Rollout: r, UpdatedAt: upd})
		next = RolloutCursor{UpdatedAt: upd, ClusterID: r.ClusterID, Namespace: r.Namespace, Workload: r.Workload, Revision: r.Revision, StartedAt: r.StartedAt}
	}
	if len(out) == 0 && hi.After(next.UpdatedAt) {
		next = RolloutCursor{UpdatedAt: hi}
	}
	return out, next, rows.Err()
}

// RolloutStats — agregat sekmesi (audit §4: DORA zemini).
type RolloutStats struct {
	From            int64              `json:"from"` // epoch-ms (RolloutRow ile aynı)
	To              int64              `json:"to"`
	Total           int                `json:"total"`
	Completed       int                `json:"completed"`
	RolledBack      int                `json:"rolledBack"`
	InProgress      int                `json:"inProgress"`
	Stalled         int                `json:"stalled"`
	Superseded      int                `json:"superseded"` // v0.10.199 terminal durum
	PerDay          float64            `json:"perDay"`
	RollbackRate    float64            `json:"rollbackRate"` // rolled_back / (completed+rolled_back)
	MeanDurationSec float64            `json:"meanDurationSec"`
	P95DurationSec  float64            `json:"p95DurationSec"`
	TopRollback     []RolloutWorkloadN `json:"topRollback"`
	TopDeploy       []RolloutWorkloadN `json:"topDeploy"`
	ByDay           []RolloutDayCount  `json:"byDay"`
}

type RolloutWorkloadN struct {
	ClusterID string `json:"clusterId"`
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	N         int    `json:"n"`
}

type RolloutDayCount struct {
	Day        string `json:"day"`
	Total      int    `json:"total"`
	RolledBack int    `json:"rolledBack"`
}

func (s *Store) RolloutStats(ctx context.Context, clusterID, ns string, from, to time.Time, topN int) (*RolloutStats, error) {
	topN = clampRolloutLimit(topN, 10, 50)
	where, args := rolloutWhere(RolloutFilter{ClusterID: clusterID, Namespace: ns}, from, to)
	st := &RolloutStats{From: from.UnixMilli(), To: to.UnixMilli(), TopRollback: []RolloutWorkloadN{}, TopDeploy: []RolloutWorkloadN{}, ByDay: []RolloutDayCount{}}
	// 1) toplamlar + süre (completed_at − started_at, tamamlananlar)
	var total, completed, rolled, inprog, stalled, superseded uint64
	var meanSec, p95Sec float64
	// Süre yalnız COMPLETED satırlardan (DORA lead time); rolled_back/superseded
	// süreleri "geri alınana kadar geçen süre"dir, karıştırılmaz.
	if err := s.conn.QueryRow(ctx, `SELECT count(), countIf(status='completed'), countIf(status='rolled_back'),
			countIf(status='in_progress'), countIf(status='stalled'), countIf(status='superseded'),
			avgIf(dateDiff('second', started_at, completed_at), status = 'completed' AND completed_at > toDateTime64(0,3)),
			quantileIf(0.95)(dateDiff('second', started_at, completed_at), status = 'completed' AND completed_at > toDateTime64(0,3))
		FROM workload_rollouts FINAL WHERE `+where+` SETTINGS max_execution_time = 10`, args...).
		Scan(&total, &completed, &rolled, &inprog, &stalled, &superseded, &meanSec, &p95Sec); err != nil {
		return nil, fmt.Errorf("rollout stats: %w", err)
	}
	st.Total, st.Completed, st.RolledBack, st.InProgress, st.Stalled, st.Superseded = int(total), int(completed), int(rolled), int(inprog), int(stalled), int(superseded)
	if days := to.Sub(from).Hours() / 24; days > 0 {
		st.PerDay = float64(total) / days
	}
	if completed+rolled > 0 {
		st.RollbackRate = float64(rolled) / float64(completed+rolled)
	}
	if meanSec == meanSec { // NaN korunması
		st.MeanDurationSec = meanSec
	}
	if p95Sec == p95Sec {
		st.P95DurationSec = p95Sec
	}
	// 2) en çok rollback / en çok deploy alan workload'lar
	top := func(extra string, dst *[]RolloutWorkloadN) error {
		rows, err := s.conn.Query(ctx, `SELECT cluster_id, namespace, workload, count() AS n FROM workload_rollouts FINAL
			WHERE `+where+extra+` GROUP BY cluster_id, namespace, workload ORDER BY n DESC LIMIT ? SETTINGS max_execution_time = 10`, append(append([]any{}, args...), topN)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w RolloutWorkloadN
			var n uint64
			if err := rows.Scan(&w.ClusterID, &w.Namespace, &w.Workload, &n); err != nil {
				return err
			}
			w.N = int(n)
			*dst = append(*dst, w)
		}
		return rows.Err()
	}
	if err := top(" AND status = 'rolled_back'", &st.TopRollback); err != nil {
		return nil, fmt.Errorf("rollout stats top rollback: %w", err)
	}
	if err := top("", &st.TopDeploy); err != nil {
		return nil, fmt.Errorf("rollout stats top deploy: %w", err)
	}
	// 3) gün kırılımı
	rows, err := s.conn.Query(ctx, `SELECT toString(toDate(started_at)) AS d, count(), countIf(status='rolled_back')
		FROM workload_rollouts FINAL WHERE `+where+` GROUP BY d ORDER BY d LIMIT 400 SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, fmt.Errorf("rollout stats by day: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d RolloutDayCount
		var n, rb uint64
		if err := rows.Scan(&d.Day, &n, &rb); err != nil {
			return nil, err
		}
		d.Total, d.RolledBack = int(n), int(rb)
		st.ByDay = append(st.ByDay, d)
	}
	return st, rows.Err()
}

// RolloutRuns — son N koşu (store kelepçeler: 1..1000, varsayılan 50).
func (s *Store) RolloutRuns(ctx context.Context, limit int) ([]rollout.Run, error) {
	limit = clampRolloutLimit(limit, 50, 1000)
	rows, err := s.conn.Query(ctx, `SELECT started_at, host, finished_at, status, clusters, rollouts_written, span_ms, ksm_ms, error
		FROM rollout_reconcile_runs FINAL WHERE started_at >= now() - INTERVAL 7 DAY ORDER BY started_at DESC LIMIT ? SETTINGS max_execution_time = 5`, limit)
	if err != nil {
		return nil, fmt.Errorf("rollout runs: %w", err)
	}
	defer rows.Close()
	out := []rollout.Run{}
	for rows.Next() {
		var r rollout.Run
		var clusters uint16
		var written, spanMs, ksmMs uint32
		if err := rows.Scan(&r.StartedAt, &r.Host, &r.FinishedAt, &r.Status, &clusters, &written, &spanMs, &ksmMs, &r.Error); err != nil {
			return nil, err
		}
		r.Clusters, r.RolloutsWritten, r.SpanMs, r.KSMMs = int(clusters), int(written), int(spanMs), int(ksmMs)
		out = append(out, r)
	}
	return out, rows.Err()
}
