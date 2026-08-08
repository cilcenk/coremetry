package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/devops"
	"github.com/cilcenk/coremetry/internal/stackparse"
)

// copilot_code.go — "Kodu da incele" (v0.9.831).
//
// Explain uçlarına opsiyonel `{"includeCode": true}` gövdesi geldiğinde
// bu dosya devreye girer: stacktrace → frame'ler → servis→depo çözümü →
// kaynak penceresi → prompt'a KOD BAĞLAMI bloğu. Sonra çağrı, kod
// gövdesini ai_calls kaydından MASKELEYEN sarmalayıcıdan geçer.
//
// Varsayılan KAPALI ve geriye uyumlu: gövdesiz POST (bugünkü tüm
// çağrılar) bayt-bayt eski prompt'u ve eski davranışı alır.

// explainOptions — Explain uçlarının opsiyonel gövdesi.
type explainOptions struct {
	IncludeCode bool `json:"includeCode"`
}

// decodeExplainOptions — gövdeyi okur. BOŞ GÖVDE GEÇERLİDİR: bu uçlar
// v0.9.408'den beri gövdesiz POST alıyor ve her mevcut çağıran öyle
// çağırıyor. Bozuk JSON de sessizce varsayılana düşer — bir açıklama
// isteğini gövde ayrıştırma hatasıyla 400'lemek, opsiyonel bir
// zenginleştirme uğruna çalışan bir yüzeyi kırmak olurdu.
func decodeExplainOptions(r *http.Request) explainOptions {
	var o explainOptions
	if r == nil || r.Body == nil {
		return o
	}
	// 4KB yeter: gövdede tek bir bayrak var.
	b, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return o
	}
	_ = json.Unmarshal(b, &o)
	return o
}

// buildCodeContext — servis + stacktrace'ten kaynak pencereleri.
//
// FAIL-OPEN her adımda: devops yok, katalogda depo yok, konvansiyon
// tutmadı, dosya ağaçta yok — hepsi boş bağlam + Reason. Açıklama
// yine üretilir, yalnızca kodsuz.
func (s *Server) buildCodeContext(ctx context.Context, service, stack string) devops.CodeContext {
	if s.devops == nil {
		return devops.CodeContext{Reason: "kod entegrasyonu yapılandırılmamış"}
	}
	if strings.TrimSpace(stack) == "" {
		return devops.CodeContext{Reason: "bu kayıtta stacktrace yok"}
	}
	// Katalog pini önce: elle girilen depo konvansiyonu EZER.
	repoPin := ""
	if service != "" {
		if md, err := s.store.GetServiceMetadata(ctx, service); err == nil && md != nil {
			repoPin = md.Repository
		}
	}
	res := devops.ResolveRepo(service, repoPin, s.devops.ResolveConfig())
	if res.Repo == "" {
		reason := res.Reason
		if reason == "" {
			reason = "servis için depo çözülemedi"
		}
		return devops.CodeContext{Reason: reason, Source: res.Source}
	}
	cc := s.devops.FetchCode(ctx, res.Repo, stackparse.ParseJava(stack))
	cc.Source = res.Source
	return cc
}

// codeContextPayload — yanıtın `code` alanı. Kod GÖVDESİ tarayıcıya
// gitmez; yalnız hangi depodan hangi dosyanın hangi aralığının
// okunduğu. Operatör kaynağı görmek isterse zaten DevOps'ta açar; kodu
// bir de API yanıtında taşımak, onu tarayıcı cache'ine ve HAR
// dökümlerine kopyalamak olurdu.
type codeContextPayload struct {
	Repo   string           `json:"repo,omitempty"`
	Branch string           `json:"branch,omitempty"`
	Source string           `json:"source,omitempty"` // pin | convention
	Files  []codeFileRefDTO `json:"files,omitempty"`
	Reason string           `json:"reason,omitempty"`
}

type codeFileRefDTO struct {
	Path     string `json:"path"`
	FromLine int    `json:"fromLine"`
	ToLine   int    `json:"toLine"`
}

// codePayload — CodeContext → wire. requested=false ise nil döner:
// kutuyu işaretlemeyen operatöre "kod yok" diye bir alan göstermek,
// olmayan bir arıza bildirmek olurdu.
func codePayload(cc devops.CodeContext, requested bool) *codeContextPayload {
	if !requested {
		return nil
	}
	p := &codeContextPayload{
		Repo: cc.Repo, Branch: cc.Branch, Source: cc.Source, Reason: cc.Reason,
	}
	for _, w := range cc.Windows {
		p.Files = append(p.Files, codeFileRefDTO{Path: w.Path, FromLine: w.FromLine, ToLine: w.ToLine})
	}
	return p
}

// isContextOverflowErr — sağlayıcı hatası "prompt bağlama sığmadı" mı?
// Saf; tablo-testli.
//
// İki koşul BİRLİKTE aranıyor: 400/413 sınıfı bir ret VE taşmaya işaret
// eden bir ifade. Yalnız "400" yetmez — response_format'ı reddeden bir
// 400 de vardır (copilot.go'daki mevcut JSON-mode merdiveni) ve onu
// taşma sanıp kodu yarıya indirmek yanlış teşhistir: cevap yine
// gelmez, üstüne bir çağrı daha yakılır.
func isContextOverflowErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "400") && !strings.Contains(msg, "413") {
		return false
	}
	for _, kw := range []string{
		"context length", "context_length", "context window", "maximum context",
		"too long", "too large", "exceed", "token limit", "tokens limit",
		"input length", "prompt is too", "reduce the length",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// copilotExplainCode — kod bağlamlı Explain çağrısı.
//
// İki iş yapıyor, ikisi de burada olmak zorunda:
//
//  1. MASKELEME. Sağlayıcıya TAM prompt (kod dahil) gider; ai_calls
//     kaydına kod bloğu yerine `[kod: repo/dosya:aralık · N satır]`
//     özeti yazılır. Kaynak kod telemetri deposuna yazılmaz.
//  2. TAŞMA YENİDEN DENEMESİ. Bağlam taşması 400'ünde kod bloğu YARIYA
//     indirilip BİR kez yeniden denenir. copilot.go'daki mevcut
//     400-merdivenine (JSON mode / response_format yoklaması)
//     DOKUNULMUYOR — o merdiven parametre reddini çözer, bu katman
//     prompt boyutunu; ikisi farklı sorun ve tek bir yerde
//     birleştirilirse hangi hipotezin denendiği okunamaz hale gelir.
//
// Kod bağlamı boşsa doğrudan kodsuz yola düşer (fail-open).
func (s *Server) copilotExplainCode(r *http.Request, systemNoCode, systemWithCode, user string, cc devops.CodeContext) (string, error) {
	block := cc.PromptBlock()
	if block == "" {
		return s.copilotExplain(r, systemNoCode, user)
	}
	out, err := s.explainWithCodeBlock(r, systemWithCode, user, block, cc.LogSummary())
	if err == nil || !isContextOverflowErr(err) {
		return out, err
	}
	half := cc.Halved()
	hb := half.PromptBlock()
	if hb == "" || hb == block {
		// Küçültecek bir şey kalmadı: kodsuz dene, cevapsız bırakma.
		return s.copilotExplain(r, systemNoCode, user)
	}
	return s.explainWithCodeBlock(r, systemWithCode, user, hb, half.LogSummary())
}

// explainWithCodeBlock — tek çağrı: gerçek prompt = user + block,
// log kopyası = user + summary.
func (s *Server) explainWithCodeBlock(r *http.Request, system, user, block, summary string) (string, error) {
	full := user + block
	return s.copilotExplainMasked(r, system, full, devops.MaskCodeInPrompt(full, block, summary))
}
