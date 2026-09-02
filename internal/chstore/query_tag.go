package chstore

// query_tag.go — v0.10.254 (perf profili §7 madde 8, P-comment): her CH
// sorgusuna `log_comment` etiketi.
//
// Bugüne dek uygulama sorguları işaretlenmiyordu; prod'da endpoint →
// query_log eşlemesi yalnız SQL metni imzasıyla kuruluyordu (perf-triage
// "bilinen boşluk"). Etiket ctx'te taşınır (WithQueryTag), tracedConn her
// Query/Exec/QueryRow/Select/PrepareBatch'te clickhouse ayarlarına
// `log_comment` olarak basar. Kaynaklar: `route:<GET /api/...>` (serveCached),
// `worker:<ad>` (main.go arka plan işçileri), `admin:playground`.
//
// clickhouse-go v2 `WithSettings` ayar haritasını DEĞİŞTİRİR, birleştirmez
// (context.go:122). Bu yüzden ctx-düzeyi ayarlar depoda TEK kapıdan geçer
// (WithQuerySettings — kendi anahtarında da tutar); applyQueryTag o
// haritayı + etiketi birleştirir. Doğrudan clickhouse.WithSettings çağrısı
// yasak (query_tag_test kaynak taraması) — aksi hâlde async_insert /
// playground ayarları etiketle ezilirdi.
//
// query_log'da: WHERE log_comment LIKE 'route:%' GROUP BY log_comment.

import (
	"context"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type queryTagKey struct{}
type querySettingsKey struct{}

const queryTagMax = 120

// WithQueryTag — ctx'e sorgu etiketi (sanitize edilir; boş → aynı ctx).
func WithQueryTag(ctx context.Context, tag string) context.Context {
	t := sanitizeQueryTag(tag)
	if t == "" {
		return ctx
	}
	return context.WithValue(ctx, queryTagKey{}, t)
}

// QueryTag — ctx'teki etiket ("" = yok).
func QueryTag(ctx context.Context) string {
	t, _ := ctx.Value(queryTagKey{}).(string)
	return t
}

// sanitizeQueryTag — log_comment düz metin: yazdırılabilir ASCII, tırnak/
// yeni satır yok, en çok queryTagMax bayt. SAF.
func sanitizeQueryTag(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 10 || r == 13 || r == 9 || r == 39 || r == 34 || r == 92: // \n \r \t ' " backslash
			b.WriteByte(' ')
		case r < 32 || r > 126:
			continue
		default:
			b.WriteRune(r)
		}
		if b.Len() >= queryTagMax {
			break
		}
	}
	// Ardışık boşluklar tek boşluğa (tırnak/yeni satır silinince kalan izler).
	return strings.Join(strings.Fields(b.String()), " ")
}

// WithQuerySettings — ctx-düzeyi ClickHouse ayarlarının TEK kapısı: hem
// clickhouse-go'ya hem kendi anahtarımıza yazar (applyQueryTag birleştirsin).
func WithQuerySettings(ctx context.Context, settings clickhouse.Settings) context.Context {
	merged := clickhouse.Settings{}
	if prev, ok := ctx.Value(querySettingsKey{}).(clickhouse.Settings); ok {
		for k, v := range prev {
			merged[k] = v
		}
	}
	for k, v := range settings {
		merged[k] = v
	}
	ctx = context.WithValue(ctx, querySettingsKey{}, merged)
	return clickhouse.Context(ctx, clickhouse.WithSettings(merged))
}

// applyQueryTag — tracedConn girişi: etiket varsa ayarlara log_comment
// eklenmiş yeni ctx; yoksa aynen. Var olan ayarlar korunur.
func applyQueryTag(ctx context.Context) context.Context {
	tag := QueryTag(ctx)
	if tag == "" {
		return ctx
	}
	merged := clickhouse.Settings{}
	if prev, ok := ctx.Value(querySettingsKey{}).(clickhouse.Settings); ok {
		if prev["log_comment"] == tag {
			return ctx // zaten uygulanmış (iç içe çağrı)
		}
		for k, v := range prev {
			merged[k] = v
		}
	}
	merged["log_comment"] = tag
	ctx = context.WithValue(ctx, querySettingsKey{}, merged)
	return clickhouse.Context(ctx, clickhouse.WithSettings(merged))
}

// RouteQueryTag — HTTP yolu etiketi ("route:GET /api/traces"); pattern boşsa
// yol. SAF.
func RouteQueryTag(pattern, path string) string {
	p := strings.TrimSpace(pattern)
	if p == "" {
		p = strings.TrimSpace(path)
	}
	return sanitizeQueryTag("route:" + p)
}
