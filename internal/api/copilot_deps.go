package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// copilot_deps.go — bağımlılık sağlığı bundle'ları (v0.9.420, CoSRE
// fikir #5): "hangi db yavaş?", "kafka lag nasıl?". İkisi de MV-destekli
// tek okuma (db_summary_5m ailesi / messaging MV'leri) — spans taraması
// yok. Servisli soruda not düşülür (kırılımlar global; servis-özel detay
// Databases/Messaging sayfasında).

func (s *Server) guidedDBHealthBundle(ctx context.Context, emit func(string, any), service string, from, to time.Time, rangeS int64) (string, string, error) {
	emitGuidedStep(emit, "db_summary", "")
	dbs, err := s.store.GetDatabases(ctx, from, to)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Veritabanı kırılımı — son %s, filo geneli.\n", fmtAgoTR(rangeS))
	if service != "" {
		fmt.Fprintf(&b, "Not: soru %s servisini anıyor; bu kırılım TÜM çağıranları kapsar — servis-özel detay Databases sayfasında.\n", service)
	}
	if len(dbs) == 0 {
		b.WriteString("Pencerede DB çağrısı görülmedi (db.system'li span yok).\n")
		return b.String(), "db_summary_5m (boş)", nil
	}
	// En yoğun 10 — hacim bağlamı; ardından p95'e göre en yavaş 3 ayrıca işaretlenir.
	sort.SliceStable(dbs, func(i, j int) bool { return dbs[i].SpanCount > dbs[j].SpanCount })
	top := dbs
	if len(top) > 10 {
		fmt.Fprintf(&b, "(%d instance'tan en yoğun 10'u)\n", len(dbs))
		top = top[:10]
	}
	for _, d := range top {
		name := d.System + "/" + d.Instance
		if d.DBName != "" && d.DBName != "default" {
			name += "·" + d.DBName
		}
		fmt.Fprintf(&b, "- %s · %d çağrı · hata %%%.2f · avg %.1fms · p95 %.1fms\n",
			name, d.SpanCount, d.ErrorRate, d.AvgMs, d.P95Ms)
	}
	slow := make([]int, 0, len(top))
	for i := range top {
		slow = append(slow, i)
	}
	sort.SliceStable(slow, func(a, c int) bool { return top[slow[a]].P95Ms > top[slow[c]].P95Ms })
	if len(slow) > 0 {
		b.WriteString("p95'e göre en yavaşlar: ")
		for i, idx := range slow {
			if i >= 3 {
				break
			}
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s/%s (%.1fms)", top[idx].System, top[idx].Instance, top[idx].P95Ms)
		}
		b.WriteString("\n")
	}
	return b.String(), fmt.Sprintf("db_summary_5m MV (%d instance, %s)", len(dbs), fmtAgoTR(rangeS)), nil
}

func (s *Server) guidedMessagingBundle(ctx context.Context, emit func(string, any), service string, from, to time.Time, rangeS int64) (string, string, error) {
	emitGuidedStep(emit, "messaging_summary", "")
	rows, err := s.store.GetMessaging(ctx, from, to)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Mesajlaşma kırılımı — son %s, filo geneli.\n", fmtAgoTR(rangeS))
	if service != "" {
		fmt.Fprintf(&b, "Not: soru %s servisini anıyor; bu kırılım TÜM üretici/tüketicileri kapsar — servis-özel detay Messaging sayfasında.\n", service)
	}
	if len(rows) == 0 {
		b.WriteString("Pencerede messaging çağrısı görülmedi (messaging.system'li span yok).\n")
		return b.String(), "messaging MV (boş)", nil
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].SpanCount > rows[j].SpanCount })
	top := rows
	if len(top) > 10 {
		fmt.Fprintf(&b, "(%d destination'dan en yoğun 10'u)\n", len(rows))
		top = top[:10]
	}
	for _, m := range top {
		fmt.Fprintf(&b, "- %s/%s · %s · %d mesaj-span · hata %%%.2f · avg %.1fms · p95 %.1fms\n",
			m.System, m.Cluster, m.Destination, m.SpanCount, m.ErrorRate, m.AvgMs, m.P95Ms)
	}
	b.WriteString("Not: consumer-lag doğrudan ölçülmez (broker metriği ingest edilmiyor) — yüksek avg/p95 consumer tarafındaki işleme süresidir; lag şüphesinde Messaging sayfasındaki trend'e bak.\n")
	return b.String(), fmt.Sprintf("messaging MV (%d destination, %s)", len(rows), fmtAgoTR(rangeS)), nil
}
