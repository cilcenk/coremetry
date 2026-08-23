// v0.9.1304 — Kural P1: ReplacingMergeTree'de DEĞİŞKEN partition kolonu.
//
// Orijinal semptom (CH şema taraması bulgusu): root_cause_hypotheses
// `PARTITION BY toYYYYMM(computed_at)` + `ORDER BY (anchor_kind, anchor_id)`
// taşıyordu. computed_at ORDER BY'da değil ve synthesizer onu her 30 sn'lik
// tikte `now` ile yeniden yazıyor → ay sınırını geçen her açık anchor ikinci
// bir satır kazanıyor, ve ReplacingMergeTree'nin arka plan birleştirmesi
// partition sınırını aşmadığı için o kopya TTL'e kadar ÖLÜMSÜZ kalıyor.
// Doğruluğu ayakta tutan tek şey `SELECT … FINAL`'in sorgu anında
// partition'lar arası birleştirmesiydi — yani bir sunucu ayarı
// (do_not_merge_across_partitions_select_final) tek fren.
//
// Bu dosya İKİ kapı kuruyor:
//
//  1. root_cause_hypotheses DDL'inin artık PARTITION BY taşımadığını pinler
//     (dar regresyon).
//  2. KURALIN KENDİSİNİ genelleştirir: `tables` dilimindeki HER
//     ReplacingMergeTree için, PARTITION BY kolonları ORDER BY'da olmalı —
//     ya da tablo aşağıdaki sicile GEREKÇESİYLE yazılmalı. Böylece yeni bir
//     tablo bu sınıfa sessizce katılamaz.
package chstore

import (
	"regexp"
	"strings"
	"testing"
)

// ── Genelleştirilmiş P1 taraması ────────────────────────────────────────

// partitionNotInOrderBy — PARTITION BY kolonu ORDER BY'da OLMAYAN, BİLİNEN
// ReplacingMergeTree'ler ve neden kabul edildikleri.
//
// Kural P1 iki koşullu: (a) partition kolonu ORDER BY'da değil VE (b) kolon
// yeniden yazımda DEĞİŞİYOR. Tarama yalnız (a)'yı mekanik olarak görebilir;
// (b) yazma yolunu okumayı gerektirir. Bu yüzden sicil bir MUAFİYET listesi
// değil, bir KARAR KAYDI: buraya bir tablo eklemek, "yazıcının bu kolonu
// yeniden yazmadığını kontrol ettim" demektir.
//
// 2026-08-23'te chc-0 üzerinde ÖLÇÜLDÜ (aynı id'nin kaç farklı partition'a
// düştüğü). Sonuçlar gerekçelerde.
//
// ── v0.9.1306 TEŞHİSİ: anomaly_events + problems kayması ─────────────────
// v0.9.1304'ün açık bıraktığı iki bulgu kovalandı. Sonuç: YAZICI SUÇSUZ.
// Hiçbir yazma yolu started_at'i taşımayı unutmuyor; kök neden TOPOLOJİ.
//
// Mekanizma (ölçülmüş, lokal 2-shard küme):
//
//	problems / anomaly_events — ve 34 state tablosu daha — shard-YEREL
//	Replicated*MergeTree; telemetri tablolarının aksine Distributed
//	sarmalayıcıları YOK. Uygulama ise COREMETRY_CH_ADDR'deki İKİ host'a
//	birden bağlanıyor. Ana havuz bilinçli ConnOpenInOrder (v0.9.486), ama
//	in-order yalnız SAĞLIKLI ilk host'a pinler: chc-0 kısa süre düşünce
//	bağlantılar chc-1'e (shard 02) kayıyor ve ConnMaxLifetime=1h onları
//	orada bir saate kadar tutuyor.
//
//	O pencerede UpsertAnomalyEvents'in taşıma SELECT'i (anomaly_event.go
//	:153) chc-1'e düşüyor. Sorgu HATA VERMİYOR — shard'da satır olmadığı
//	için dürüstçe 0 satır dönüyor. mergeAnomalyCarry exists=false görüyor
//	ve started_at'i TAZELİYOR; sonraki INSERT chc-0'a düşünce taze değer
//	eski satırın YANINA, BAŞKA bir partition'a yazılıyor.
//
// Kanıt: 23 sıfırlama anının 23'ü de chc-1'e yazım penceresiyle çakışıyor
// (medyan <1 dk). Bir tikin tüm batch'i tek nanosaniyeyi paylaşıyor (bir
// anda 10 id'ye kadar) — tek tek değil, toplu sıfırlanıyorlar.
//
// problems tarafı %100 TÜREV: kayan 21 id'nin 21'i `anomaly-auto:`, ve o
// id'lerin 50 satırının 50'sinin started_at'i nanosaniyesine kadar bir
// anomaly_events değeri. evaluator.go:1323 açık problem bulamayınca
// startedAt = ev.StartedAt alıyor; FindOpenProblem de aynı yanlış shard'a
// düştüğü için hem "yeniden aç" hem de "sıfırlanmış başlangıcı miras al"
// aynı anda oluyor. problems'ın anomaly DIŞI 4476 id'sinde kayma SIFIR.
//
// Bu kozmetik DEĞİL: started_at hem P1 açık-saat eşiğini hem
// effectiveSeverity'nin yaş tabanlı yükseltmesini besliyor (evaluator.go
// :1330). Sıfırlanan başlangıç, yaşlanmış bir problemi sessizce GERİ
// indiriyor.
//
// v0.9.1304'ün çaresi (PARTITION BY'ı SÖKMEK) buraya TAŞINMIYOR: o çare
// DROP+CREATE ile geliyordu, bu iki tablo ise geri getirilemez operatör
// durumu taşıyor (ack, assignee, AI özeti, 30 günlük anomali geçmişi).
// Retention BLOKÖR DEĞİL — doğrulandı: EnforceRetention yalnız spans /
// logs / metric_points / profiles / exemplars / span_links{,_reverse}
// partition'larını DROP ediyor, bu ikisine dokunmuyor. Yani partition
// düşürülebilir, ama veri KORUYAN bir repartition göçüyle (CREATE yeni +
// INSERT SELECT + RENAME) — bir sürümün değil, bir spec'in işi.
//
// Doğruluk bugün AYAKTA ama tek frene asılı: FINAL'in partition'lar arası
// birleştirmesi. ÖLÇÜLDÜ — aynı sorgu
// do_not_merge_across_partitions_select_final=1 ile tek problem id'si
// için ÜÇ satır döndürüyor. Kural P1'in tam olarak uyardığı yer.
//
// Operatör kararı bekleyen iki çare: (1) state tablolarını shard'dan
// bağımsız kıl (ortak replica path / Distributed + id sharding key) —
// asıl düzeltme; (2) state havuzunu tek host'a pinle — tutarlılığı alır,
// HA'yı verir. İkisi de prod etkili, ikisi de bu dilimin dışında.
var partitionNotInOrderBy = map[string]string{
	"anomaly_events": "started_at = olayın BAŞLANGICI ve mergeAnomalyCarry " +
		"onu ASLA tazelememeye söz veriyor. ÖLÇÜM 2026-08-23: 185 id'nin " +
		"28'i yine de >1 gün partition'ında. TEŞHİS v0.9.1306: yazıcı değil " +
		"TOPOLOJİ — taşıma SELECT'i satırı taşımayan shard'a düşünce 0 satır " +
		"dönüyor, exists=false oluyor, started_at taze basılıyor. Yukarıdaki " +
		"blokta tam gerekçe; çare veri koruyan repartition göçü (spec).",
	"problems": "started_at = problemin BAŞLANGICI; problemInsertArgs onu " +
		"mevcut satırdan taşır. ÖLÇÜM 2026-08-23: 4560 id'nin 21'i >1 gün " +
		"partition'ında. TEŞHİS v0.9.1306: %100 TÜREV — 21/21 kayan id " +
		"`anomaly-auto:`, 50/50 satırın started_at'i bir anomaly_events " +
		"değerine nanosaniyesine kadar eşit. anomaly_events düzelince bu da " +
		"düzelir; bağımsız bir yazıcı hatası YOK (anomaly dışı 4476 id: 0).",
	"incidents": "started_at operatörün açtığı olayın başlangıcı, güncelleme " +
		"yollarında taşınıyor. ÖLÇÜM 2026-08-23: 2861 id, 0 çoklu-partition.",
	"runbook_executions": "started_at bir koşumun başlangıcı — koşum başına " +
		"bir kez yazılır, statü güncellemeleri onu taşır. ÖLÇÜM 2026-08-23: " +
		"0 satır (lokalde koşum yok), kolon semantiği tek-yazımlı.",
}

// createTableRe — ifadenin BAŞINDA duran CREATE TABLE ve tablo adı.
var createTableRe = regexp.MustCompile(
	`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)

// replacingEngineRe — ENGINE = [Replicated]ReplacingMergeTree(...).
var replacingEngineRe = regexp.MustCompile(
	`(?is)ENGINE\s*=\s*(?:Replicated)?ReplacingMergeTree\s*\(`)

// clauseRe — bir DDL yan tümcesini SONRAKİ yan tümceye kadar yakalar.
// Çok satırlı ORDER BY'lar (service_callers_5m) satır-sonu ile kesilemez.
func clauseRe(kw string) *regexp.Regexp {
	return regexp.MustCompile(`(?is)\b` + kw + `\s+(.*?)` +
		`(?:\n\s*(?:TTL|SETTINGS|PARTITION\s+BY|ORDER\s+BY|PRIMARY\s+KEY|SAMPLE\s+BY)\b|$)`)
}

var (
	partitionByRe = clauseRe(`PARTITION\s+BY`)
	orderByRe     = clauseRe(`ORDER\s+BY`)
	identRe       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// exprColumns — bir SQL ifadesindeki KOLON adları: tanımlayıcılardan hemen
// ardından '(' geleni (fonksiyon adı) atar. `toYYYYMM(computed_at)` →
// {computed_at}. RE2'de lookahead yok, o yüzden indeksle bakıyoruz.
func exprColumns(expr string) []string {
	var out []string
	for _, loc := range identRe.FindAllStringIndex(expr, -1) {
		rest := strings.TrimLeft(expr[loc[1]:], " \t\r\n")
		if strings.HasPrefix(rest, "(") {
			continue // fonksiyon adı
		}
		out = append(out, expr[loc[0]:loc[1]])
	}
	return out
}

// TestReplacingMergeTreePartitionColumnsAreInOrderBy — Kural P1'in kalıcı
// kapısı. Yeni bir ReplacingMergeTree, ORDER BY'da olmayan bir kolonla
// partition'lanırsa bu test kızarır ve yazarı ya kolonu ORDER BY'a alır, ya
// PARTITION BY'ı düşürür, ya da sicile gerekçe yazar.
func TestReplacingMergeTreePartitionColumnsAreInOrderBy(t *testing.T) {
	tables := migrateDDLSlice(t, "tables")

	// POZİTİF KONTROL — gezici gerçekten ifade görüyor mu? Bu assert
	// olmadan "hiç ihlal yok" sonucu, hiçbir şey ayrıştıramamış ölü bir
	// taramada da yeşil yanardı.
	if len(tables) < 50 {
		t.Fatalf("`tables` yalnız %d eleman verdi — gezici ölü tarama yapıyor olabilir", len(tables))
	}

	scannedRMT, withPartition := 0, 0
	seen := map[string]bool{}

	for _, stmt := range tables {
		m := createTableRe.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		name := m[1]
		loc := replacingEngineRe.FindStringIndex(stmt)
		if loc == nil {
			continue
		}
		scannedRMT++
		seen[name] = true
		tail := stmt[loc[0]:] // ENGINE'den sonrası: yan tümceler burada

		pm := partitionByRe.FindStringSubmatch(tail)
		if pm == nil {
			continue // partition yok → P1 imkânsız (doğru şekil)
		}
		withPartition++
		om := orderByRe.FindStringSubmatch(tail)
		if om == nil {
			t.Errorf("%s: ReplacingMergeTree ama ORDER BY ayrıştırılamadı", name)
			continue
		}
		orderCols := map[string]bool{}
		for _, c := range exprColumns(om[1]) {
			orderCols[c] = true
		}
		var missing []string
		for _, c := range exprColumns(pm[1]) {
			if !orderCols[c] {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			continue
		}
		if _, ok := partitionNotInOrderBy[name]; ok {
			continue // bilinen + gerekçelendirilmiş
		}
		t.Errorf("KURAL P1 — %s: PARTITION BY kolon(lar)ı %v ORDER BY'da "+
			"(%s) DEĞİL.\nReplacingMergeTree yalnız partition İÇİNDE dedup "+
			"eder: bu kolon yeniden yazımda değişirse eski satır başka bir "+
			"partition'da ÖLÜMSÜZ kalır ve arka plan birleştirmesi onu asla "+
			"toplayamaz (root_cause_hypotheses, v0.9.1304).\nSeçenekler: "+
			"kolonu ORDER BY'a al · PARTITION BY'ı düşür (ai_feedback / "+
			"rca_verdicts / root_cause_hypotheses emsali) · kolonun yeniden "+
			"yazımda DEĞİŞMEDİĞİNİ doğrulayıp partitionNotInOrderBy siciline "+
			"gerekçesiyle ekle.",
			name, missing, strings.Join(strings.Fields(om[1]), " "))
	}

	// POZİTİF KONTROL 2 — tarama gerçekten RMT + partition şekli görüyor mu?
	// Sicildeki dört tablo tam olarak bu şekle sahip; sıfır görmek,
	// ayrıştırıcının bozulduğu anlamına gelir.
	if scannedRMT < 20 {
		t.Errorf("yalnız %d ReplacingMergeTree görüldü — motor regex'i "+
			"bozulmuş olabilir (beklenen: 30+)", scannedRMT)
	}
	if withPartition < len(partitionNotInOrderBy) {
		t.Errorf("PARTITION BY taşıyan yalnız %d RMT görüldü ama sicilde %d "+
			"tablo var — yan tümce ayrıştırıcısı ısırmıyor",
			withPartition, len(partitionNotInOrderBy))
	}

	// Sicil BAYATLAMASIN: kaydı olan her tablo hâlâ `tables` diliminde
	// olmalı. Tablo silinmişse gerekçe de gitmeli.
	for name := range partitionNotInOrderBy {
		if !seen[name] {
			t.Errorf("partitionNotInOrderBy['%s'] var ama tablo `tables` "+
				"diliminde yok — sicil bayatladı", name)
		}
	}

	// v0.9.1304'ün kendisi: düzeltilen tablo sicile GERİ yazılarak
	// "düzeltilmiş" sayılamaz.
	if _, ok := partitionNotInOrderBy["root_cause_hypotheses"]; ok {
		t.Error("root_cause_hypotheses sicile eklenmiş — v0.9.1304 düzeltmesi " +
			"PARTITION BY'ı SÖKMEKTİ, muafiyet yazmak değil")
	}
}

// ── Dar regresyon: root_cause_hypotheses DDL'i ──────────────────────────

func TestRootCauseHypothesesHasNoPartitionBy(t *testing.T) {
	tables := migrateDDLSlice(t, "tables")
	ddl := tableDDLByName(tables, "root_cause_hypotheses")
	if ddl == "" {
		t.Fatal("root_cause_hypotheses CREATE ifadesi `tables` diliminde bulunamadı")
	}
	loc := replacingEngineRe.FindStringIndex(ddl)
	if loc == nil {
		t.Fatal("ReplacingMergeTree motoru bulunamadı — tablo sınıf değiştirdiyse " +
			"bu testin gerekçesi yeniden düşünülmeli")
	}
	tail := ddl[loc[0]:]

	if m := partitionByRe.FindStringSubmatch(tail); m != nil {
		t.Errorf("root_cause_hypotheses yine PARTITION BY taşıyor (%q). "+
			"computed_at HER tikte `now` ile yeniden yazılıyor ve ORDER BY'da "+
			"değil → ay sınırını geçen anchor'ın eski satırı ölümsüz kopya "+
			"olur (v0.9.1304, Kural P1).", strings.TrimSpace(m[1]))
	}

	// Dedup anahtarı DEĞİŞMEMELİ: computed_at'i ORDER BY'a eklemek de
	// "düzeltme" gibi görünür ama her yeniden hesabı yeni satır yapar.
	om := orderByRe.FindStringSubmatch(tail)
	if om == nil {
		t.Fatal("ORDER BY ayrıştırılamadı")
	}
	got := strings.Join(strings.Fields(om[1]), " ")
	if want := "(anchor_kind, anchor_id)"; got != want {
		t.Errorf("ORDER BY %q, beklenen %q — dedup anahtarı MÜNHASIRAN "+
			"anchor kimliği olmalı (computed_at eklemek her yeniden hesabı "+
			"yeni satır yapar)", got, want)
	}

	// TTL yaşamalı: partition düştü, satır-düzeyi TTL tek temizleyici.
	if !strings.Contains(strings.ToUpper(tail), "TTL TODATE(COMPUTED_AT) + INTERVAL 30 DAY") {
		t.Error("30 günlük TTL kayboldu — PARTITION BY düştüğüne göre tabloyu " +
			"sınırlayan TEK şey bu (ai_feedback / rca_verdicts emsali)")
	}
}

// ── Saf yardımcılar ─────────────────────────────────────────────────────

func TestRootCauseNeedsRepartition(t *testing.T) {
	for _, tc := range []struct {
		name       string
		partionKey string
		want       bool
	}{
		{"taze kurulum (tablo yok → probe boş döner)", "", false},
		{"düzeltme uygulanmış", "", false},
		{"boşluk = partition yok", "   ", false},
		{"eski şema", "toYYYYMM(computed_at)", true},
		{"başka bir eski partition biçimi", "toDate(computed_at)", true},
	} {
		if got := rootCauseNeedsRepartition(tc.partionKey); got != tc.want {
			t.Errorf("%s: rootCauseNeedsRepartition(%q) = %v, beklenen %v",
				tc.name, tc.partionKey, got, tc.want)
		}
	}
}

func TestRootCauseDropStmt(t *testing.T) {
	for _, tc := range []struct{ name, onCluster string }{
		{"tek düğüm", ""},
		{"küme", " ON CLUSTER `c1`"},
	} {
		got := rootCauseDropStmt(tc.onCluster)
		if !strings.Contains(got, "DROP TABLE IF EXISTS root_cause_hypotheses") {
			t.Errorf("%s: hedef tablo yok: %q", tc.name, got)
		}
		if tc.onCluster != "" && !strings.Contains(got, tc.onCluster) {
			t.Errorf("%s: ON CLUSTER yan tümcesi düşmüş: %q", tc.name, got)
		}
		// SYNC şart: znode temizlenmeden gelen CREATE "Replica already
		// exists" ile patlar.
		if !strings.Contains(got, " SYNC") {
			t.Errorf("%s: SYNC yok — Replicated motorda CREATE yarışır: %q", tc.name, got)
		}
		// v0.8.190: boot DROP'u CH'nin boyut korumasına takılıp prod'u
		// crash-loop'a sokmuştu.
		if !strings.Contains(got, "max_table_size_to_drop = 0") {
			t.Errorf("%s: boyut koruması bypass'ı yok: %q", tc.name, got)
		}
	}
}

func TestTableDDLByName(t *testing.T) {
	tables := []string{
		"CREATE TABLE IF NOT EXISTS spans (a String) ENGINE = MergeTree()",
		"CREATE TABLE IF NOT EXISTS span_links (b String) ENGINE = MergeTree()",
		"CREATE TABLE IF NOT EXISTS logs\n\t(c String) ENGINE = MergeTree()",
		"CREATE TABLE IF NOT EXISTS metric_points(d String) ENGINE = MergeTree()",
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS e String",
	}
	for _, tc := range []struct{ name, wantSub string }{
		// Önek çakışması: "spans" araması "span_links"i yakalamamalı.
		{"spans", "(a String)"},
		{"span_links", "(b String)"},
		// Ad ile '(' arasında satırbaşı / doğrudan '(' — iki sınır biçimi.
		{"logs", "(c String)"},
		{"metric_points", "(d String)"},
	} {
		got := tableDDLByName(tables, tc.name)
		if !strings.Contains(got, tc.wantSub) {
			t.Errorf("tableDDLByName(%q) = %q, %q içermeliydi", tc.name, got, tc.wantSub)
		}
	}
	if got := tableDDLByName(tables, "yok_boyle_tablo"); got != "" {
		t.Errorf("olmayan tablo için %q döndü, boş beklenirdi", got)
	}
	// Yalnız CREATE TABLE eşleşmeli — ALTER değil.
	if got := tableDDLByName([]string{"ALTER TABLE zz ADD COLUMN x String"}, "zz"); got != "" {
		t.Errorf("ALTER ifadesi eşleşti: %q", got)
	}
}
