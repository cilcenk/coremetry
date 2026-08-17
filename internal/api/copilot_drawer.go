package api

import (
	"context"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilot_drawer.go — AI çekmecesi içindeki sohbetin BAĞLAM DEVRİ
// (v0.9.479, operatör raporu).
//
// Semptom (prod fotoğrafı): v0.9.477 AI çekmecesindeki "Chat'te devam
// et →" GLOBAL CoSRE penceresini çekmecenin ÜSTÜNE açıyordu (iki üst
// üste yüzey) ve sohbet ekrandaki açıklamayı BİLMİYORDU — ekranda bir
// exception grubu dururken CoSRE "bu trace ID'ye ait veri yok" deyip
// filo geneli JVM GC uyarılarını anlatıyordu.
//
// Kök neden iki katmanlı:
//  1. Global chat ayrı bir yüzeydi; çekmeceyle arasında `coremetry:
//     ai-ask` üstünden yalnız bir SORU METNİ geçiyordu, açıklama değil.
//  2. Guided router (copilot_guided.go) yalnız SON kullanıcı mesajına
//     bakar ve veriyi KENDİ prefetch eder; konuşma geçmişi narration
//     çağrısına hiç girmez. Yani konuşmaya sentetik bir "önceki cevap"
//     turu eklemek TEK BAŞINA çözmezdi — açıklama metni modele hiç
//     ulaşmazdı. Üstelik özneye oturmayan bir rota (servissiz
//     pod_health) filo geneli veri çekip konuyu tamamen kaçırıyordu.
//
// Çözüm: chat isteğine opsiyonel `context.explain` alanı. Alan BOŞKEN
// her şey bayt-bayt eski davranış (TestGuidedNarrationUserAbsentExplain
// bunu pinler). Doluyken:
//
//	a) guided rota somut bir özneye (servis/aile) oturmuyorsa guided
//	   ATLANIR — filo prefetch'i konuyu ıskalıyordu (drawerSuppressesGuided);
//	b) guided çalışırsa açıklama narration bloğuna ek bağlam olarak
//	   girer, cevap ekrandaki konuya bağlı kalır (guidedNarrationUser);
//	c) guided almazsa buradaki tek-çağrılı explain-grounded yol
//	   cevaplar — guided'ın prefetch+narrate deseninin aynısı, prefetch
//	   yerine operatörün AZ ÖNCE OKUDUĞU açıklama. Küçük model (gemma4)
//	   için doğru şekil bu: tool döngüsü değil, hazır bağlam + tek
//	   anlatım çağrısı.
//
// Not: explain bağlamı varken serbest tool döngüsüne ve RAG'a
// düşülmez — çekmece sohbeti ÖZNE-KAPSAMLIDIR. Filo geneli ve doküman
// soruları global CoSRE penceresinde kalır (tasarım kararı).
//
// ---------------------------------------------------------------------
// v0.9.482 (operatör raporu, v0.9.479 takibi) — HAM KANIT devri.
//
// Semptom (prod fotoğrafı): "Explain trace" trace'i VE ilişkili logları
// birlikte okuyup cevaplıyor; ama çekmece sohbeti yalnız o cevabın
// METNİNİ görüyordu. "logda ne yazıyor", "BKMQR-39 neden" gibi takipler
// ham kanıta hiç ulaşamadığı için kör cevaplanıyordu. Exception öznesi
// aynı boşluktaydı (explain'i zengin bir paket kuruyor: stacktrace +
// örnek trace + loglar + deploy penceresi).
//
// Çözüm: sohbet ÖZNE-FARKINDA. `context.subject` (frontend'in `?ai=`
// kodeği, lib/aiSubject.ts) ile gelen kind:id'den, ilgili explain'in
// kullandığı AYNI kanıt paketi sunucuda yeniden kurulur ve narration
// VERİ bloğuna açıklama metninin yanına konur:
//
//	trace / span → buildTraceExplainInput (explain_trace_input.go;
//	               explain handler'ıyla TEK kurucu)
//	exception    → anomaly.BuildExceptionExplainInput (v0.9.415)
//	diğerleri    → kanıtsız, v0.9.479 davranışı aynen (operatör:
//	               problem explain takipleri zaten iyi çalışıyor)
//
// Maliyet disiplini (v0.9.166 kuralı): kanıt SADECE operatör çekmecede
// soru sorunca, soru başına TEK sınırlı pass çekilir; poll/proaktif yok.
// Fetch hata verirse ya da boş dönerse sessizce v0.9.479'un metin-tabanlı
// anlatımına düşer — sohbet ASLA kanıt yüzünden kırılmaz.

// drawerExplainMaxRunes — açıklama bloğunun üst sınırı. Küçük modelde
// bağlam penceresi dar; tipik açıklama 1-3 KB, runbook çıktıları
// taşabilir. Rune bazlı kesilir (Türkçe metin: byte kesmesi karakter
// bölerdi) ve kesildiği modele SÖYLENİR.
const drawerExplainMaxRunes = 3000

// drawerHistoryTurns — narration bloğuna sığdırılan önceki tur sayısı
// (aktif soru hariç). Çekmece sohbeti kısa takiplerden ibarettir;
// tamamı taşınırsa küçük modelin bağlamı açıklamayı bastırır.
const drawerHistoryTurns = 6

const drawerTruncSuffix = "\n…(kısaltıldı)"

// drawerEvidenceMaxRunes — HAM KANIT bloğunun üst sınırı (v0.9.482).
// Bütçe: açıklama ≤3000 + kanıt ≤6000 + geçmiş; gemma4 sınıfı yerel
// modelde bağlam penceresini zorlamayan, ama 100 span'lik kompakt JSON +
// 15 logu tipik olarak TAMAMEN taşıyan bir tavan.
const drawerEvidenceMaxRunes = 6000

// drawerEvidenceMinHead — kanıt budanırken span/meta bölümünden korunacak
// asgari pay. Bunun altına düşülüyorsa loglar tek başına bütçeyi
// doldurmuş demektir; o zaman span listesi TÜMÜYLE bırakılır.
const drawerEvidenceMinHead = 800

const (
	drawerEvidenceCutNote      = "\n…(kanıt kısaltıldı)"
	drawerEvidenceTruncNote    = "\n…(kanıt kısaltıldı: span listesi budandı, loglar korundu)\n"
	drawerEvidenceLogsOnlyNote = "…(kanıt kısaltıldı: span listesi çıkarıldı, yalnız loglar taşındı)\n"
)

// clampDrawerExplain — açıklamayı buda + kırp. Saf; tablo-testli.
func clampDrawerExplain(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= drawerExplainMaxRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:drawerExplainMaxRunes])) + drawerTruncSuffix
}

// drawerSubject — çekmecenin öznesi (`?ai=<kind>:<id>[:<extra>]`).
// frontend/src/lib/aiSubject.ts kodeğinin sunucu tarafı ikizi.
type drawerSubject struct {
	Kind   string
	ID     string
	SpanID string // yalnız kind=span
}

// drawerEvidenceKinds — kanıt paketi KURULABİLEN özneler. Diğer
// özneler (problem/incident/anomaly/runbook/service-health) v0.9.479
// davranışında kalır: yalnız açıklama metni + konuşma (operatör:
// problem explain takipleri zaten iyi cevaplanıyor).
var drawerEvidenceKinds = map[string]bool{"trace": true, "span": true, "exception": true}

// parseDrawerSubject — `kind:enc(id)[:enc(spanId)]` çözümü. aiSubject.ts
// ile aynı katılıkta: id URL-decode edilir (servis adı/fingerprint ':'
// içerebilir), span TAM 3 parça ister, basit özneler TAM 2 parça ister.
// Yalnız kanıt kurulabilen kind'ler kabul edilir — geri kalanı için
// çağıran zaten hiçbir şey yapmaz. Saf; tablo-testli.
func parseDrawerSubject(raw string) (drawerSubject, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return drawerSubject{}, false
	}
	parts := strings.Split(raw, ":")
	kind := parts[0]
	if !drawerEvidenceKinds[kind] {
		return drawerSubject{}, false
	}
	id := ""
	if len(parts) > 1 {
		// Bozuk yüzdeli dizide QueryUnescape hata döner — çekmece sohbeti
		// bir URL parçası yüzünden asla patlamamalı, kanıtsız devam eder.
		dec, err := url.QueryUnescape(parts[1])
		if err != nil {
			return drawerSubject{}, false
		}
		id = dec
	}
	if id == "" {
		return drawerSubject{}, false
	}
	if kind == "span" {
		if len(parts) != 3 {
			return drawerSubject{}, false
		}
		sp, err := url.QueryUnescape(parts[2])
		if err != nil || sp == "" {
			return drawerSubject{}, false
		}
		return drawerSubject{Kind: kind, ID: id, SpanID: sp}, true
	}
	if len(parts) != 2 {
		return drawerSubject{}, false
	}
	return drawerSubject{Kind: kind, ID: id}, true
}

// clampDrawerEvidence — kanıt paketini narration bütçesine sığdırır.
// full = explain'in kullandığı TAM paket, logs = paketin İÇİNDEKİ log
// bölümü (aynı kurucudan gelir, string olarak bir kez geçer).
//
// Budama sırası bilinçli: ÖNCE span/trace listesi, LOGLAR EN SONA.
// Operatörün takip soruları log içeriğine dair ("logda ne yazıyor") —
// v0.9.482 raporunun ta kendisi. Kesme rune bazlı (Türkçe metin: byte
// kesmesi karakter bölerdi) ve modele AÇIKÇA söylenir. Saf; tablo-testli.
func clampDrawerEvidence(full, logs string) string {
	if strings.TrimSpace(full) == "" {
		return ""
	}
	if utf8.RuneCountInString(full) <= drawerEvidenceMaxRunes {
		return full
	}
	if logs == "" || !strings.Contains(full, logs) {
		// Log yok (ya da paketin parçası değil): tek parça, kuyruktan kes.
		return cutRunes(full, drawerEvidenceMaxRunes) + drawerEvidenceCutNote
	}
	keep := drawerEvidenceMaxRunes - utf8.RuneCountInString(logs)
	if keep < drawerEvidenceMinHead {
		// Loglar tek başına bütçeyi doldurdu: span listesini tümüyle bırak,
		// logların YÜKSEK SEVERITY başını taşı (LogsForTrace sıralaması).
		return drawerEvidenceLogsOnlyNote + cutRunes(logs, drawerEvidenceMaxRunes)
	}
	head := strings.Replace(full, logs, "", 1)
	return cutRunes(head, keep) + drawerEvidenceTruncNote + logs
}

// cutRunes — rune-güvenli baştan-kesme (kenar boşlukları kırpılır).
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(r[:n]))
}

// drawerSuppressesGuided — explain bağlamı varken guided rotanın
// bırakılıp bırakılmayacağı. Somut özne (servis ya da aile) yoksa
// guided prefetch FİLO GENELİNE düşer ve operatörün ekranındaki konuyu
// ıskalar — operatör raporundaki tam senaryo. Operatör açıkça filo
// istiyorsa ("tüm servislerde bu hata var mı?") guided kalır.
// Saf; tablo-testli.
func drawerSuppressesGuided(explain string, route guidedRoute, question string) bool {
	if strings.TrimSpace(explain) == "" || route.Intent == guidedNone {
		return false
	}
	if route.Service != "" || len(route.Family) > 0 {
		return false
	}
	// v0.9.1134 — ADI GEÇEN takım da SOMUT bir öznedir: operatör
	// "avengersy takımının servisleri" diye sorduğunda çekmecedeki
	// konu değil o takım kastediliyor, guided gerçek RED'i getirsin.
	if route.Team != "" {
		return false
	}
	// v0.9.1142 — YAPIŞTIRILMIŞ yapılandırılmış request kimliği en somut
	// öznedir: operatör çekmecedeki açıklamayı değil TAM O İSTEĞİ soruyor
	// ve kimliğin çözümü (log → trace) yalnız guided yolda var.
	//
	// NOT: aynı muafiyet route.TraceID/SpanID için YOK — bu, v0.9.479'dan
	// beri süregelen davranış ve bu dilimin kapsamı dışında bilinçli
	// bırakıldı (aynı sınıf bir boşluk gibi görünüyor; ayrı bir karar).
	if route.RequestID != "" {
		return false
	}
	return !wantsFleetScope(guidedTokens(normalizeGuidedMsg(question)))
}

// guidedNarrationUser — guided narration çağrısının user bloğu.
// explain BOŞKEN üretilen metin v0.9.478'dekiyle bayt-bayt aynıdır
// (regresyon testi bunu pinler); doluyken açıklama ek bir blok olarak
// eklenir — guided prompt'un (copilot.SystemPromptGuidedChat)
// "SADECE verilen veriye dayan" kuralı
// böylece açıklamayı da kapsar. Saf; tablo-testli.
func guidedNarrationUser(question, evidence, explain string) string {
	base := "SORU: " + question + "\n\nVERİ:\n" + evidence
	ex := clampDrawerExplain(explain)
	if ex == "" {
		return base
	}
	return base + "\n\nEKRANDAKİ AÇIKLAMA (operatör bu cevabı az önce okudu; soru buna dair olabilir):\n" + ex
}

// drawerEvidenceHeader — HAM KANIT bloğunun başlığı (v0.9.482). Modele
// bunun ÖZETLENMİŞ değil, açıklamanın dayandığı ham veri olduğunu söyler.
const drawerEvidenceHeader = "HAM KANIT (bu açıklamanın dayandığı veri — açıklamada geçmeyen ayrıntılar için BURAYA bak):\n"

// drawerSubjectEvidence — özneye karşılık gelen kanıt paketini yeniden
// kurar (v0.9.482). İlgili explain'in kullandığı AYNI kurucular; ikinci
// bir montaj kopyası yoktur. Her şey best-effort: hata/boş → "" ve
// çağıran metin-tabanlı anlatıma düşer (sohbet kırılmaz).
//
// Maliyet: soru başına TEK sınırlı pass, üst-zaman aşımı ile çitlenir —
// yavaş bir CH/ES sohbeti askıda bırakamaz.
func (s *Server) drawerSubjectEvidence(ctx context.Context, subj drawerSubject) string {
	ectx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	switch subj.Kind {
	case "trace", "span":
		// span öznesinde de TRACE paketi kurulur: takip soruları
		// ("logda ne yazıyor") komşuluk değil, trace + log kanıtı ister.
		// Odak span ayrıca yazılır ki model hangi satırın sorulduğunu bilsin.
		in, err := s.buildTraceExplainInput(ectx, subj.ID)
		if err != nil {
			return ""
		}
		body := clampDrawerEvidence(in.User, in.LogsBlock)
		if body == "" {
			return ""
		}
		if subj.Kind == "span" && subj.SpanID != "" {
			body = "Odak span: " + subj.SpanID + "\n" + body
		}
		return body
	case "exception":
		g, err := s.store.GetExceptionGroup(ectx, subj.ID)
		if err != nil || g == nil {
			return ""
		}
		in := anomaly.BuildExceptionExplainInput(ectx, s.store, s.logs, g)
		return clampDrawerEvidence(in.User, in.LogsBlock)
	}
	return ""
}

// drawerEvidenceStep — kanıt çekildiğinde operatöre gösterilen şeffaflık
// çipi (guided'ın "⚙ bundle" çipiyle aynı dil). Saf; tablo-testli.
func drawerEvidenceStep(kind string) string {
	switch kind {
	case "exception":
		return "kanıt: exception grubu + örnek trace + loglar"
	case "span", "trace":
		return "kanıt: trace span'leri + ilişkili loglar"
	}
	return ""
}

// drawerSourceNote — cevabın altına düşen DETERMİNİSTİK kaynak dipnotu.
// Kanıt gerçekten çekildiyse bunu söyler; çekilemediyse v0.9.479'un
// metni aynen kalır — dipnot asla olmayan bir kanıtı iddia etmez.
// Saf; tablo-testli.
func drawerSourceNote(evidenceKind string) string {
	switch evidenceKind {
	case "exception":
		return "\n\nKaynak: ekrandaki AI açıklaması + exception grubunun örnek trace/log kanıtı"
	case "trace", "span":
		return "\n\nKaynak: ekrandaki AI açıklaması + trace span'leri ve ilişkili loglar"
	}
	return "\n\nKaynak: ekrandaki AI açıklaması"
}

// drawerHistoryBlock — çekmece içinde sorulmuş önceki turların kompakt
// dökümü (aktif soru HARİÇ; boş metinli tool-result turları atlanır).
// Sıra eskiden yeniye. Saf; tablo-testli.
func drawerHistoryBlock(msgs []copilot.ChatMessage) string {
	// Aktif soru = son, metni dolu kullanıcı turu (lastUserText ile aynı
	// kural). Ondan öncesi geçmiştir.
	cut := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Text) != "" {
			cut = i
			break
		}
	}
	var rows []string
	for i := cut - 1; i >= 0 && len(rows) < drawerHistoryTurns; i-- {
		txt := strings.TrimSpace(msgs[i].Text)
		if txt == "" {
			continue
		}
		tag := "C: "
		if msgs[i].Role == "user" {
			tag = "K: "
		}
		rows = append(rows, tag+txt)
	}
	if len(rows) == 0 {
		return ""
	}
	for l, r := 0, len(rows)-1; l < r; l, r = l+1, r-1 {
		rows[l], rows[r] = rows[r], rows[l]
	}
	return strings.Join(rows, "\n")
}

// drawerNarrationUser — explain-grounded yolun user bloğu. evidence
// BOŞKEN üretilen metin v0.9.479'dakiyle bayt-bayt aynıdır (regresyon
// testi pinler) — kanıt fetch'i başarısız olduğunda düşülen yol budur.
// Sıra bilinçli: açıklama → ham kanıt → konuşma → SORU en sonda (küçük
// model son talimatı en iyi izler). Saf; tablo-testli.
func drawerNarrationUser(question, explain string, msgs []copilot.ChatMessage, evidence string) string {
	var b strings.Builder
	b.WriteString("EKRANDAKİ AÇIKLAMA (operatörün az önce okuduğu CoSRE cevabı):\n")
	b.WriteString(clampDrawerExplain(explain))
	if ev := strings.TrimSpace(evidence); ev != "" {
		b.WriteString("\n\n")
		b.WriteString(drawerEvidenceHeader)
		b.WriteString(ev)
	}
	if h := drawerHistoryBlock(msgs); h != "" {
		b.WriteString("\n\nKONUŞMA (K: operatör, C: sen):\n")
		b.WriteString(h)
	}
	b.WriteString("\n\nSORU: ")
	b.WriteString(question)
	return b.String()
}

// copilotChatDrawer — çekmece sohbetinin tek-çağrılı cevabı. Guided
// almadıysa ve explain bağlamı varsa çağrılır; handled=true dönerse
// exchange tamamlanmıştır (RAG ve serbest tool döngüsü çalışmaz).
// ai_calls satırı "chat-drawer" yüzeyiyle düşer — /ai sayfası çekmece
// sohbetinin kalitesini guided'dan ayrı izleyebilsin.
//
// v0.9.482: subject (frontend `?ai=` kodeği) doluysa ilgili explain'in
// kanıt paketi yeniden kurulur ve anlatıma girer. Kanıt YOKSA/çekilemezse
// akış v0.9.479'daki metin-tabanlı anlatımdır — soft-fail.
func (s *Server) copilotChatDrawer(ctx context.Context, emit func(string, any), msgs []copilot.ChatMessage, explain, subject, ctxService string) (handled, ok bool) {
	ex := clampDrawerExplain(explain)
	question := strings.TrimSpace(lastUserText(msgs))
	if ex == "" || question == "" {
		return false, false
	}
	// Şeffaflık: guided'ın "bağlam: … (önceki sorudan)" çipiyle aynı dil.
	emitGuidedStep(emit, "bağlam: ekrandaki AI açıklaması", "")

	// Kanıt: operatör SORU SORDUĞU için çekilir (v0.9.166 disiplini),
	// soru başına tek pass. Çekilemezse çip de düşmez, akış bozulmaz.
	evidence, evidenceKind := "", ""
	if subj, sok := parseDrawerSubject(subject); sok {
		if evidence = s.drawerSubjectEvidence(ctx, subj); evidence != "" {
			evidenceKind = subj.Kind
			if step := drawerEvidenceStep(subj.Kind); step != "" {
				emitGuidedStep(emit, step, "")
			}
		}
	}

	raw, err := s.copilotStreamSurface(ctx, "chat-drawer", copilot.SystemPromptDrawerChat(),
		drawerNarrationUser(question, ex, msgs, evidence), func(delta string) {
			emit("delta", map[string]string{"text": delta})
		})
	if err != nil {
		emit("error", map[string]string{"error": err.Error()})
		return true, false
	}
	// Deterministik kaynak dipnotu — guided yoldaki "Kaynak: …" ile aynı
	// sözleşme; modele bırakılmaz.
	// v0.9.1140 (operator-reported): log-köprüsü çipleri yalnız guided
	// + serbest döngüde vardı — drawer cevabındaki request_id LINKSİZ
	// kalıyordu (CoSRE id'yi buluyor, Settings şablonu doğru, hat yok).
	// ctxService ortam şablonunu seçer (-int/-uat/-prep eki).
	emit("answer", map[string]any{
		"text":       strings.TrimSpace(raw) + drawerSourceNote(evidenceKind),
		"exchangeId": copilot.MetaFromContext(ctx).ExchangeID,
		"links":      s.answerRequestIDLinks(ctx, raw, ctxService),
	})
	return true, true
}
