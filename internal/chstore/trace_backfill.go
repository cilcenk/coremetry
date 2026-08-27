package chstore

import (
	"context"
	"log"
	"fmt"
	"strings"
	"time"
)

// trace_backfill.go — /traces tarihçe geri doldurma sihirbazının STORE
// yarısı (v0.10.103, operatör isteği: "Sihirbaz ile yapalım").
//
// ── NEDEN ───────────────────────────────────────────────────────────────
//
// v0.10.97'nin trace_summary_5m migration'ı (drop+recreate) geçmiş 5-dk
// bucket'ları düşürüyor: span'ler duruyor ama /traces LİSTESİ ve
// Aggregated sekmesi eski pencerelerde boşalıyor. Ham spans 30 gün
// tutulduğu için tarihçe yeniden kurulabilir — bu dosya o yeniden
// kurmayı GÜN GÜN yapar.
//
// ── İDEMPOTENS: DROP PARTITION + INSERT ─────────────────────────────────
//
// trace_summary_5m AggregatingMergeTree'dir: aynı (bucket, trace) için
// İKİNCİ kez state satırı basmak, merge'de sayıları ŞİŞİRİR (countMerge
// çift sayar) — ReplacingMergeTree'nin "son yazan kazanır" güvencesi
// burada YOK. Bu yüzden her gün için önce o günün MV PARTITION'ı düşer
// (PARTITION BY toDate(time_bucket) — yalnız MV parçası; spans'e
// DOKUNULMAZ), sonra gün ham spans'ten yeniden aggregate edilir.
// Yarıda kesilen bir koşu güvenle tekrarlanır: gün ya boş ya bütündür.
//
// ── SQL TEK YAZIM ───────────────────────────────────────────────────────
//
// Backfill SELECT'i, MV DDL'inin state listesinin AYNASIDIR. İkisi
// ayrışırsa geri doldurulan günler farklı şekle sahip olur ve bunu
// hiçbir tip denetimi görmez — TestTraceBackfillMirrorsMVStates iki
// metni alan alan karşılaştırır.

// TraceBackfillDay — preflight'ın gün satırı.
type TraceBackfillDay struct {
	Day string `json:"day"` // "2026-08-26"
	// SpanTraces — ham spans'teki yaklaşık trace sayısı (uniq).
	SpanTraces uint64 `json:"spanTraces"`
	// MVTraces — MV'de o gün görünen trace sayısı.
	MVTraces uint64 `json:"mvTraces"`
	// Gap — MV'nin ham veriye oranla boş/zayıf olduğu hüküm satırı.
	// Eşik %50: upgrade sonrası tam boş günler 0'dır; kısmi günler
	// (upgrade günü) de yakalansın. Sağlıklı günlerde iki sayı ~eşittir.
	Gap bool `json:"gap"`
}

// TraceBackfillPreflight — spans'ın tuttuğu her gün için MV/ham kıyası.
// SAF ölçüm; hiçbir şey yazmaz.
//
// v0.10.103 tasarımı: sayım system.parts'tan (partition başına aktif
// satır) — veri TARANMAZ, prod hacminde de anlıktır ve ana bağlantıda
// koşar (telemetri okuma havuzu telemetri SELECT'lerine ayrılmış,
// conn_strategy kapısı). Cluster'da clusterAllReplicas fan-out'u
// (distribution_queue emsali); satır sayısı trace sayısı değildir ama
// hüküm için yeter: boşluk = spans dolu ∧ MV boş/çok zayıf.
func (s *Store) TraceBackfillPreflight(ctx context.Context, days int) ([]TraceBackfillDay, error) {
	if days <= 0 || days > 30 {
		days = 30
	}
	// v0.10.106 (lokal smoke'un yakaladığı ürün hatası): TO'suz bir
	// MaterializedView'ın system.parts'ta KENDİ satırı YOKTUR — veri
	// gizli `.inner_id.<uuid>` tablosunda durur. MV adını saymak
	// mv_rows'u ebediyen 0 gösterir ve sihirbaz DOLU günleri de
	// "boşluk" diye yıkıcı yeniden-doluma önerir (prod'da boşa iş).
	// İç adlar uuid'den çözülür (mvInnerTable/innerName ailesinin
	// deseni); cluster'da her replica'nın uuid'i farklı olabilir,
	// fan-out'la HEPSİ toplanır.
	inner, err := s.traceMVInnerNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("mv iç tablo çözümü: %w", err)
	}
	src, spansT := "system.parts", "spans"
	if s.clusterMode() {
		src = fmt.Sprintf("clusterAllReplicas('%s', system.parts)", s.ClusterName())
		spansT = "spans_local"
	}
	mvNames := make([]string, 0, len(inner)+1)
	mvNames = append(mvNames, inner...)
	if len(mvNames) == 0 {
		// Gelecekte TO-tablolu bir refactor iç ad üretmezse dürüst
		// düşüş: MV adının kendisi (bugünkü şekilde daima 0 satır ama
		// sorgu kırılmaz; boşluk hükmü "bilinmiyor"a değil "boş"a
		// düşer ve log bunu söyler).
		mvNames = append(mvNames, "trace_summary_5m")
		log.Printf("[trace-backfill] MV iç tablosu çözülemedi — mv sayımı 0 görünecek")
	}
	ph := strings.Repeat("?,", len(mvNames))
	ph = ph[:len(ph)-1]
	args := []any{spansT}
	for _, n := range mvNames {
		args = append(args, n)
	}
	args = append(args, spansT)
	for _, n := range mvNames {
		args = append(args, n)
	}
	args = append(args, time.Now().AddDate(0, 0, -days).Format("2006-01-02"))
	rows, err := s.conn.Query(ctx, `
		SELECT partition AS day,
		       sumIf(rows, table = ?)      AS span_rows,
		       sumIf(rows, table IN (`+ph+`)) AS mv_rows
		FROM `+src+`
		WHERE database = currentDatabase()
		  AND (table = ? OR table IN (`+ph+`))
		  AND active AND partition >= ?
		GROUP BY day ORDER BY day DESC
		SETTINGS max_execution_time = 25`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceBackfillDay
	for rows.Next() {
		var day string
		var spanRows, mvRows uint64
		if err := rows.Scan(&day, &spanRows, &mvRows); err != nil {
			return nil, err
		}
		out = append(out, TraceBackfillDay{
			Day: day, SpanTraces: spanRows, MVTraces: mvRows,
			Gap: spanRows > 0 && mvRows*50 < spanRows,
		})
	}
	return out, rows.Err()
}

// traceMVInnerNames — trace_summary_5m'in gizli depolama tablo adları
// (`.inner_id.<uuid>`). Cluster'da clusterAllReplicas fan-out'u: her
// replica'nın kendi uuid'i olabilir, parts eşlemesi hepsini ister.
func (s *Store) traceMVInnerNames(ctx context.Context) ([]string, error) {
	src, name := "system.tables", "trace_summary_5m"
	if s.clusterMode() {
		src = fmt.Sprintf("clusterAllReplicas('%s', system.tables)", s.ClusterName())
		name = "trace_summary_5m_local"
	}
	rows, err := s.conn.Query(ctx, `
		SELECT DISTINCT concat('.inner_id.', toString(uuid))
		FROM `+src+`
		WHERE database = currentDatabase() AND name = ?
		  AND engine = 'MaterializedView'
		  AND toString(uuid) != '00000000-0000-0000-0000-000000000000'
		SETTINGS max_execution_time = 10`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// traceBackfillStateSQL — MV DDL'indeki state listesinin aynası.
// ⚠ DDL değişirse BURASI da değişir; TestTraceBackfillMirrorsMVStates
// ayrışmayı çiviler.
const traceBackfillStateSQL = `
	argMaxIfState(service_name, time,
	  (parent_id = '' OR parent_id = '0000000000000000') AND name != ''),
	argMaxIfState(name, time,
	  (parent_id = '' OR parent_id = '0000000000000000') AND name != ''),
	minState(time),
	maxState(toUnixTimestamp64Nano(time) + duration),
	countState(),
	countIfState(status_code = 'error'),
	argMaxIfState(http_route, time,
	  (parent_id = '' OR parent_id = '0000000000000000') AND name != ''),
	argMinIfState(service_name, time,
	  (kind = 'server' OR kind = 'consumer')
	  AND service_name != '' AND service_name != 'unknown')`

// TraceBackfillDayRun — TEK günü geri doldurur.
//
// v0.10.103 taşıma gerçeği: sürücü ReadTimeout'u 30s; tam-gün INSERT
// SELECT 25s tavanını aşabilir (canlı provada aştı, code 159). Daha
// kritik gerçek: zaman aşımına uğrayan INSERT SELECT o ana dek yazdığı
// blokları GERİDE BIRAKIR (canlı provada doğrulandı — 159 sonrası MV'de
// kısmi satırlar vardı). AggregatingMergeTree'de o kısmın üstüne yeniden
// yazmak sayıları şişirir. Bu yüzden yarılama DEĞİL, MERDİVEN:
//
//	dilim boyu ∈ {24h, 6h, 1h, 15m, 5m}
//	her denemede: günün partition'ı DÜŞER → gün o boyda dilim dilim
//	kurulur; herhangi bir dilim zaman aşarsa TÜM GÜN bir alt boyla
//	baştan (yeniden düşerek) denenir.
//
// Böylece kısmi yazım hiçbir zaman üstüne-eklenmez: gün ya boş ya
// bütündür; tekrar koşmak da güvenlidir. 5 dk (MV'nin canlı bucket
// maliyeti) da yetmezse hata dürüstçe yükselir.
func (s *Store) TraceBackfillDayRun(ctx context.Context, day string) error {
	dayT, err := time.Parse("2006-01-02", day)
	if err != nil {
		return fmt.Errorf("geçersiz gün %q: %w", day, err)
	}
	var lastErr error
	for _, slice := range backfillSliceLadder {
		if err := s.dropTraceDayPartition(ctx, day); err != nil {
			return fmt.Errorf("gün %s: partition düşürme: %w", day, err)
		}
		lastErr = s.backfillDaySlices(ctx, dayT, slice)
		if lastErr == nil {
			return nil
		}
		if !isBackfillTimeout(lastErr) {
			return fmt.Errorf("gün %s: %w", day, lastErr)
		}
		// zaman aşımı → bir alt dilim boyuyla, temiz zeminde yeniden
	}
	return fmt.Errorf("gün %s: en küçük dilim (5 dk) de zaman aştı: %w", day, lastErr)
}

// backfillSliceLadder — deneme sırasıyla dilim boyları.
var backfillSliceLadder = []time.Duration{
	24 * time.Hour, 6 * time.Hour, time.Hour, 15 * time.Minute, 5 * time.Minute,
}

func (s *Store) dropTraceDayPartition(ctx context.Context, day string) error {
	target := "trace_summary_5m"
	if s.clusterMode() {
		target = "trace_summary_5m_local"
	}
	return s.conn.Exec(ctx,
		"ALTER TABLE "+target+s.onCluster()+" DROP PARTITION '"+day+"'")
}

// backfillDaySlices — günü sabit boy dilimlerle kurar; ilk hatada durur.
func (s *Store) backfillDaySlices(ctx context.Context, day time.Time, slice time.Duration) error {
	end := day.AddDate(0, 0, 1)
	for from := day; from.Before(end); from = from.Add(slice) {
		to := from.Add(slice)
		if to.After(end) {
			to = end
		}
		if err := s.conn.Exec(ctx, `
			INSERT INTO trace_summary_5m
			  (trace_id, time_bucket, root_service_state, root_name_state,
			   trace_start_state, trace_end_state, span_count_state,
			   error_count_state, entry_route_state, entry_service_state)
			SELECT trace_id, toStartOfInterval(time, INTERVAL 5 MINUTE),`+
			traceBackfillStateSQL+`
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
			GROUP BY trace_id, toStartOfInterval(time, INTERVAL 5 MINUTE)
			SETTINGS max_execution_time = 25,
			         max_bytes_before_external_group_by = 2000000000,
			         distributed_product_mode = 'global'`,
			from.UTC().Format("2006-01-02 15:04:05"), to.UTC().Format("2006-01-02 15:04:05")); err != nil {
			// Etiket dilim BOYUNU da söyler — 24h diliminde "00:00→00:00"
			// (ertesi gün sarması) tek başına anlaşılmıyordu (prod ekranı).
			return fmt.Errorf("dilim %s→%s (%s boy): %w",
				from.UTC().Format("01-02 15:04"), to.UTC().Format("01-02 15:04"),
				slice, err)
		}
	}
	return nil
}

// isBackfillTimeout — merdiveni İNDİREN hata sınıfı: KAYNAK hataları.
//
// v0.10.104 (prod ilk koşusu, operatör ekranı): 21 Ağustos günü code
// 241 (Query memory limit exceeded, 3.74 GiB / 3.73 GiB) ile durdu.
// İlk tasarım yalnız zaman aşımını (159) indiriyordu ve "bellek
// yarılanmaz" diyordu — BU İŞ YÜKÜ İÇİN YANLIŞ: dilim zamanla küçülünce
// GROUP BY'ın grup sayısı ve remote stream tamponları da küçülür, yani
// bellek de zamana bağlı bir kaynak. 241 artık 159'la aynı basamağı
// iniyor. Sözdizimi/bilinmeyen-kolon gibi yapısal hatalar İNMEZ — aynı
// hatayı 288 dilimde tekrarlamak teşhisi boğar.
func isBackfillTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "code: 159") ||
		strings.Contains(msg, "code: 241") ||
		strings.Contains(msg, "Timeout exceeded") ||
		strings.Contains(msg, "memory limit exceeded") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "i/o timeout")
}

// TraceBackfillStateFragments — test aynası için ÜST-DÜZEY parça listesi
// (SAF). Düz virgül bölmesi fonksiyon argümanlarının içindeki virgülleri
// de bölerdi; parantez derinliği sıfırken bölünür.
func TraceBackfillStateFragments() []string {
	var out []string
	depth, start := 0, 0
	s := traceBackfillStateSQL
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				if p := strings.TrimSpace(s[start:i]); p != "" {
					out = append(out, p)
				}
				start = i + 1
			}
		}
	}
	if p := strings.TrimSpace(s[start:]); p != "" {
		out = append(out, p)
	}
	return out
}
