package api

// devops_frames.go — stack frame → VCS derin linki (v0.10.581,
// Aşama 1.5).
//
// api.go'ya SATIR EKLENMEZ: kayıt route_registry.go defterine init()
// ile düşüyor (v0.10.247 yolu), yani api.go hiç büyümüyor.
//
//	POST /api/devops/stack-frames   {"service": "...", "stack": "..."}
//
// ROL KAPISI YOK — salt okuma, hiçbir şey yazmaz (dolayısıyla audit
// satırı da yok: audit yazma eylemlerinin izidir). Küresel JWT
// middleware'i kimliksiz isteği zaten 401 yapıyor ve viewer bu linki
// GÖRMELİ.
//
// ÜÇ SÖZLEŞME:
//
//  1. KOD GÖVDESİ DÖNMEZ. Yalnız künye + URL. copilot_code.go'nun
//     codePayload'ı da Content'i bilinçle kopyalamıyor ("kaynak modele
//     gider, tarayıcıya değil", lib/types.ts:1713); aynı duruş. Alan
//     seti devops_frames_test.go'da PİNLİ.
//  2. lineIndex ZORUNLU. Frontend ham stack'i kendi bölüp süsleyecek;
//     TypeScript tarafında İKİNCİ bir ayrıştırıcı yazılmayacak — tek
//     kanonik çözümleyici internal/stackparse.
//  3. YAPILANDIRILMAMIŞ = 200. "DevOps bağlantısı yok" operatörün
//     sorusuna verilmiş başarılı bir cevaptır; eşleme tanımlı değilse
//     frame'ler düz metin kalır, hata gösterilmez (test ucunun
//     {ok,error} duruşunun aynısı).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/devops"
	"github.com/cilcenk/coremetry/internal/stackparse"
)

func init() {
	registerRoutesExtra("devops_frames", (*Server).registerDevopsFrameRoutes)
}

func (s *Server) registerDevopsFrameRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/devops/stack-frames", s.postDevopsStackFrames)
}

const (
	// devopsStackMaxBytes — kabul edilen stack metninin tavanı.
	// 64 KB, en uzun JBoss yığın izinin bir mertebe üstünde; asıl işi
	// bir kopyala-yapıştır kazasının ayrıştırıcıyı meşgul etmesini
	// engellemek.
	devopsStackMaxBytes = 64 << 10
	// devopsStackMaxBody — gövde tavanı. Stack tavanının 4 katı:
	// JSON kaçışları (\n, \" ve çok baytlı karakterler) metni
	// büyütür, yani gövdeyi tam 64 KB'ta kesmek GEÇERLİ bir 64 KB'lık
	// stack'i reddederdi. Tavanı aşan gövde de 400'e çıkar (decode
	// hatası), yani sınır iki yerde de kapalı.
	devopsStackMaxBody = 4 * devopsStackMaxBytes
	// devopsFramesTTL — çözüm cache'i. Depo ağacı devops paketinde
	// zaten 10 dk cache'li; buradaki 5 dk, aynı çekmeceyi açıp kapatan
	// operatöre yol eşleşmesini de bedava veriyor.
	devopsFramesTTL = 5 * time.Minute
)

// devopsStackFramesInput — istek gövdesi.
type devopsStackFramesInput struct {
	Service string `json:"service"`
	Stack   string `json:"stack"`
}

// devopsFrameDTO — tek frame. omitempty YOK, bilinçli: frontend
// "alan yok" ile "alan boş" arasında ayrım yapmak zorunda kalmasın,
// ve alan seti testte pinlenebilsin.
type devopsFrameDTO struct {
	// LineIndex — ham stack'in KAÇINCI satırı (0 tabanlı, "\n" ile
	// bölünmüş). Frontend satırı bununla süsler.
	LineIndex int    `json:"lineIndex"`
	Class     string `json:"class"`
	Method    string `json:"method"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	// IsApp — EFEKTİF uygulama frame'i mi (operatörün AppPrefixes'i
	// sabit çerçeve listesini ezer). false = frontend soluk çizer.
	IsApp bool `json:"isApp"`
	Tier  int  `json:"tier"`
	// URL — boş = link yok, Reason nedenini söyler.
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// devopsStackFramesResponse — yanıt gövdesi. Kod GÖVDESİ taşıyan
// hiçbir alan yok ve olmayacak (test pinli).
type devopsStackFramesResponse struct {
	Configured bool   `json:"configured"`
	Repo       string `json:"repo,omitempty"`
	Project    string `json:"project,omitempty"`
	Branch     string `json:"branch,omitempty"`
	RepoSource string `json:"repoSource,omitempty"`
	// RevisionWarning — link üretildiği HER yanıtta dolu. Revizyon
	// çözümleme yok (kod branşın UCUndan geliyor, deploy edilen
	// sürümden değil), o yüzden uyarı istisna değil kural.
	RevisionWarning string           `json:"revisionWarning,omitempty"`
	Frames          []devopsFrameDTO `json:"frames"`
}

// devopsFramesKey — SAF cache anahtarı. TÜM girdiler ayrı ayrı FNV
// özetlenir; stack'in UZUNLUĞU değil İÇERİĞİ (v0.5.187 sınıfı).
//
// Dört bileşen dört ayrı digest: tek bir birleştirilmiş dizede
// "a"+"bc" ile "ab"+"c" aynı anahtara düşerdi.
//
// branch anahtarda YOK ve olamaz: branş bir ÇIKTI (refs API'sinden
// gelir), girdi değil. Onu belirleyen girdi — BranchOrder — cfgDigest
// içinde taşınıyor, yani ayar değişince anahtar da değişir.
func devopsFramesKey(service, repo, cfgDigest, stack string) string {
	return fmt.Sprintf("devops:frames:svc=%s:repo=%s:cfg=%s:st=%s",
		fnvStr(service), fnvStr(repo), fnvStr(cfgDigest), fnvStr(stack))
}

// devopsSettingsDigest — cevabı değiştirebilecek TÜM DevOps ayarları.
// SAF; PAT'a dokunmaz (Snapshot zaten taşımıyor).
//
// Dilimlerin elemanları AYRI parça olarak yazılır ve aralarına
// kontrol karakterli işaretçi konur: {"a","b"} ile {"a\x00b"} aynı
// özete düşmesin.
func devopsSettingsDigest(sn devops.Snapshot) string {
	parts := []string{sn.BaseURL, sn.Collection, sn.Project, "\x01rp"}
	parts = append(parts, sn.RepoPrefixes...)
	parts = append(parts, "\x01bo")
	parts = append(parts, sn.BranchOrder...)
	parts = append(parts, "\x01ap")
	parts = append(parts, sn.AppPrefixes...)
	return fnvStr(parts...)
}

// revisionWarningText — SAF. Branş boşsa deponun varsayılanı
// kullanılmıştır (resolveBranch sözleşmesi); "" yazmak yerine ne
// olduğunu söyle.
func revisionWarningText(branch string) string {
	b := strings.TrimSpace(branch)
	if b == "" {
		b = "deponun varsayılan"
	}
	return "Bağlantılar " + b + " branşının ucuna gider; olay anındaki " +
		"sürüm doğrulanamıyor, satır numaraları kaymış olabilir."
}

// parseStackFrames — SAF: kanonik çözümleyici + her frame'in HAM
// SATIR İNDEKSİ. İki dilim aynı uzunlukta.
//
// ⛔ İKİNCİ AYRIŞTIRICI YOK. Satır indeksi, aynı ParseJava'yı satır
// satır koşturarak bulunuyor: ParseJava girdiyi "\n" ile bölüp tanınan
// her satır için tam bir frame üretiyor, yani tek satırlık çağrı
// "bu satır bir frame mi" sorusunun TA KENDİSİ. Kural bir kopyaya
// yazılsaydı (regex, "at " öneki), ilk düzeltmede ayrışır ve frontend
// yanlış satırı süslerdi.
//
// Segment damgası tam metin çağrısından gelir (satır satır koşan
// çağrı "Caused by:" sayacını göremez) — o yüzden frame'lerin kendisi
// tek seferlik ParseJava(stack)'ten alınır.
func parseStackFrames(stack string) ([]stackparse.Frame, []int) {
	frames := stackparse.ParseJava(stack)
	idx := make([]int, len(frames))
	for i := range idx {
		idx[i] = -1
	}
	n := 0
	for i, ln := range strings.Split(stack, "\n") {
		if n >= len(frames) {
			break
		}
		if len(stackparse.ParseJava(ln)) == 1 {
			idx[n] = i
			n++
		}
	}
	return frames, idx
}

// stackFramesPayload — SAF: ham stack + çözüm → yanıt gövdesi.
//
// resolve bir CALLBACK: ağ tarafı (devops.Service) testte yerine
// konabilsin ve gövde kurgusu ağsız pinlensin. Ayrıca stack YALNIZ
// BİR KEZ ayrıştırılır — çözücü de aynı frame dilimini alır.
func stackFramesPayload(stack string, resolve func([]stackparse.Frame) devops.FrameLinks) devopsStackFramesResponse {
	frames, idx := parseStackFrames(stack)
	links := resolve(frames)
	out := devopsStackFramesResponse{
		Configured: links.Configured,
		Frames:     []devopsFrameDTO{},
	}
	if !links.Configured {
		// Sözleşme: yapılandırılmamışsa frames BOŞ. Frame künyesini
		// yine de göndermek, frontend'i "link gelecek mi" belirsizliğine
		// sokardı; düz metin kalması gereken hâlin tek işareti bu.
		return out
	}
	out.Repo, out.Project = links.Repo, links.Project
	out.Branch, out.RepoSource = links.Branch, links.RepoSource
	hasURL := false
	for i, f := range frames {
		d := devopsFrameDTO{
			LineIndex: idx[i], Class: f.Class, Method: f.Method,
			File: f.File, Line: f.Line,
			// Hizasız çözüm (sözleşme ihlali) sessiz bir yalan
			// üretmesin: frame'in KENDİ IsApp'ine düş, link verme.
			IsApp: f.IsApp, Tier: 1,
		}
		if i < len(links.Links) {
			l := links.Links[i]
			d.IsApp, d.Tier, d.URL, d.Reason = l.App, l.Tier, l.URL, l.Reason
		}
		if d.URL != "" {
			hasURL = true
		}
		out.Frames = append(out.Frames, d)
	}
	if hasURL {
		out.RevisionWarning = revisionWarningText(out.Branch)
	}
	return out
}

// postDevopsStackFrames — stack metni → frame künyeleri + derin
// linkler.
func (s *Server) postDevopsStackFrames(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, devopsStackMaxBody)
	var in devopsStackFramesInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz gövde: "+err.Error())
		return
	}
	service := strings.TrimSpace(in.Service)
	stack := in.Stack
	if strings.TrimSpace(stack) == "" {
		writeJSONError(w, http.StatusBadRequest, "stack parametresi zorunlu")
		return
	}
	if len(stack) > devopsStackMaxBytes {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("stack çok uzun (%d bayt, tavan %d)", len(stack), devopsStackMaxBytes))
		return
	}

	// Yapılandırılmamış: cache'e, kataloğa, ağa HİÇ dokunma.
	if s.devops == nil || !s.devops.Configured() {
		writeJSON(w, devopsStackFramesResponse{Frames: []devopsFrameDTO{}})
		return
	}

	// Katalog pini — gerçek kod yolundaki okumanın AYNISI
	// (copilot_code.go/buildCodeContext), pinReadDecision dahil:
	// okuma hatası fail-CLOSED, çünkü yanlış depoya link vermek link
	// vermemekten kötü.
	//
	// serveCached'in DIŞINDA, bilinçli: pin bir GİRDİdir ve anahtara
	// girmesi gerekir. service_metadata FINAL okuması alt-ms; asıl
	// pahalı iş (depo ağacı, ağ) cache'in içinde kalıyor.
	var pin devops.PinRead
	if s.store != nil {
		md, err := s.store.GetServiceMetadataStrict(r.Context(), service)
		mdRepo := ""
		if md != nil {
			mdRepo = md.Repository
		}
		pin.Repo, pin.Abort = pinReadDecision(mdRepo, md != nil, err)
	}
	repo := devops.ResolveRepo(service, pin.Repo, s.devops.ResolveConfig()).Repo
	key := devopsFramesKey(service, repo, devopsSettingsDigest(s.devops.Snapshot()), stack)

	s.serveCached(w, r, key, devopsFramesTTL, func(ctx context.Context) (any, error) {
		// Kapalı gelen ctx KULLANILIR, isteğinki DEĞİL: SWR arka
		// plan tazelemesi kendi Background ctx'iyle koşar, closure
		// isteğin ctx'ine bağlanırsa her tazeleme context.Canceled
		// ile ölür (v0.8.319). Not: yazım da bilinçli — kapı
		// (servecached_ctx_test.go) closure gövdesindeki YORUMU da
		// tarıyor, o yüzden yasak çağrı burada adıyla anılmıyor.
		return stackFramesPayload(stack, func(frames []stackparse.Frame) devops.FrameLinks {
			return s.devops.ResolveFrameLinks(ctx, service, pin, frames)
		}), nil
	})
}
