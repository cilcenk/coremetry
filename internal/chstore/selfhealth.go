// selfhealth.go — v0.9.1279: "izleme sistemi kendisi öldü ve kimse
// bilmedi" sınıfının OKUMA yarısı (karar yarısı internal/evaluator/
// selfhealth.go).
//
// NEDEN VAR (2026-08-20 prod olayı + 2026-08-12 lokal küme olayı):
// meta-görünürlüğün tamamı bugüne dek PASİF panel'di. sysstats.go
// Disks/Servers/Drops/DistributionQueue okuyor, cardinality.go seri
// sayıyor — hiçbiri PROBLEM AÇMIYORDU. Spool bekçisinin (v0.9.985)
// kendi başlık yorumu bunu birebir yazıyor: "3.5 saatlik ölü ingest tüm
// ekranlarda yeşil görünüyordu". Panel, kimse bakmıyorsa alarm değildir.
//
// Bu dosya YENİ (mevcut chstore dosyalarına dokunulmadı) ve üç şey
// veriyor: (1) self_health eşik blobu, (2) MV-first ingest canlılık
// ölçümü, (3) sysstats'ın satır içi disk okumasının paylaşılan hâli.
package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ── Eşik vidası (system_settings blob) ──────────────────────────────────

// SelfHealthConfig — self-health alarm ailesinin eşikleri.
// runtime_alerts.go / problem_priority şablonu: key başına tek JSON
// blob, boot'ta zero-patch, UI sekmesi bu dilimde YOK (eşikler
// /api/settings üzerinden yazılabilir olduğunda da varsayılanlar kodda
// kalır — operatörün hiçbir şey yapmadığı kurulum doğru davranmalı).
type SelfHealthConfig struct {
	// Enabled — POINTER ve bu bilinçli. Zero-patch şemasında `false` ile
	// "alan hiç yazılmamış" ayırt EDİLEMEZ; bool olsaydı kaydedilen her
	// kısmi blob (örn. yalnız spoolMaxFiles) tüm aileyi sessizce
	// kapatırdı. nil = varsayılan (açık).
	Enabled *bool `json:"enabled,omitempty"`
	// IngestStallMin — "hiç veri gelmedi" penceresi, dakika. İki pencere
	// kıyaslanır (şimdi / bir önceki), yani gerçek karar süresi 2×.
	IngestStallMin int `json:"ingestStallMin"`
	// SpoolMaxFiles / SpoolMaxBytes — Distributed spool alarm eşikleri.
	// İKİSİ DE OR: az sayıda ama devasa dosya da, çok sayıda küçük dosya
	// da aynı arızadır (2026-08-20: 3M dosya / 965 GiB).
	SpoolMaxFiles uint64 `json:"spoolMaxFiles"`
	SpoolMaxBytes uint64 `json:"spoolMaxBytes"`
	// DiskEtaDays — bu kadar günden yakın bir "dolacak" projeksiyonu
	// problem açar.
	DiskEtaDays float64 `json:"diskEtaDays"`
	// ChannelConsecFails — bir bildirim kanalını ÖLÜ saymak için gereken
	// ardışık başarısız gönderim.
	ChannelConsecFails int `json:"channelConsecFails"`
	// VolumeSpikeFactor / VolumeSpikeMinSpans — v0.9.1294, hacim
	// sıçraması kuralı (self-volume-spike). Factor: son 24 saatlik span
	// hacmi önceki 24 saatin kaç katına çıkarsa sıçrama sayılır.
	// MinSpans: TABAN GÜRÜLTÜ KAPISI — küçük serviste 3 span → 30 span
	// 10×'tir ama hiçbir şey ifade etmez; güncel pencere bu mutlak
	// hacme ulaşmadan kural hiç konuşmaz.
	//
	// Eksik alan (bu sürümden ÖNCE kaydedilmiş her blob) patchSelfHealth
	// ile varsayılana düşer — kısmi blob'un aileyi bozmaması Enabled
	// pointer'ıyla aynı sözleşme.
	VolumeSpikeFactor   float64 `json:"volumeSpikeFactor"`
	VolumeSpikeMinSpans uint64  `json:"volumeSpikeMinSpans"`
}

// DefaultSelfHealth — kodda yaşayan varsayılanlar.
//
// Sayıların gerekçesi:
//   - 10 dk: bir rolling deploy + otelcol yeniden başlatması en kötü
//     ihtimalle ~2 dk sessizlik üretir; 10 dk onun beş katı ve 3.5
//     saatlik bir sessizliğin yanında hâlâ hızlı.
//   - 100k dosya / 10 GiB: v0.9.985'in CANLI ölçümü sağlıklı tabloları
//     0-1 dosyada, kilitlenmiş olanları 12.702-29.837 dosyada gördü;
//     prod olayı 3M dosya / 965 GiB'ti. 100k iki rejimin ÇOK üstünde —
//     bu alarm "gönderici yavaşladı" değil "gönderici öldü" alarmı.
//     (Spool bekçisinin kendi 2000'lik tabanı /api/health sinyalini
//     sürüyor; bu farklı ve daha ağır bir kapı: PROBLEM açıyor.)
//   - 7 gün: bir haftalık koşu payı, retention/disk büyütme kararını
//     mesai içinde vermeye yeter.
//   - 3 ardışık hata: tek bir ağ blip'i kanalı ölü ilan etmesin.
//   - 4× / 100k span: v0.9.1294 sıçrama kuralı, ikisi de CANLI ÖLÇÜMLE
//     kalibre edildi (101 servislik yerel demo, iki 24s penceresi):
//     meşru büyüme 1.19-1.30× bandında, en yüksek meşru değer 2.42×
//     (pencerenin ortasında doğan servis). 4×, ölçülen en kötü meşru
//     değerin 1.65 katı. Hacim tabanı: yerel demoda EN BÜYÜK servis 24
//     saatte 115.936 span üretiyor, yani 100k tabanı bu kurulumda tek
//     bir servisi bile konuşturmuyor; 1 milyar span/gün'lük hedef
//     ölçekte 100k = %0,01 ve o büyüklükteki bir sıçrama ne diski
//     doldurur ne kardinaliteyi patlatır.
func DefaultSelfHealth() SelfHealthConfig {
	on := true
	return SelfHealthConfig{
		Enabled:             &on,
		IngestStallMin:      10,
		SpoolMaxFiles:       100_000,
		SpoolMaxBytes:       10 << 30, // 10 GiB
		DiskEtaDays:         7,
		ChannelConsecFails:  3,
		VolumeSpikeFactor:   4.0,
		VolumeSpikeMinSpans: 100_000,
	}
}

const selfHealthKey = "self_health"

// SelfHealthOn — aile açık mı? nil pointer varsayılana (açık) düşer.
func (c SelfHealthConfig) SelfHealthOn() bool {
	return c.Enabled == nil || *c.Enabled
}

// GetSelfHealth — kalıcı config ya da varsayılanlar. CH hatasında
// varsayılana düşer: uzun ömürlü evaluator'da geçici bir blip
// self-health ailesini kapatmamalı (GetRuntimeAlerts ile aynı duruş).
func (s *Store) GetSelfHealth(ctx context.Context) SelfHealthConfig {
	d := DefaultSelfHealth()
	raw, err := s.GetSetting(ctx, selfHealthKey)
	if err != nil || len(raw) == 0 {
		return d
	}
	var c SelfHealthConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return d
	}
	return patchSelfHealth(c, d)
}

// patchSelfHealth — saf zero-patch çekirdeği (tablo testli).
// 0/negatif sayısal alan varsayılana döner; Enabled pointer olduğu için
// AYNEN korunur (açık false, "yazılmamış"tan farklıdır).
func patchSelfHealth(c, d SelfHealthConfig) SelfHealthConfig {
	if c.IngestStallMin <= 0 {
		c.IngestStallMin = d.IngestStallMin
	}
	if c.SpoolMaxFiles == 0 {
		c.SpoolMaxFiles = d.SpoolMaxFiles
	}
	if c.SpoolMaxBytes == 0 {
		c.SpoolMaxBytes = d.SpoolMaxBytes
	}
	if c.DiskEtaDays <= 0 {
		c.DiskEtaDays = d.DiskEtaDays
	}
	if c.ChannelConsecFails <= 0 {
		c.ChannelConsecFails = d.ChannelConsecFails
	}
	if c.VolumeSpikeFactor <= 0 {
		c.VolumeSpikeFactor = d.VolumeSpikeFactor
	}
	if c.VolumeSpikeMinSpans == 0 {
		c.VolumeSpikeMinSpans = d.VolumeSpikeMinSpans
	}
	return c
}

// SaveSelfHealth — admin PUT yolu (SaveRuntimeAlerts emsali).
func (s *Store) SaveSelfHealth(ctx context.Context, c SelfHealthConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, selfHealthKey, raw)
}

// ── Ingest canlılığı (MV-first) ─────────────────────────────────────────

// IngestWindows — iki ardışık pencerede ingest AKTİ Mİ ölçümü.
//
// Sayılar EŞİK için değil SIFIR/SIFIR-DEĞİL için okunuyor; o yüzden
// 5 dakikalık MV kovalarının pencere kenarına tam oturmaması önemsiz
// (her pencere en az bir TAM kova içerir).
type IngestWindows struct {
	// SpansCur / SpansPrev — service_summary_5m üzerinden pencere
	// toplamı. RAW spans OKUNMUYOR (mimari değişmez #3): milyar satırda
	// bir "var mı yok mu" sorusu için tam tarama yapmak bug olurdu.
	SpansCur  uint64
	SpansPrev uint64
	// MetricsLastSeenNs — metric_catalog'un maxMerge(last_seen_state)'i.
	// Metrik tarafında pencere TOPLAMI yok çünkü gerek yok: katalog MV'si
	// zaten "en taze metrik noktası ne zaman" sorusunu birkaç bin satırlık
	// anlık taramayla cevaplıyor (v0.8.396'nın picker düzeltmesi). 0 =
	// katalog boş (hiç metrik gelmemiş kurulum).
	MetricsLastSeenNs int64
	// NowNs — ÖLÇÜMÜN CH SAATİ. Uygulama saati DEĞİL: pencere kararı
	// CH'nin now()'ıyla kuruluyor, dolayısıyla metrik tazeliği de aynı
	// saatle kıyaslanmalı. İki saat arasındaki kayma yoksa fark yok;
	// varsa bu alan onu nötrler.
	NowNs int64
}

// ReadIngestWindows — iki MV okuması, tik başına bir kez.
//
// Pencereler: cur = [now-w, now), prev = [now-2w, now-w). Tek sorguda
// koşullu merge (countMergeIf) — iki round-trip yerine bir tane.
func (s *Store) ReadIngestWindows(ctx context.Context, window time.Duration) (IngestWindows, error) {
	var out IngestWindows
	w := int64(window / time.Second)
	if w <= 0 {
		return out, fmt.Errorf("selfhealth: geçersiz pencere %s", window)
	}
	// service_summary_5m bir AggregatingMergeTree MV'si; time_bucket
	// ORDER BY önekinde, yani iki pencerelik WHERE gerçek bir budama.
	if err := s.conn.QueryRow(ctx, `
		SELECT
		  toUInt64(countMergeIf(span_count_state, time_bucket >= now() - toIntervalSecond(?))) AS cur,
		  toUInt64(countMergeIf(span_count_state, time_bucket <  now() - toIntervalSecond(?))) AS prev
		FROM service_summary_5m
		WHERE time_bucket >= now() - toIntervalSecond(?)
		SETTINGS max_execution_time = 5`, w, w, 2*w).
		Scan(&out.SpansCur, &out.SpansPrev); err != nil {
		return out, err
	}
	// Katalog boşsa maxMerge 0 (epoch) döner ve satır YİNE gelir —
	// GROUP BY'sız agregat her zaman tek satırdır. 0, "hiç metrik
	// gelmemiş" demektir ve karar katmanı onu iddia OLARAK KULLANMAZ.
	if err := s.conn.QueryRow(ctx, `
		SELECT toUnixTimestamp64Nano(maxMerge(last_seen_state)) AS last_seen,
		       toUnixTimestamp64Nano(now64(9))                  AS now_ns
		FROM metric_catalog
		SETTINGS max_execution_time = 5`).
		Scan(&out.MetricsLastSeenNs, &out.NowNs); err != nil {
		return out, err
	}
	return out, nil
}

// ── Disk kimliği ────────────────────────────────────────────────────────

// DiskKey — bir volume'ün kararlı kimliği (düğüm + disk adı).
//
// self-disk-eta hem problem id'sini hem ETA serisinin map anahtarını
// BUNDAN türetir; iki yerde ayrı ayrı birleştirilseydi biri değişince
// seri sessizce sıfırlanır ve alarm bir daha hiç açılmazdı.
//
// Okuma tarafı için YENİ SORGU YOK: spool runbook'unun (v0.9.1191)
// CollectDisks'i zaten düğüm başına disk döndürüyor ve dağıtık-güvenli
// (clusterAllReplicas + yerel fallback). Aynı soruya ikinci bir SQL
// yazmak, v0.9.454'ün cluster()→clusterAllReplicas düzeltmesinin bir
// kopyada eskimesi demekti.
func DiskKey(host, disk string) string {
	if host == "" {
		return disk
	}
	return host + "/" + disk
}

// HumanBytes — humanBytes'ın dışa açık hâli. Alarm gerekçeleri
// (internal/evaluator) bayt biçimlemeyi paylaşsın diye: "12.3 GiB"
// operatöre iki farklı ekranda İKİ FARKLI şekilde yazılmamalı, ve
// ikinci bir biçimleyici yazmak tam olarak onu üretirdi.
func HumanBytes(b uint64) string { return humanBytes(b) }
