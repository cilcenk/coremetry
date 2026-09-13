package chstore

// problem_category.go — v0.10.706 (Dynatrace paritesi #5, spec onayı
// 2026-09-13): Problem satırına iki OKUMA-ANI alanı, şema yok.
//
// Category — Davis'in AVAILABILITY / ERROR / SLOWDOWN / RESOURCE / CUSTOM
// sınıfı; rule_id öneki + metric + kind + comparator'dan saf türetim
// (computePriority'nin okuduğu alanlar). Neden saklanmıyor: 18 üretici
// var ve her biri "kategori yaz" diye dokunulsaydı ikinci yarısı unutulan
// bir sözleşme olurdu; tek saf fonksiyon eski satırlara da anında uygulanır.
//
// DisplayID — "P-xxxxx": problem id'sinin FNV-32a özeti base36. Sıralı
// DEĞİL (sayaç Redis INCR + kolon + iki-boot + üreticilere enjeksiyon
// isterdi; operatör kararı: türetilmiş). Amaç sesli söylenebilen kısa bir
// sap: palete/aramaya yazınca açılır. Çakışma ~%0.1 (2^32 uzayı, ~10K
// satır) → çözümde EN YENİ eşleşme kazanır (GetProblemByDisplayID).

import (
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
)

const (
	CategoryAvailability = "AVAILABILITY"
	CategoryError        = "ERROR"
	CategorySlowdown     = "SLOWDOWN"
	CategoryResource     = "RESOURCE"
	CategoryCustom       = "CUSTOM"
)

// ProblemCategories — çip sözlüğü (sıra = UI sırası).
var ProblemCategories = []string{CategoryAvailability, CategoryError, CategorySlowdown, CategoryResource, CategoryCustom}

// ProblemCategory — SAF, tablo-testli. Sıra önemli: özne/aile önce,
// metrik ailesi sonra, bilinmeyen → CUSTOM (uydurma yok).
func ProblemCategory(p Problem) string {
	rule, metric, cmp := p.RuleID, p.Metric, strings.TrimSpace(p.Comparator)
	dropping := cmp == "<" || cmp == "<="
	switch {
	// Dış kaynak (Oracle/Influx özneleri): down = kullanılamaz, tavan = kaynak.
	case strings.HasPrefix(rule, "anomaly:ext-down:"):
		return CategoryAvailability
	case strings.HasPrefix(rule, "anomaly:ext-cap:"):
		return CategoryResource
	case p.Kind == ProblemKindExternal:
		return CategoryCustom
	// Sentetik monitör, susan servis, trafik çöküşü, platform sağlığı.
	case strings.HasPrefix(rule, "monitor:") || metric == "uptime":
		return CategoryAvailability
	case strings.HasSuffix(rule, ":service_silent"):
		return CategoryAvailability
	case rule == "self-disk-eta" || rule == "self-spool-depth":
		return CategoryResource
	case strings.HasPrefix(rule, "self-"):
		return CategoryAvailability
	case (metric == "request_rate" || metric == "http_route_rate") && dropping:
		return CategoryAvailability
	case metric == "request_rate" || metric == "http_route_rate":
		return CategoryResource // yük sıçraması
	// Hata ailesi.
	case rule == "exception-storm" || strings.HasPrefix(metric, "exception"):
		return CategoryError
	case metric == "error_rate" || metric == "error_count" || metric == "log_query" || metric == "watcher" || metric == "anomaly_ratio":
		return CategoryError
	case metric == "http_route_error_rate" || metric == "kafka_producer_error_rate":
		return CategoryError
	case strings.HasPrefix(metric, "mq_") && strings.HasSuffix(metric, "_error_rate"):
		return CategoryError
	// Yavaşlama ailesi.
	case metric == "p50_ms" || metric == "p95_ms" || metric == "p99_ms" || metric == "avg_ms":
		return CategorySlowdown
	case strings.HasPrefix(metric, "db_stmt_") || metric == "db.statement_p95_ms":
		return CategorySlowdown
	case strings.HasPrefix(metric, "http_route_") && strings.HasSuffix(metric, "_ms"):
		return CategorySlowdown
	case strings.HasPrefix(metric, "mq_") && strings.HasSuffix(metric, "_ms"):
		return CategorySlowdown
	// Kaynak ailesi.
	case metric == "db.capacity" || strings.HasPrefix(metric, "runtime.") || metric == "kafka_lag_max":
		return CategoryResource
	}
	return CategoryCustom // anomali kümesi ("cluster"), SLO burn, bilinmeyen
}

// displayIDRe — "P-" + base36 (≤7 hane, uint32 tavanı "1z141z3"); harf
// duyarsız kabul, kanonik yazım küçük harf.
var displayIDRe = regexp.MustCompile(`^[Pp]-[0-9A-Za-z]{1,7}$`)

// ProblemDisplayID — SAF, kararlı: aynı id her zaman aynı sap.
func ProblemDisplayID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return "P-" + strconv.FormatUint(uint64(h.Sum32()), 36)
}

// IsProblemDisplayID — bir kimlik dizgisi görüntü kimliği mi (ham id değil).
func IsProblemDisplayID(s string) bool { return displayIDRe.MatchString(strings.TrimSpace(s)) }

// NormalizeProblemDisplayID — "p-3F9A2" → "P-3f9a2" (eşleşme küçük harf).
func NormalizeProblemDisplayID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		return s
	}
	return "P-" + strings.ToLower(s[2:])
}
