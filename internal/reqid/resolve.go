package reqid

// resolve.go — kimlik → trace_id çözümlemesi (v0.9.1142).
//
// Yol: logstore.Store.Search (CH ya da ES; ASLA ES'e direkt) ile kimliğin
// TOKEN'ı, kimliğin KENDİ penceresinde aranır ve eşleşen kayıtların
// trace bağlamı okunur. search_logs tool'unun ve /logs sayfasının
// kullandığı aynı okuma — ikinci bir sorgu yazılmıyor, dolayısıyla
// backend maliyet korumaları (soft timeout, LIMIT, pencere) aynen geçerli.

import (
	"context"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// SearchLimit — pencere içinde okunacak log satırı tavanı.
//
// 20: aynı kimlik birden çok satır loglar (giriş/çıkış/hata) ve trace
// bağlamı taşıyan satır bunların herhangi biri olabilir. Daha büyük bir
// tavan tek ES sorgusunun maliyetini boşuna büyütürdü — biz satırları
// LİSTELEMİYORUZ, tek bir trace_id çıkarıyoruz.
const SearchLimit = 20

// Resolution — çözümlemenin dürüst sonucu.
type Resolution struct {
	ID ID
	// TraceID — bulunan trace; "" = bulunamadı (HATA DEĞİL).
	TraceID string
	// SpanID — trace bağlamını taşıyan log satırının span'i (varsa);
	// waterfall içinde işaret etmeye yarar.
	SpanID string
	// Service — o log satırının servisi ("" = kayıt taşımıyor).
	Service string
	// LogTS — eşleşen log satırının damgası (unix ns, 0 = yok).
	LogTS int64
	// MatchedLogs — pencere+tavan içinde kimlikle eşleşen satır sayısı.
	MatchedLogs int
	// DistinctTraces — eşleşen satırlarda görülen FARKLI trace sayısı.
	// >1 ise cevap "tek trace" diye sunulmamalı (dürüstlük zarfı).
	DistinctTraces int
	// Partial — logstore dürüstlük zarfı: soft timeout / başarısız shard.
	// true ise satırlar gerçek cevabın ALT KÜMESİ, yani "bulunamadı"
	// kesin bir yokluk değildir.
	Partial bool
}

// Resolve — tek arama, tek pencere.
//
// ES MALİYET DİSİPLİNİ: WantCursor BİLEREK false (v0.9.286 — cursor
// istemeyen okuma PIT tutturmaz); tek Search çağrısı; pencere kimliğin
// kendisinden, yani dar.
//
// HasTrace filtresi KULLANILMIYOR ve bu v0.9.1084'ün dersi: ES
// mapping'inde yapısal trace alanı yoksa `exists` hiçbir doc'la eşleşmez
// ve filtre SESSİZCE her şeyi eler. Burada onu kullanmak "kimlik yok"
// diye yanlış cevap üretirdi. Satırları filtresiz alıp trace bağlamını
// KENDİMİZ seçiyoruz.
func Resolve(ctx context.Context, ls logstore.Store, id ID) (Resolution, error) {
	out := Resolution{ID: id}
	if ls == nil {
		return out, nil
	}
	from, to := id.Window()
	page, err := ls.Search(ctx, logstore.Filter{
		Search: id.Raw,
		From:   from,
		To:     to,
		Limit:  SearchLimit,
	})
	if err != nil {
		return out, err
	}
	if page == nil {
		return out, nil
	}
	out.Partial = page.Partial
	out.MatchedLogs = len(page.Logs)
	seen := map[string]bool{}
	for _, rec := range page.Logs {
		if rec == nil || rec.TraceID == "" {
			continue
		}
		if !seen[rec.TraceID] {
			seen[rec.TraceID] = true
		}
		if out.TraceID == "" {
			out.TraceID = rec.TraceID
			out.SpanID = rec.SpanID
			out.Service = rec.ServiceName
			out.LogTS = rec.Timestamp
		}
	}
	out.DistinctTraces = len(seen)
	return out, nil
}

// FmtLocal — kimliğin saat diliminde okunabilir damga. 24 saat
// konvansiyonu (v0.9.879) sunucu tarafında da geçerli: AM/PM yok.
func FmtLocal(t time.Time) string {
	return t.Format("2006-01-02 15:04:05.000 -07:00")
}
