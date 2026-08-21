package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/insight"
	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// insight.go — gömülü insight kartının sunucu ucu (AI Assistant Faz 2.1,
// v0.9.1129; docs/plans/ai-assistant-design-2026-08-16.md §2.3 + onaylı
// mockup).
//
//	GET /api/insight/{kind}/{id}[?stream=1]
//
// Bu dosya KOMPOZİSYON KÖKÜ: chstore okumaları burada, saf projeksiyon
// internal/ai/insight'ta, prompt'un sistem yarısı internal/copilot'ta.
// Üçü ayrı çünkü kartın sözleşmesi "deterministik yarı LLM'siz de
// doğru" — o yarıyı test edebilmek için store'suz olması gerekiyor.
//
// ── NEDEN /api/copilot/ DEĞİL (namespace kararı) ────────────────────
//
// Bu, AI KAPALIYKEN de anlamlı cevap veren TEK AI-komşusu uç. Sinyaller
// ve linkler deterministik; prose opsiyonel bir katman. requireCopilot
// (ai_routes.go) 503 döndürür — bu uçta 503 YANLIŞ cevap olurdu: kart
// yapılandırılmamış kurulumda da tam değerli çizilmeli.
//
// /api/copilot/ altına GET olarak koymak iki kötü seçenek doğuruyordu:
//
//  1. requireCopilot ile sar → AI-off yarısı ölür (mockup sözleşmesi
//     kırılır);
//  2. sarmadan koy → ai_routes_test.go'nun kaynak-pini yalnız
//     `POST /api/copilot/` satırlarını tarıyor, yani "copilot
//     namespace'inde kapısız uç" diye GÖRÜNMEZ bir ikinci sınıf doğar.
//     v0.9.1071/1080/1101 tam olarak "kapısız yeni uç" sınıfıydı;
//     testin göremediği bir istisna açmak o sınıfı geri getirirdi.
//
// Üçüncü yol seçildi: uç KENDİ namespace'inde (/api/insight/) yaşıyor,
// böylece "/api/copilot/ altındaki her şey LLM ister" kuralı İSTİSNASIZ
// kalıyor. Karşılığında yeni bir pin eklendi
// (TestInsightRouteNotCopilotGated): bu route requireCopilot ile
// sarılırsa test kırmızı yanar — yani gelecekte biri "eksik kapı"
// sanıp sarmaya kalkarsa gerekçeyi okumak zorunda kalır.
//
// ai_calls yüzey etiketi: aiSurfaceFromPath path'ten
// "insight-exception" / "insight-problem" türetir (whitelist'li, /ai
// kırılımı sonlu kalsın).
//
// ── Yetki ───────────────────────────────────────────────────────────
//
// Kartın projelediği veri (problem, exception grubu) bugün viewer'a
// AÇIK; kart o veriyi yeniden çerçeveliyor, yeni bir şey sızdırmıyor.
// Dolayısıyla rol kapısı YOK (kimlik kapısı auth middleware'inde,
// SkipPath'te değil). Yazma yok → audit yok.
//
// ── Önbellek ────────────────────────────────────────────────────────
//
// s.serveCached YOK ve bu bilinçli: (a) SSE gövdesi önbelleklenemez,
// (b) uç yalnız kart AÇILDIĞINDA ateşlenir (collapsed hâl worker'ın
// pasif özetini gösterir, sıfır fetch), (c) tüm explain yüzeylerinin
// sözleşmesi "her tıkta taze". Kanıt okumaları bounded: exception
// yolunda BuildExceptionExplainInput'un bugünkü tık maliyeti, problem
// yolunda rootcause fan-out'unun ALT KÜMESİ (4 okuma, paralel,
// soft-fail). FE tarafı ES-maliyet disiplinini staleTime ile 2.2'de
// kuruyor.

// getInsight — tür ayrıştırıcı. Bilinmeyen tür 404: LLM'e ulaşmadan,
// ai_calls yüzeyi kirlenmeden.
func (s *Server) getInsight(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.PathValue("kind"))
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "insight id required", http.StatusBadRequest)
		return
	}
	if !insight.KnownKind(kind) {
		http.Error(w, "unknown insight kind", http.StatusNotFound)
		return
	}
	switch kind {
	case insight.KindException:
		s.insightException(w, r, id)
	case insight.KindProblem:
		s.insightProblem(w, r, id)
	case insight.KindLogPattern:
		s.insightLogPattern(w, r, id)
	case insight.KindSlowQuery:
		s.insightSlowQuery(w, r, id)
	}
}

// ── exception türü ──────────────────────────────────────────────────

// insightException — id = exception grubunun fingerprint'i.
//
// Kanıt: anomaly.BuildExceptionExplainInput — copilotExplainException'ın
// ve proaktif ExceptionExplainer işçisinin kullandığı AYNI kurucu. İkinci
// bir prefetch yazmadık; kart o girdinin yapısal yarısını (v0.9.1129'da
// eklenen TraceID/Trend/Deploys) projeliyor.
func (s *Server) insightException(w http.ResponseWriter, r *http.Request, fp string) {
	g, err := s.store.GetExceptionGroup(r.Context(), fp)
	if err != nil {
		writeErr(w, err)
		return
	}
	if g == nil {
		http.Error(w, "exception group not found", http.StatusNotFound)
		return
	}
	in := anomaly.BuildExceptionExplainInput(r.Context(), s.store, s.logs, g)
	ev := exceptionEvidence(g, in, time.Now().UnixNano())

	resp := insight.Response{Charts: insight.ExceptionCharts(ev), Links: insight.ExceptionLinks(ev)}
	resp.Signals, resp.Truncated = insight.ExceptionSignals(ev)
	s.deliverInsight(w, r, resp, copilot.SystemPromptException(), in.User)
}

// exceptionEvidence — chstore/anomaly → saf kanıt dönüşümü. SAF
// (nowNs parametre) ki testte sabit bir "şimdi" ile pinlenebilsin.
func exceptionEvidence(g *chstore.ExceptionGroup, in anomaly.ExceptionExplainInput, nowNs int64) insight.ExceptionEvidence {
	ev := insight.ExceptionEvidence{
		Fingerprint: g.Fingerprint,
		Type:        g.Type,
		Service:     g.Service,
		State:       g.State,
		Occurrences: g.Occurrences,
		FirstSeenNs: g.FirstSeen,
		LastSeenNs:  g.LastSeen,
		TraceID:     in.TraceID,
		NowNs:       nowNs,
	}
	if t := in.Trend; t != nil {
		ev.Last24, ev.PeakCount = t.Last24, t.Peak
		// Trend toplamı occurrence kovalarından gelir ve grubun
		// sayacından SAPABİLİR (kovalar (service,type) çözünürlüğünde —
		// GetExceptionOccurrences'ın kendi dürüstlük notu). Grubun
		// kendi sayacı otoritedir; trend yalnız kırılımı verir.
		if ev.Occurrences == 0 {
			ev.Occurrences = t.Total
		}
	}
	for _, d := range in.Deploys {
		ev.Deploys = append(ev.Deploys, insight.DeployCandidate{
			Version: d.Version, OffsetSec: d.OffsetSec, After: d.After})
	}
	return ev
}

// ── problem türü ────────────────────────────────────────────────────

// insightProblem — id = problem id.
//
// Kanıt: rootcause.go'nun ÇAĞIRDIĞI store metotlarının alt kümesi —
// deploy zenginleştirmesi, kalıcı hipotez, blast radius. Pencere aynı
// boundAnalysisWindow zarfı ([10dk, 1h]) ki kartın linkleri
// /rootcause panelinin gösterdiği pencereyle ÇELİŞMESİN.
//
// Bilinçli DIŞARIDA: GetCorrelatedChangesMV / BubbleUp / FindExemplar.
// Korelasyon listesinin karta giren yüzü hipotezin Candidates'ı
// (worker zaten hesaplamış, okuma bedava); bubble-up ve exemplar
// /rootcause panelinin işi — kart onları tekrar hesaplayıp tık başına
// iki okuma daha ödemez.
func (s *Server) insightProblem(w http.ResponseWriter, r *http.Request, id string) {
	p, err := s.store.GetProblem(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if p == nil {
		http.Error(w, "problem not found", http.StatusNotFound)
		return
	}

	// Saat BİR kez okunuyor: pencere sonu ile sinyallerin "şimdi"si
	// aynı an olmalı, yoksa "açık · 4sa" satırı pencereden birkaç ms
	// sapar ve iki damga arasındaki fark açıklanamaz hâle gelir.
	now := time.Now()
	started := time.Unix(0, p.StartedAt)
	end := now
	if p.ResolvedAt != nil {
		end = time.Unix(0, *p.ResolvedAt)
	}
	started, end = boundAnalysisWindow(started, end)

	// Deploy + hipotez + blast PARALEL, her biri soft-fail: eksik bir
	// kanıt satırı kartı düşürmez (rootcause fan-out'unun sözleşmesi).
	// Goroutine'ler AYRIK değişkenlere yazıyor ve okuma wg.Wait()
	// sonrasında — kilide gerek yok (rootcause.go'nun aynı gerekçesi).
	var (
		wg    sync.WaitGroup
		hyp   *chstore.RootCauseHypothesis
		blast *chstore.BlastRadius
	)
	enriched := *p
	wg.Add(3)
	go func() {
		defer wg.Done()
		if enr := s.store.EnrichProblemsWithDeploys(r.Context(), []chstore.Problem{*p}, 30*time.Minute); len(enr) == 1 {
			enriched = enr[0]
		}
	}()
	go func() {
		defer wg.Done()
		if h, e := s.store.GetHypothesis(r.Context(), "problem", p.ID); e == nil && h != nil {
			hyp = h
		}
	}()
	go func() {
		defer wg.Done()
		if br, e := s.store.GetServiceBlastRadius(r.Context(), p.Service, started, end); e == nil {
			blast = &br
		}
	}()
	wg.Wait()

	ev := problemEvidence(&enriched, hyp, blast, started.UnixNano(), end.UnixNano(), now.UnixNano())
	resp := insight.Response{Charts: insight.ProblemCharts(ev), Links: insight.ProblemLinks(ev)}
	resp.Signals, resp.Truncated = insight.ProblemSignals(ev)
	// v0.9.1207 (Faz 6.3) — HAZIR verdict'i tüket, ÜRETME. Bir operatör
	// son ~30 dk içinde ✨ Explain tıkladıysa kalkanlı verdict
	// rootcause-explain önbelleğinde duruyor; kart onu bedavaya taşır
	// (kanıt-ID'li anlatı karta iner). Miss = sessiz yokluk — A2 kararı
	// (kart otomatik LLM ateşlemez) aynen geçerli; verdict'in kendi
	// LLM'i yalnız ✨ Explain yolunda çağrılır.
	if hyp != nil {
		resp.Verdict = s.peekRCAVerdict(r.Context(), "problem", p.ID, hyp.Version)
	}
	s.deliverInsight(w, r, resp,
		copilot.SystemPromptProblem(),
		insight.ProblemPromptUser(ev, anomaly.HypothesisPromptBlockTR(hyp)))
}

// peekRCAVerdict — rootcause-explain önbelleğindeki gövdeden verdict'in
// HAM JSON'unu çıkarır; yoksa nil. Anahtar rootcause.go:525 ile AYNI
// biçim (kind:id:version — sürüm anahtarda olduğu için bayat hipotezin
// verdict'i asla dönmez).
// rootcauseExplainCacheKey — TEK tanım: hem serveCached yazan taraf
// (rootcause.go) hem peek okuyan taraf bunu kullanır. İki elle kurulan
// anahtar bir gün sessizce ayrışır ve peek sonsuza dek miss olurdu.
func rootcauseExplainCacheKey(anchorKind, id string, hypVersion uint64) string {
	return fmt.Sprintf("rootcause-explain:%s:%s:%d", anchorKind, id, hypVersion)
}

func (s *Server) peekRCAVerdict(ctx context.Context, anchorKind, id string, hypVersion uint64) json.RawMessage {
	key := rootcauseExplainCacheKey(anchorKind, id, hypVersion)
	body, ok := s.cachePeek(ctx, key)
	if !ok {
		return nil
	}
	var envl struct {
		Verdict json.RawMessage `json:"verdict"`
	}
	if err := json.Unmarshal(body, &envl); err != nil || len(envl.Verdict) == 0 {
		return nil
	}
	return envl.Verdict
}

// problemEvidence — chstore → saf kanıt dönüşümü. SAF; hypothesis /
// blast nil olabilir.
func problemEvidence(p *chstore.Problem, hyp *chstore.RootCauseHypothesis,
	blast *chstore.BlastRadius, fromNs, toNs, nowNs int64) insight.ProblemEvidence {
	ev := insight.ProblemEvidence{
		ID: p.ID, Service: p.Service, Metric: p.Metric,
		Severity: p.Severity, Priority: p.Priority, PriorityReason: p.PriorityReason,
		Comparator: p.Comparator, Status: p.Status,
		Value: p.Value, Threshold: p.Threshold,
		StartedNs: p.StartedAt, FromNs: fromNs, ToNs: toNs, NowNs: nowNs,
	}
	if p.ResolvedAt != nil {
		ev.ResolvedNs = *p.ResolvedAt
	}
	// Deploy: problems zenginleştirmesi ÖNCE, hipotezin deploy'u SONRA —
	// hipotez yolu ölçülmüş etkiyi (Impact) taşıyan TEK yol (v0.9.1059),
	// o yüzden varsa o kazanır.
	dep := p.RecentDeploy
	if hyp != nil && hyp.RecentDeploy != nil {
		dep = hyp.RecentDeploy
	}
	if dep != nil && strings.TrimSpace(dep.Version) != "" {
		d := insight.DeployRef{Version: dep.Version, AgeSec: dep.AgeSeconds}
		if im := dep.Impact; im != nil {
			d.HasImpact = true
			d.P99DeltaPct = im.P99DeltaPct
			d.ErrDeltaPP = im.ErrorRateDeltaPct
		}
		ev.Deploy = &d
	}
	if hyp != nil && strings.TrimSpace(hyp.TopSuspect) != "" {
		h := insight.HypothesisRef{TopSuspect: hyp.TopSuspect, Confidence: hyp.Confidence}
		for _, c := range hyp.Candidates {
			if c.Service == "" || c.Service == hyp.TopSuspect {
				continue
			}
			h.Candidates = append(h.Candidates, c.Service)
		}
		// Others = top suspect DIŞINDAKİ aday sayısı. Kart listeyi
		// kırparken "+N"i bundan türetiyor; hipotezin tam sayısını
		// (suspect dahil) vermek N'i bir fazla gösterirdi.
		h.Others = len(h.Candidates)
		ev.Hyp = &h
		// DeepEvidence'ın en yavaş operasyonu: worker zaten hesapladı ve
		// bugüne kadar HİÇBİR yüzeyde görünmüyordu (tasarım §1.6).
		if hyp.Deep != nil && len(hyp.Deep.SlowOps) > 0 {
			op := hyp.Deep.SlowOps[0]
			ev.SlowOp = &insight.OpRef{Name: op.Name, P95Ms: op.P95Ms, ErrorRate: op.ErrorRate}
		}
	}
	if blast != nil {
		b := insight.BlastRef{
			TotalCallers: blast.TotalCallers, CascadingCallers: blast.CascadingCallers,
		}
		for _, c := range blast.Callers {
			if c.Service != "" {
				b.TopCallers = append(b.TopCallers, c.Service)
			}
		}
		ev.Blast = &b
	}
	return ev
}

// ── log-pattern türü (v0.9.1137, Faz 2.4) ───────────────────────────

// insightLogPattern — id = küratörlü desenin ADI ("Out of memory").
// Kimlik gerekçesi contract.go'da.
//
// Kanıt: anomaly.DetectLogPatterns — /api/anomalies/log-patterns'ın ve
// "Desenleri anlat" panelinin kullandığı AYNI okuma (tek batched
// _msearch / tek CH match+tokenbf turu). Kart o listeden KENDİ satırını
// seçiyor; ikinci bir sorgu şekli YOK.
//
// PENCERE — varsayılan 5dk ve bu bir seçim: satırların geldiği uç
// (useLogPatternAnomalies → /api/anomalies/log-patterns, param'sız)
// 5dk varsayılanıyla koşuyor. Kart 30dk'ya açılsa AYNI adı taşıyan ama
// BAŞKA sayıları olan bir olayı anlatırdı — operatör satırda "1.240",
// kartta "7.900" görürdü. ?window= verilirse snapAnomalyWindow'un
// rung'larına oturur (v0.8.270: ES anahtar kardinalitesi tavanlı).
//
// Maliyet: kart AÇILDIĞINDA bir okuma. Panel anlatıcısının (v0.9.1100)
// tık başına maliyetiyle AYNI sınıf; poll yok, prefetch yok.
func (s *Server) insightLogPattern(w http.ResponseWriter, r *http.Request, name string) {
	window := snapAnomalyWindow(parseDuration(r.URL.Query().Get("window"), 5*time.Minute))
	hits, err := anomaly.DetectLogPatterns(r.Context(), s.logs, window)
	if err != nil {
		writeErr(w, err)
		return
	}
	idx := -1
	for i := range hits {
		if hits[i].Pattern == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		// 404 ve mesaj AÇIK: "bulunamadı" değil "bu pencerede artık
		// tetiklenmiyor". Desen kataloğu sabit, tetikleme değil — kartı
		// paylaşılan bir linkten açan operatör farkı bilmeli.
		http.Error(w, "log pattern is not firing in this window", http.StatusNotFound)
		return
	}
	ev := logPatternEvidence(hits[idx], window, time.Now().UnixNano())
	resp := insight.Response{Links: insight.LogPatternLinks(ev)}
	resp.Signals, resp.Truncated = insight.LogPatternSignals(ev)
	s.deliverInsight(w, r, resp,
		copilot.SystemPromptLogPatterns(), insight.LogPatternPromptUser(ev))
}

// logPatternEvidence — detektör çıktısı → saf kanıt. SAF (nowNs
// parametre).
func logPatternEvidence(a anomaly.LogPatternAnomaly, window time.Duration, nowNs int64) insight.LogPatternEvidence {
	ev := insight.LogPatternEvidence{
		Pattern: a.Pattern, Kind: a.Kind,
		CurrentCount: a.CurrentCount, BaselineCount: a.BaselineCount,
		Ratio: a.Ratio, Service: a.Service, Sample: a.Sample,
		Tokens: a.Tokens, LastSeenNs: a.LastSeenNs,
		WindowSec: int64(window / time.Second), NowNs: nowNs,
	}
	for _, ts := range a.TopServices {
		ev.TopServices = append(ev.TopServices,
			insight.PatternServiceRef{Service: ts.Service, Count: ts.Count})
	}
	return ev
}

// ── slow-query türü (v0.9.1137, Faz 2.4) ────────────────────────────

// insightStmtWindowDefault / min / max — yavaş sorgu kartının penceresi.
// Alt kenar MV'nin 5dk tanesi (daha darı yeni bir kova bile içermez),
// üst kenar 7g (chart bandının tavanıyla aynı sayı). Varsayılan 1sa =
// katalog sayfasının varsayılan aralığı.
const (
	insightStmtWindowDefault = time.Hour
	insightStmtWindowMin     = 5 * time.Minute
	insightStmtWindowMax     = 7 * 24 * time.Hour
)

// boundInsightStmtWindow — pencere kelepçesi. SAF, tablo-testli: birim
// karıştırma sınıfının (v0.6.36) kuralı gereği HER birim ("300s", "5m",
// "1h", "168h") testte. Go'nun time.ParseDuration'ı "7d" TANIMAZ —
// gün penceresi saat olarak yazılır, yoksa parseDuration sessizce
// varsayılana düşer (bu depoda iki kez oldu).
func boundInsightStmtWindow(d time.Duration) time.Duration {
	if d < insightStmtWindowMin {
		return insightStmtWindowMin
	}
	if d > insightStmtWindowMax {
		return insightStmtWindowMax
	}
	return d
}

// insightSlowQuery — id = `?stmt=` kodeği ("<hash>[|<system>]").
//
// Kanıt: db_statement_summary_5m MV'si — /api/databases/statements/
// detail'in KULLANDIĞI okuyucuların alt kümesi (özet + çağıran kırılımı
// + MV-gömülü exemplar, v0.9.1097). Ham `spans` taraması YOK.
//
// Bilinçli DIŞARIDA: trend serisi (GetDBStmtTrend) ve prior-pencere
// karşılaştırması. Trend kartta ÇİZİLEMİYOR (charts[] FE'de henüz
// çizilmiyor — CosreChart penceresini "şimdi"ye çakıyor, InsightCard.tsx
// gerekçesi) ve prior okuması MV maliyetini İKİYE katlıyor; ikisi de
// çekmecenin işi (o zaten bu sayfada bir tık uzakta ve kart ona link
// veriyor).
func (s *Server) insightSlowQuery(w http.ResponseWriter, r *http.Request, id string) {
	ref, ok := insight.ParseStmtRef(id)
	if !ok {
		http.Error(w, "slow-query id must be <stmtHash>[|<dbSystem>] "+
			"(decimal, non-zero — SlowQueryRow.stmtHash)", http.StatusBadRequest)
		return
	}
	window := boundInsightStmtWindow(parseDuration(r.URL.Query().Get("window"),
		insightStmtWindowDefault))
	now := time.Now()
	from := now.Add(-window)
	q := chstore.DBStmtDetailQuery{
		Hash: ref.Hash, DBSystem: ref.System, From: from, To: now,
	}

	// Üç okuma PARALEL, her biri soft-fail (dbstmt_detail.go'nun aynı
	// bölüm-toleransı). Goroutine'ler AYRIK değişkenlere yazıyor ve okuma
	// wg.Wait() sonrasında — kilide gerek yok.
	var (
		wg              sync.WaitGroup
		sum             *chstore.DBStmtSummary
		callers         []chstore.DBStmtCaller
		slowTID, errTID string
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		if v, err := s.store.GetDBStmtSummary(r.Context(), q); err == nil {
			sum = v
		}
	}()
	go func() {
		defer wg.Done()
		if v, err := s.store.GetDBStmtCallers(r.Context(), q, insightStmtCallerLimit); err == nil {
			callers = v
		}
	}()
	go func() {
		defer wg.Done()
		if a, b, err := s.store.DBStmtExemplars(r.Context(), q); err == nil {
			slowTID, errTID = a, b
		}
	}()
	wg.Wait()

	if sum == nil {
		// Özet yoksa sınıfın bu pencerede HİÇ satırı yok (GROUP BY'sız
		// toplamda cnt==0 = "bulunamadı" sinyali). Çağıran/exemplar da
		// boş olur; kanıtsız bir kart çizdirmek yerine 404.
		http.Error(w, "slow query class has no rows in this window", http.StatusNotFound)
		return
	}

	ev := slowQueryEvidence(ref, sum, callers, slowTID, errTID,
		from.UnixNano(), now.UnixNano(), now.UnixNano())
	resp := insight.Response{Links: insight.SlowQueryLinks(ev)}
	resp.Signals, resp.Truncated = insight.SlowQuerySignals(ev)
	s.deliverInsight(w, r, resp,
		copilot.SystemPromptSlowQuery(), insight.SlowQueryPromptUser(ev))
}

// insightStmtCallerLimit — çağıran okumasının tavanı. Kart üç ad
// gösteriyor (maxListedNames); tavan biraz daha yüksek ki "+N" GERÇEK
// bir fazlalığı göstersin, ve tavana dayanmak Truncated'ı kaldırsın.
// Çekmece 20 okuyor — kart onun beşte biriyle yetiniyor.
const insightStmtCallerLimit = 6

// slowQueryEvidence — chstore → saf kanıt dönüşümü. SAF.
func slowQueryEvidence(ref insight.StmtRef, sum *chstore.DBStmtSummary,
	callers []chstore.DBStmtCaller, slowTID, errTID string,
	fromNs, toNs, nowNs int64) insight.SlowQueryEvidence {

	ev := insight.SlowQueryEvidence{
		StmtParam:    ref.Param,
		SlowTraceID:  slowTID,
		ErrorTraceID: errTID,
		FromNs:       fromNs, ToNs: toNs, NowNs: nowNs,
	}
	if sum != nil {
		// Görünüm formu ÖZETİN kendi bucket örneğinden türüyor
		// (NormalizeDBStatement) — çekmecenin yaptığının aynısı, yani
		// kart ile çekmece aynı metni gösterir ve ikisi de hash'le
		// tutarlıdır (dbstmt.go parite sözleşmesi).
		ev.Statement = chstore.NormalizeDBStatement(sum.SampleStatement)
		ev.Sample = sum.SampleStatement
		ev.DBSystem, ev.DBName = sum.DBSystem, sum.DBName
		ev.Calls, ev.Errors = sum.Calls, sum.Errors
		ev.TotalMs, ev.AvgMs = sum.TotalMs, sum.AvgMs
		ev.P95Ms, ev.P99Ms, ev.MaxMs = sum.P95Ms, sum.P99Ms, sum.MaxMs
	}
	// db_system kimlikte yoksa (motorlar arası katlanmış id) özetin
	// kendi değeri kullanılır; ref'teki varsa o otoritedir.
	if ref.System != "" {
		ev.DBSystem = ref.System
	}
	for _, c := range callers {
		if strings.TrimSpace(c.Service) == "" {
			continue
		}
		ev.Callers = append(ev.Callers, insight.CallerRef{
			Service: c.Service, Calls: c.Calls, P95Ms: c.P95Ms, TotalMs: c.TotalMs,
		})
	}
	ev.CallersCapped = len(callers) >= insightStmtCallerLimit
	return ev
}

// ── teslim ──────────────────────────────────────────────────────────

// deliverInsight — kart cevabının TEK çıkışı. deliverExplain'in
// kardeşi; SSE yazıcısı ORTAK (sseEmitter), ikinci bir yazıcı yok.
//
// Çerçeve sırası (akan kip):
//
//	signals{Response, prose:""}  ← DETERMİNİSTİK yarı, İLK
//	delta{text}*                 ← prose token token (AI aktifse)
//	answer{text, exchangeId}
//	done{ok}
//
// AI kapalı/yapılandırılmamışsa: signals → done{ok:true}. LLM'e çağrı
// YAPILMAZ (testte sahte sağlayıcının hiç çağrılmadığı assert edilir),
// cevap AIOff=true taşır ve exchangeId BOŞ kalır (oylanacak model
// cevabı yok).
//
// deliverExplain'den BİLİNÇLİ sapma: orada ilk bayt üretimden SONRA
// yazılır, böylece üretim hatası gerçek bir HTTP hatası olabiliyor.
// Burada `signals` ÖNCE düşüyor — kartın tabanı prose'u beklemez, zaten
// bütün fikir bu. Bedeli, akan kipte LLM hatasının artık statü koduyla
// değil `error` çerçevesiyle anlatılması. Buffered kip explain
// sözleşmesini korur (hata = HTTP hatası).
func (s *Server) deliverInsight(w http.ResponseWriter, r *http.Request,
	resp insight.Response, system, user string) {
	resp.Normalize()

	// AI kapısı BURADA, response ŞEKLİ olarak — 503 olarak DEĞİL
	// (namespace gerekçesi dosya başında). copilotReady tek yazılış:
	// ai_routes.go'daki requireCopilot da onu kullanır.
	var run explainRun
	if s.copilotReady() {
		r2, xid := withExchange(r)
		r = r2
		resp.ExchangeID = xid
		resp.Model = s.copilot.ActiveModel()
		run = s.explainPrompt(r, system, user)
	} else {
		resp.AIOff = true
	}

	em, canStream := newSSEEmitter(w)
	if !explainWantsStream(r) || !canStream {
		if run != nil {
			out, err := run(nil)
			if err != nil {
				writeErr(w, err)
				return
			}
			resp.Prose = out
		}
		writeJSON(w, resp)
		return
	}

	em.emit("signals", resp.WithoutProse())
	if run == nil {
		em.emit("done", map[string]bool{"ok": true})
		return
	}
	out, err := run(func(d string) {
		if d == "" {
			return
		}
		em.emit("delta", map[string]string{"text": d})
	})
	if err != nil {
		em.emit("error", map[string]string{"error": err.Error()})
		em.emit("done", map[string]bool{"ok": false})
		return
	}
	em.emit("answer", explainAnswerFrame(out, resp.ExchangeID, nil))
	em.emit("done", map[string]bool{"ok": true})
}
