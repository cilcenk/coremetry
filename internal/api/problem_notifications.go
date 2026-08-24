package api

// GET /api/problems/{id}/notifications — bu problem için ne
// gönderildi, kime, ve KİMSEYE GİTMEDİYSE bu (v0.9.1344).
//
// NEDEN AYRI BİR UÇ: /events zaten tüm gönderim defterini gösteriyor,
// ama triyaj eden operatör orada DEĞİL — açık problemin sayfasında.
// "Bu probleme kimse bakmıyor olabilir" bilgisini öğrenmek için
// /events'e gidip pencereyi daraltıp problem kimliğini araması
// gerekiyordu, yani pratikte hiç öğrenmiyordu.
//
// Kaynak notification_log; yeni tablo YOK. İki tür satır döner:
//   - gerçek gönderimler (kanal adı + hedef + sonuç),
//   - channelKind="none" / channelName="unmatched" işareti: hiçbir
//     kanal eşleşmedi VE ekip-yönlendirme de kimseyi bulamadı.
//     error sütunu haberin NEREDE kaybolduğunu anlatır.
//
// "Hiç yapılandırılmamış" hâli işaret ÜRETMEZ (internal/notify
// decideRouting) — kanalsız bir kurulumda her problem kırmızı yanmaz.
// Boş sonuç bu yüzden "henüz/hiç bildirim yok" demektir, "kayıp" değil;
// frontend ikisini ayrı çiziyor.
//
// Yetki: giriş yapmış her rol (viewer dahil). Hedefler zaten
// notification_log yazım anında maskeleme politikasından geçiyor ve
// içerik zaten operatörlere gitti — listNotificationLog ile aynı
// gerekçe. Yazma yok, denetim kaydı yok.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// problemNotificationsLimit — bir problemin gönderim geçmişi kanal
// sayısı mertebesinde; 100 fazlasıyla yeter ve okumayı tek sayfada
// tutar (ListNotificationLogByRelated zaten 90 günle sınırlı).
const problemNotificationsLimit = 100

func (s *Server) registerProblemNotificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET    /api/problems/{id}/notifications", s.listProblemNotifications)
}

func (s *Server) listProblemNotifications(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "problem id gerekli")
		return
	}
	// Önbellek anahtarı TÜM girdileri taşır (v0.5.187). Tek girdi var:
	// problem kimliği. Uzunluk-temelli bir sayı olsaydı iki farklı
	// problem birbirinin cevabını yerdi.
	key := fmt.Sprintf("prob-notif:%s", id)
	// 15sn — /events ile aynı TTL. Bildirimler saniyeler içinde
	// yazılıyor, drawer açılışında taze görünmesi bu pencereyle yeterli.
	s.serveCached(w, r, key, 15*time.Second, func(ctx context.Context) (any, error) {
		rows, err := s.store.ListNotificationLogByRelated(ctx, []string{id}, problemNotificationsLimit)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			// nil dilim JSON'da `null` olur ve frontend .map'te patlar
			// (boş küme kaybolur, sıfır olmaz sınıfı). Boş dizi döner.
			rows = []chstore.NotificationLog{}
		}
		return rows, nil
	})
}
