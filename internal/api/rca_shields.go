// v0.9.559 — RCA verdict deterministik kalkanları.
//
// Tasarım: docs/cosre-verdict-design.md §4.
//
// Temel ilke: model ne söylerse söylesin, SUNUCU doğrular. Şema
// biçimi zorlar, kalkanlar içeriği. Her kalkanın ne yakaladığı kadar
// NE YAKALAMADIĞI da yazılı — bir kalkanın sınırını bilmemek,
// olmamasından tehlikelidir.
package api

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// rcaShieldReport — kalkanların ne yaptığının kaydı. Yanıtta döner:
// operatör "bu verdict neye rağmen üretildi"yi görebilmeli.
type rcaShieldReport struct {
	// Parsed — model çıktısı şemaya uydu mu. false ⇒ deterministik
	// düşüşe geçildi ve bu UI'DA GÖSTERİLMELİ; yoksa sahte bir
	// "kanıt yetersiz" gerçek kanıt yokluğundan ayırt edilemez.
	Parsed bool `json:"parsed"`
	// Repaired — bir onarım turu gerekti mi (kalite sinyali).
	Repaired bool `json:"repaired,omitempty"`
	// RejectedEvidence — katalogda olmayan ya da YANLIŞ UZAYDA olan
	// kanıt kimlikleri (K2).
	RejectedEvidence []string `json:"rejectedEvidence,omitempty"`
	// UnknownEntities — serbest metinde geçen, hiçbir yerde olmayan
	// servis-biçimli adlar (K3).
	UnknownEntities []string `json:"unknownEntities,omitempty"`
	// RefutationInvalid — eleme geçersiz sayıldı mı (K4).
	RefutationInvalid bool `json:"refutationInvalid,omitempty"`
	// ConfidenceCapped — nihai güven modelin beyanının ALTINA çekildi mi.
	ConfidenceCapped bool `json:"confidenceCapped,omitempty"`
	// Notes — operatöre gösterilecek kısa açıklamalar.
	Notes []string `json:"notes,omitempty"`
}

func (r *rcaShieldReport) note(format string, a ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, a...))
}

// ── K2: kanıt kimliği doğrulama, iki uzaylı ──────────────────────────

// filterEvidenceIDs — bir DESTEK alanının kanıt listesini süzer.
//
// Yalnız katalogda VAR OLAN ve POZİTİF uzaydaki kimlikler kalır.
// Uydurulmuş kimlik ve N-kimliği düşürülür.
//
// Yakalamaz: gerçek bir E-kimliğinin ALAKASIZ bir iddiaya
// iliştirilmesi. Bunun mekanik çaresi yok; çare sunumda — sunucu kanıt
// metnini iddianın YANINA değil altına, "model şu kanıtı gösterdi"
// ayrımıyla basar ve modelin iddiasını kendi sesiyle onaylamaz.
func filterEvidenceIDs(cat rcaEvidenceCatalog, ids []string, sh *rcaShieldReport) []string {
	out := []string{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		ref, ok := cat.lookup(id)
		if !ok {
			sh.RejectedEvidence = append(sh.RejectedEvidence, id)
			sh.note("kanıt %s katalogda yok — iddia dayanaksız", id)
			continue
		}
		if ref.Kind == rcaNegative {
			sh.RejectedEvidence = append(sh.RejectedEvidence, id)
			sh.note("kanıt %s BULUNAMAYAN bir sinyal — kök nedene dayanak olamaz", id)
			continue
		}
		out = append(out, id)
	}
	return out
}

// filterRefutationIDs — ÇÜRÜTME kanıtlarını süzer. Burada N kabul
// edilir: bir hipotezi çürütmenin dayanağı pekâlâ bir yokluk olabilir
// ("trafik artışı değil, çünkü istek hacmi sabit").
func filterRefutationIDs(cat rcaEvidenceCatalog, ids []string, sh *rcaShieldReport) []string {
	out := []string{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := cat.lookup(id); !ok {
			sh.RejectedEvidence = append(sh.RejectedEvidence, id)
			sh.note("çürütme kanıtı %s katalogda yok", id)
			continue
		}
		out = append(out, id)
	}
	return out
}

// ── K4: elemecilik oyunlanamaz olmalı ────────────────────────────────

// refutationValid — bir elemenin geçerli sayılıp sayılmayacağı.
//
// İki koşul:
//  1. En az bir geçerli çürütme kanıtı olmalı.
//  2. Çürütme kanıtları, kök nedenin DESTEK kanıtlarıyla KESİŞMEMELİ.
//
// (2) şart çünkü aynı kanıt hem destek hem çürütme olamaz. Bu kural
// olmadan model tek gerçek kimliği hem "kök neden şu, kanıt E1" hem
// "rakip hipotez elendi, kanıt E1" diye kullanıp tavanı atlıyordu —
// yani kalkan bir DAVRANIŞ TEŞVİKİ üretiyordu.
func refutationValid(supportIDs, refuteIDs []string) bool {
	if len(refuteIDs) == 0 {
		return false
	}
	sup := map[string]bool{}
	for _, s := range supportIDs {
		sup[s] = true
	}
	for _, r := range refuteIDs {
		if sup[r] {
			return false
		}
	}
	return true
}

// ── Güven tavanı ─────────────────────────────────────────────────────

const (
	// rcaNoRefutationCap — hiç geçerli eleme yoksa tavan.
	rcaNoRefutationCap = 0.6
	// rcaHypothesisHeadroom — modelin deterministik motordan ne kadar
	// daha emin olabileceği.
	//
	// Model, korelasyon motorunun verdiğinden FAZLA kanıta sahip değil
	// (tool döngüsü yok — operatör kararı). Motordan belirgin şekilde
	// daha emin olması tanım gereği temelsizdir. 0.1 pay, anlatının
	// parçaları birleştirmesinden gelen küçük gerçek katkıyı bırakır.
	rcaHypothesisHeadroom = 0.1
)

// capRCAConfidence — nihai güveni hesaplar.
//
// SAF ve tablo-testli. Üç kısıt birlikte:
//   - [0,1] aralığına kıstırılır (model aralık dışı beyan edebilir)
//   - eleme yoksa ≤ rcaNoRefutationCap
//   - her hâlükârda ≤ hipotez güveni + pay
func capRCAConfidence(model, hypothesis float64, hasValidRefutation bool, sh *rcaShieldReport) float64 {
	// NaN/Inf savunması: modelden gelen sayı doğrudan JSON'dan geliyor.
	if math.IsNaN(model) || math.IsInf(model, 0) {
		model = 0
		sh.note("model güveni sayı değil — 0 kabul edildi")
	}
	out := math.Min(1, math.Max(0, model))
	orig := out

	if !hasValidRefutation && out > rcaNoRefutationCap {
		out = rcaNoRefutationCap
		sh.note("geçerli eleme yok — güven %.2f'ye tavanlandı", rcaNoRefutationCap)
	}
	if h := math.Min(1, math.Max(0, hypothesis)) + rcaHypothesisHeadroom; out > h {
		out = math.Min(out, h)
		sh.note("güven, deterministik hipotezin üstüne %.2f'den fazla çıkamaz (%.2f)",
			rcaHypothesisHeadroom, out)
	}
	if out < orig {
		sh.ConfidenceCapped = true
	}
	return out
}

// ── Rakip hipotez listesi (sunucu üretir) ────────────────────────────

// rcaRivalClasses — sabit rakip sınıflar. Model bunları YAZMAZ, seçer.
//
// Serbest metin bırakmak, modelin hiç değerlendirilmemiş bir rakibi
// sahte bir gerekçeyle "elenmiş" göstermesine izin veriyordu. Enum,
// elemenin en azından GERÇEK bir alternatife dair olmasını garanti eder.
var rcaRivalClasses = []string{
	"yük artışı (trafik/istek hacmi)",
	"altyapı (node, ağ, disk)",
	"bağımlılık (dış servis / veritabanı)",
	"konfigürasyon değişikliği",
	"kaynak doygunluğu (havuz, thread, bellek)",
}

// buildRCARivalOptions — modele sunulacak rakip hipotez listesi.
//
// Hipotezin 2..N. adayları + sabit sınıflar. Baş şüpheli DIŞARIDA:
// o zaten önerilen kök neden, kendisini elemek anlamsız.
//
// Sıra deterministik (adaylar skor sırasında gelir, sınıflar sabit),
// çünkü liste şema enum'una gidiyor.
func buildRCARivalOptions(cat rcaEvidenceCatalog, topSuspect string, candidates []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || c == topSuspect || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, "kök neden "+c+" olabilir")
	}
	out = append(out, rcaRivalClasses...)
	return out
}

// ── K3: varlık doğrulama + serbest metin taraması ────────────────────

// checkRCAEntities — enum'la korunmayan TÜM serbest metni tarar.
//
// Enum kalkanı yalnız root_cause.entity'yi korur. Özet, nedensellik
// zinciri ve önerilen aksiyon serbest metindir ve operatör asıl onları
// okur — model beyaz listeden geçen gerçek bir varlığı entity alanına
// yazıp, zincirde ve aksiyonda HİÇ var olmayan bir servisi anlatabilir.
func checkRCAEntities(cat rcaEvidenceCatalog, texts []string, sh *rcaShieldReport) {
	known := lowerKnownSet(rcaAllowedEntities(cat)...)
	unknown := scanUnknownEntities(known, texts...)
	if len(unknown) == 0 {
		return
	}
	sort.Strings(unknown)
	sh.UnknownEntities = unknown
	sh.note("metinde veride olmayan ad(lar) geçiyor: %s — doğrulanamadı",
		strings.Join(unknown, ", "))
}
