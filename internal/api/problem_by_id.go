// v0.9.825 (operatör-raporlu) — BİLDİRİM DERİN LİNKİ KIRIKTI.
//
// E-postadaki "Open: <url>/problems?problem=<id>" bağlantısı, detayı
// LİSTE sorgusundan çözüyordu (AnomaliesPage → AlertProblemHost →
// useProblems({limit:200})). Liste "herhangi bir durumdan en yeni 200"
// penceresi; kayıt o pencerenin dışına düştüğü anda ekran "Problem not
// found" diyordu.
//
// Ve tam olarak DÜŞMESİ beklenen kayıtlar bunlar: bildirim gönderilen
// problem çoğu zaman çözülür, çözülünce listenin dibine iner, filo
// hareketliyse 200 satır birkaç saatte devrilir. Yani bağlantı en çok
// gerektiği anda — "gece 3'te gelen sayfayı sabah aç" — çalışmıyordu.
//
// Üstelik yanlış cümle kuruyordu: kayıt SİLİNMİŞ değil, problems
// tablosunda duruyor (90 günlük TTL). "Bulunamadı" demek, operatöre
// olmayan bir veri kaybı bildirmekti.
//
// ÇÖZÜM: tekil okuma ucu. Drawer önce listeden dener (sıfır maliyet,
// açık problemlerin %99'u orada), bulamazsa buraya düşer. E-postadaki
// URL DEĞİŞMEDİ — eski bildirimlerdeki bağlantılar da artık çözülüyor.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// errNotFound — handler'ların "istenen kayıt YOK" demesi için sentinel;
// writeErr onu 404'e çevirir (ve loglamaz). 500 ile karıştırılmaması
// şart: istemci "sunucu bozuk" ile "kayıt gitmiş"i ayırt edemezse
// kullanıcıya hata ekranı gösterir, oysa doğru cevap dürüst bir boş
// durumdur. errors.Is ile sarmalanarak kullanılır, böylece mesaj
// kayda özel kalabilir.
var errNotFound = errors.New("not found")

// notFoundf — errNotFound'u kayda özel bir mesajla sarmalar.
func notFoundf(format string, a ...any) error {
	return fmt.Errorf(format+": %w", append(a, errNotFound)...)
}

// getProblemByID — GET /api/problems/{id}.
//
// YETKİ: /api/problems ile AYNI duruş (rol kapısı yok). Bir bildirim
// bağlantısını açabilen herkes zaten listede o satırı görebiliyor;
// tekil okumayı daraltmak, aynı veriyi iki farklı kurala bağlamak olurdu.
//
// ROTA ÇAKIŞMASI YOK: Go 1.22 ServeMux'ta literal segment joker'i
// yener, yani /api/problems/count · /evaluator · /buckets kendi
// handler'larında kalır. {id}/rootcause gibi 4-segmentli yollar da bu
// 3-segmentli desenle eşleşmez.
func (s *Server) getProblemByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "problem id required", http.StatusBadRequest)
		return
	}
	// TTL /api/problems ile aynı 15 sn: aynı satırın iki farklı yoldan
	// okunması operatöre iki farklı tazelik göstermemeli. Anahtar
	// kimliği taşıyor — tek girdi, tamamı anahtarda (cache-key
	// sözleşmesi).
	s.serveCached(w, r, "problem:byid:v1:"+id, 15*time.Second, func(ctx context.Context) (any, error) {
		p, err := s.store.GetProblem(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			// GERÇEKTEN yok — 90 günlük TTL'i aşmış ya da hiç var
			// olmamış bir kimlik. serveCached'in notFound sözleşmesi
			// 404 üretir; bu, "listede yok" ile karıştırılmayan
			// DÜRÜST bir cevap.
			return nil, notFoundf("problem %q", id)
		}
		// Liste ucuyla AYNI zenginleştirme zinciri — runbook, takım,
		// cluster, deploy+öncelik, kök-sebep. Ayrı bir kısaltılmış
		// şekil dönmek, aynı kaydı iki yerde farklı gösterirdi
		// (ProblemDetail bu alanların hepsini çiziyor).
		probs := []chstore.Problem{*p}
		probs = s.store.EnrichProblemsWithRunbooks(ctx, probs)
		probs = s.store.EnrichProblemsWithTeams(ctx, probs)
		probs = s.store.EnrichProblemsWithClusters(ctx, probs, time.Hour)
		probs = s.enrichProblemsForRead(ctx, probs)
		probs = s.store.EnrichProblemsWithRootCause(ctx, probs)
		return probs[0], nil
	})
}
