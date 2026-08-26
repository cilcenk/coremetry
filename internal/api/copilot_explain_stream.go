package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// copilot_explain_stream.go — tek-atış ✨ Explain yüzeylerinin SSE
// varyantı (v0.9.1127, AI Assistant Faz 1.5;
// docs/plans/ai-assistant-design-2026-08-16.md).
//
// SORUN: sohbet v0.8.404'ten beri token token akıyor, ✨ Explain ise
// 15-40 saniye boyunca dönen bir spinner gösterip cevabı TEK PARÇA
// basıyordu. Yerel 2B-sınıfı modelde bu fark ölçülebilir: aynı cevap,
// akarken "hızlı", biriktirilirken "asılı kalmış" hissettiriyor.
//
// ŞEKİL: handler'lar cevaplarını artık writeJSON ile DEĞİL, buradaki TEK
// deliverExplain'den yazıyor. Kip isteğin kendisinden geliyor (`?stream=1`);
// bayraksız istek bugünkü buffered gövdeyi BAYT BAYT alıyor. Bu yüzden
// yeni bir route, yeni bir handler ya da handler başına kip dalı YOK —
// aksi halde 8 yüzeyde 8 kopya doğar ve ilk drift orada başlar (1024
// bütçesinin üç ayrı builder'da yaşadığı Faz 1.2 dersi).
//
// ÇERÇEVE ŞEKLİ copilot_drawer.go'nunkiyle BİREBİR aynı (delta{text} →
// answer{text,exchangeId,…} → done{ok}); frontend'in elle yazılmış SSE
// okuyucusu iki yüzeyi de değişmeden okuyabilsin diye. Handler'ların
// ekstra alanları (evidenceSpanIds, code, similarCount…) `answer`
// çerçevesine biner — buffered gövdedeki anahtar kümesiyle aynı.

// explainWantsStream — istek akan varyantı mı istiyor?
//
// Sorgu parametresi, gövde bayrağı değil: bu uçların bir kısmı gövdesiz
// POST alıyor (decodeExplainOptions'ın geriye uyumluluk sözleşmesi) ve
// gövdeyi zorunlu kılmak eski istemcileri kırardı. Ayrıca `?stream=1`
// tarayıcı ağ sekmesinde ve erişim loglarında GÖRÜNÜR — hangi çağrının
// akan yolda olduğu bir gövdeyi açmadan okunur.
func explainWantsStream(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	switch strings.TrimSpace(r.URL.Query().Get("stream")) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// explainRun — bir explain'in ÜRETİM yarısı. onDelta nil ise buffered
// çağrı beklenir (bugünkü yol), dolu ise akan ikiz. Sıfır delta üretip
// tam metin dönmek GEÇERLİdir: StreamText akıyamayan uçta şeffaf biçimde
// buffered'a düşer ve kod bağlamlı yol bilerek akmaz (aşağıya bakınız).
type explainRun func(onDelta func(string)) (string, error)

// explainPrompt — standart system+user üretim yarısı. Kip seçimini TEK
// yerde yapar: handler ne buffered ne stream bilir, yalnız prompt'unu
// verir.
func (s *Server) explainPrompt(r *http.Request, system, user string) explainRun {
	return func(onDelta func(string)) (string, error) {
		if onDelta == nil {
			return s.copilotExplain(r, system, user)
		}
		return s.copilotExplainStream(r, system, user, onDelta)
	}
}

// explainPromptBuffered — akmayacağı BİLİNEN üretim yarısı. Bugün tek
// kullanıcısı "Kodu da incele" yolu (copilotExplainCode): o yol bağlam
// taşmasında kod bloğunu yarıya indirip çağrıyı BAŞTAN yapıyor. Yarısı
// akmış bir cevabın üstüne ikinci bir cevap yazmak, operatöre iki farklı
// açıklamayı arka arkaya göstermek olurdu — akmamak burada doğru karar.
// SSE kipinde yine SSE ile cevaplanır, yalnız delta üretmez (answer
// çerçevesi aynen düşer, FE hiç fark etmez).
func explainPromptBuffered(call func() (string, error)) explainRun {
	return func(func(string)) (string, error) { return call() }
}

// explainBody — buffered gövde. Anahtar kümesi v0.9.1126'daki handler
// gövdeleriyle BİREBİR aynı (map olduğu için json.Marshal anahtarları
// zaten sıralı basıyor) — akan varyantın bedeli, bayraksız istemcinin
// gördüğü tek bir bayt bile OLMAMALI.
func explainBody(text, xid string, extra map[string]any) map[string]any {
	m := map[string]any{"explanation": text, "exchangeId": xid}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// explainAnswerFrame — SSE `answer` çerçevesi. Metin alanı `text`:
// copilot_drawer.go'nun emit ettiği şekil budur ve frontend okuyucusu o
// şekle göre yazılmıştır. Ekstra alanlar (evidenceSpanIds, code,
// similarCount…) aynı çerçeveye biner — akan kipte kaybolurlarsa
// waterfall kutulaması, kaynak dipnotu ve "N geçmiş çözüm" satırı sessizce
// yok olurdu.
func explainAnswerFrame(text, xid string, extra map[string]any) map[string]any {
	m := map[string]any{"text": text, "exchangeId": xid}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// sseEmitter — SSE çerçeve yazıcısı, TEK yazılış (v0.9.1129'da
// deliverExplain'in içinden çıkarıldı; ikinci tüketici insight kartı).
//
// BAŞLIKLAR TEMBEL yazılıyor ve bu bilinçli: ilk çerçeve düşene kadar
// yanıt gövdesine tek bayt gitmez, dolayısıyla üretim İLK BAYTTAN ÖNCE
// patlarsa (503 kota, bağlantı reddi, model hatası) istemci gerçek bir
// HTTP hata kodu görür — buffered kiple bayt bayt aynı hata. 200 + SSE
// içinde gizlenmiş bir hata, FE'nin retry/uyarı davranışını sessizce
// değiştirirdi. Akış BAŞLADIKTAN sonraki hata artık statü koduyla
// anlatılamaz; orada `error` + `done{ok:false}` çerçevesi düşer.
//
// newSSEEmitter ok=false döner: writer Flush edemiyorsa (ara katman /
// proxy sarımı) çağıran buffered yola DÜŞMEK zorunda — asla flush
// edilmeyen yarım bir SSE gövdesi istemciyi asar.
type sseEmitter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func newSSEEmitter(w http.ResponseWriter) (*sseEmitter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	return &sseEmitter{w: w, flusher: f}, true
}

func (e *sseEmitter) emit(event string, payload any) {
	if !e.started {
		e.w.Header().Set("Content-Type", "text/event-stream")
		e.w.Header().Set("Cache-Control", "no-cache")
		e.w.Header().Set("Connection", "keep-alive")
		e.w.Header().Set("X-Accel-Buffering", "no")
		e.started = true
	}
	b, _ := json.Marshal(payload)
	fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b)
	e.flusher.Flush()
}

// wroteAnything — ilk çerçeve düştü mü? Hata yolunun "gerçek HTTP
// hatası mı, error çerçevesi mi" kararı buna bakar.
func (e *sseEmitter) wroteAnything() bool { return e.started }

// deliverExplain — bir explain cevabının TEK çıkışı.
//
// Buffered kip (bayraksız istek, ya da akışı desteklemeyen bir
// ResponseWriter): bugünkü davranış — üret, hata varsa writeErr, yoksa
// writeJSON.
//
// Akan kip: delta* → answer → done. Başlık/flush disiplini
// sseEmitter'da (yukarı).
// explainLinkService — kimlik köprüsünün ORTAMI hangi servisten çıkacak.
//
// v0.10.55 — eskiden yalnız `?service=` query param'ı okunuyordu ve ✨
// Explain uçlarının HİÇBİRİ onu göndermiyor: env her zaman "prod"a
// düşüyordu (envFromServiceName("") → "" → varsayılan). Prod'da doğru
// sonuç verdiği için görünmezdi; prod-DIŞI bir trace açıklandığında link
// yanlış ortamın log sistemine gidiyordu.
//
// Handler'ın bildiği servis artık AÇIKÇA geçiyor. Query param yedek
// olarak kalıyor (sohbet yüzeyleri onu kullanıyor).
func (s *Server) deliverExplain(w http.ResponseWriter, r *http.Request, xid string, extra map[string]any, run explainRun, service string) {
	em, canStream := newSSEEmitter(w)
	// v0.10.35 — KİMLİK KÖPRÜSÜ TEK NOKTADAN. answerRequestIDLinks beş
	// sohbet yüzeyinde kabloluydu (chat, drawer, guided, RAG) ama ✨ Explain
	// yüzeylerinin HİÇBİRİNDE yoktu: operatör cevapta bir request_id
	// görüyor, log arayüzüne gitmek için elle kopyalıyordu.
	//
	// Burada hesaplamak 15 explain ucunun HEPSİNİ birden kazandırıyor
	// (trace, span, problem, exception…). Servis bilinmiyorsa
	// templateForService varsayılan şablona düşüyor, yani kırılmıyor.
	withLinks := func(out string) map[string]any {
		svc := service
		if svc == "" {
			svc = r.URL.Query().Get("service")
		}
		links := s.answerRequestIDLinks(r.Context(), out, svc)
		if len(links) == 0 {
			return extra
		}
		merged := make(map[string]any, len(extra)+1)
		for k, v := range extra {
			merged[k] = v
		}
		merged["links"] = links
		return merged
	}
	if !explainWantsStream(r) || !canStream {
		out, err := run(nil)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, explainBody(out, xid, withLinks(out)))
		return
	}

	out, err := run(func(d string) {
		if d == "" {
			return
		}
		em.emit("delta", map[string]string{"text": d})
	})
	if err != nil {
		if !em.wroteAnything() {
			writeErr(w, err)
			return
		}
		em.emit("error", map[string]string{"error": err.Error()})
		em.emit("done", map[string]bool{"ok": false})
		return
	}
	em.emit("answer", explainAnswerFrame(out, xid, withLinks(out)))
	em.emit("done", map[string]bool{"ok": true})
}
