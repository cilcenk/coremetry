// v0.9.559 — RCA kanıt kataloğu: İKİ ayrı kimlik uzayı.
//
// Tasarım: docs/cosre-verdict-design.md §2.
//
// Model, ürettiği her iddiayı bir kanıt kimliğine bağlamak zorunda.
// Ama kanıtın iki TÜRÜ var ve ikisi aynı işi göremez:
//
//	E1..En  bulunmuş sinyaller     → kök neden dayanağı OLABİLİR
//	N1..Nn  bakılmış, BULUNAMAMIŞ  → yalnız ÇÜRÜTME dayanağı olabilir
//
// Asimetri mantıksal, keyfi değil: "trafik artışı değil, çünkü istek
// hacmi sabit" geçerli bir çürütmedir; "pod restart döngüsünde, çünkü
// restart kaydı BULUNAMADI" saçmadır. Aranmış ve bulunamamış olmak,
// olduğunun kanıtı değildir.
//
// Bu ayrım olmadan kalkan yalnız etkisiz kalmaz, ZARARLI olur: model
// negatif bir kaydı kanıt diye gösterir, kalkan geçer (kimlik gerçekten
// katalogdadır), sonra sunucu o katalog metnini iddianın yanına basar ve
// uydurma DOĞRULANMIŞ görünür. Kalkanın varlığı zararı artırır.
//
// Katalog kurulurken ayrım VERİDEN yapılır (CheckedSignal.Found),
// modelin beyanından değil.
package api

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// rcaEvidenceKind — kimlik uzayı.
type rcaEvidenceKind string

const (
	rcaPositive rcaEvidenceKind = "E"
	rcaNegative rcaEvidenceKind = "N"
)

// rcaEvidenceRef — katalogdaki tek kanıt satırı.
type rcaEvidenceRef struct {
	ID   string          `json:"id"`   // E3 / N1
	Kind rcaEvidenceKind `json:"kind"` // E | N
	// Entity — kanıtın AİT OLDUĞU varlık; boş olabilir (deploy gibi
	// servise bağlı olmayan sinyaller). K3 beyaz listesini besler.
	Entity string `json:"entity,omitempty"`
	// Text — operatöre gösterilecek metin. Sunucunun kendi cümlesi;
	// modelin ürettiği hiçbir şey buraya girmez.
	Text string `json:"text"`
}

// rcaEvidenceCatalog — bir ankor için kurulmuş tam katalog.
type rcaEvidenceCatalog struct {
	Refs []rcaEvidenceRef `json:"refs"`
	// byID — hızlı doğrulama için indeks (K2).
	byID map[string]rcaEvidenceRef
	// Entities — katalogdan türeyen varlık beyaz listesi (K3).
	// Burada olan bir ad, modelin uydurmadığı anlamına gelir.
	Entities map[string]bool `json:"-"`
}

// lookup — kimlikten kanıt. ok=false ⇒ katalogda YOK (uydurulmuş).
func (c rcaEvidenceCatalog) lookup(id string) (rcaEvidenceRef, bool) {
	r, ok := c.byID[strings.TrimSpace(id)]
	return r, ok
}

// positiveIDs / negativeIDs — prompt'a basılacak kimlik listeleri.
func (c rcaEvidenceCatalog) positiveIDs() []string { return c.idsOfKind(rcaPositive) }
func (c rcaEvidenceCatalog) negativeIDs() []string { return c.idsOfKind(rcaNegative) }

func (c rcaEvidenceCatalog) idsOfKind(k rcaEvidenceKind) []string {
	out := []string{}
	for _, r := range c.Refs {
		if r.Kind == k {
			out = append(out, r.ID)
		}
	}
	return out
}

// buildRCAEvidenceCatalog — hipotezden katalog kurar.
//
// SAF: hiçbir IO yok, tamamen tablo-testlenebilir. Bu bilinçli — kanıt
// ayrımı bu tasarımın tek en kritik parçası ve test edilemeyen bir
// yerde durmamalı.
//
// Sıra deterministik: aynı hipotez her çağrıda AYNI kimlikleri üretir.
// Aksi hâlde bir kimlik iki çağrı arasında başka bir kanıta kayar ve
// önbelleğe alınmış bir verdict sessizce yanlış kanıta atıf yapar.
func buildRCAEvidenceCatalog(h *chstore.RootCauseHypothesis) rcaEvidenceCatalog {
	cat := rcaEvidenceCatalog{
		byID:     map[string]rcaEvidenceRef{},
		Entities: map[string]bool{},
	}
	if h == nil {
		return cat
	}

	posN, negN := 0, 0
	addPos := func(entity, text string) {
		posN++
		ref := rcaEvidenceRef{ID: fmt.Sprintf("E%d", posN), Kind: rcaPositive, Entity: entity, Text: text}
		cat.Refs = append(cat.Refs, ref)
		cat.byID[ref.ID] = ref
		if entity != "" {
			cat.Entities[entity] = true
		}
	}
	addNeg := func(entity, text string) {
		negN++
		ref := rcaEvidenceRef{ID: fmt.Sprintf("N%d", negN), Kind: rcaNegative, Entity: entity, Text: text}
		cat.Refs = append(cat.Refs, ref)
		cat.byID[ref.ID] = ref
		// NEGATİF kanıt varlık beyaz listesini BESLEMEZ. "Şu serviste
		// pod kaydı bulunamadı" satırı o servisi meşru bir kök-neden
		// adayı yapmaz; yaparsa K3 kapısı N uzayının arka kapısına
		// dönerdi.
	}

	// Ankorun kendi servisi her zaman meşru bir varlıktır.
	if h.Service != "" {
		cat.Entities[h.Service] = true
	}

	// ── 1. Baş şüpheli ────────────────────────────────────────────────
	if h.TopSuspect != "" {
		addPos(h.TopSuspect, fmt.Sprintf(
			"korelasyon motorunun baş şüphelisi: %s (skor %.1f, güven %.2f)",
			h.TopSuspect, h.TopScore, h.Confidence))
	}

	// ── 2. Diğer adaylar ──────────────────────────────────────────────
	for _, c := range h.Candidates {
		if c.Service == "" || c.Service == h.TopSuspect {
			continue
		}
		txt := fmt.Sprintf("aday: %s (skor %.1f", c.Service, c.Score)
		if c.Hops > 0 {
			txt += fmt.Sprintf(", %d hop", c.Hops)
		}
		txt += ")"
		if c.Reason != "" {
			txt += " — " + c.Reason
		}
		addPos(c.Service, txt)
	}

	// ── 3. Deploy korelasyonu ─────────────────────────────────────────
	//
	// v0.9.1206 (Faz 6.2) — YAPISAL önce/sonra RED. Impact hipotezde
	// saklıydı (v0.9.1059) ama katalog satırı yalnız "sürüm + yaş"
	// diyordu; asıl kanıt (deploy'dan sonra p99/hata NE OLDU) modelin
	// gözünden kaçıyordu — tek cümlelik Reason'a gömülüydü. Aynı E
	// satırı, Impact varken ölçümü de taşır (ID konumu değişmez).
	if h.RecentDeploy != nil && h.RecentDeploy.Version != "" {
		txt := fmt.Sprintf("deploy: %s sürümü, problem açılmadan %d sn önce",
			h.RecentDeploy.Version, h.RecentDeploy.AgeSeconds)
		if imp := h.RecentDeploy.Impact; imp != nil {
			txt += fmt.Sprintf(" — etki (±%ddk): p99 %.0f→%.0fms (%+.0f%%), hata %%%.2f→%%%.2f, rps %.1f→%.1f",
				imp.WindowSec/60,
				imp.Before.P99Ms, imp.After.P99Ms, imp.P99DeltaPct,
				imp.Before.ErrorRate*100, imp.After.ErrorRate*100,
				imp.Before.RPS, imp.After.RPS)
		}
		addPos(h.Service, txt)
	}

	// ── 4. Derin soruşturma izi — BURADA AYRIŞIR ──────────────────────
	//
	// Checked listesi "neye bakıldı"yı söyler. Found=true olanlar
	// pozitif kanıt; Found=false olanlar NEGATİF ve ayrı uzaya gider.
	// Bu tek satırlık ayrım, tasarımın en kritik kararı.
	if h.Deep != nil {
		for _, ch := range h.Deep.Checked {
			if ch.Family == "" {
				continue
			}
			if ch.Found {
				txt := fmt.Sprintf("%s: bulundu", ch.Family)
				if ch.Records > 0 {
					txt += fmt.Sprintf(" (%d kayıt)", ch.Records)
				}
				if ch.Detail != "" {
					txt += " — " + ch.Detail
				}
				addPos("", txt)
				continue
			}
			txt := fmt.Sprintf("%s: BULUNAMADI", ch.Family)
			if ch.Detail != "" {
				txt += " — " + ch.Detail
			}
			addNeg("", txt)
		}
	}

	return cat
}

// renderRCAEvidenceCatalog — kataloğu prompt'a basar.
//
// İki bölüm AYRI başlıklar altında ve negatif bölüm ne için
// kullanılabileceğini AÇIKÇA söyler. Küçük modelde kuralı uzakta bir
// yerde bir kez söylemek yetmiyor; kısıtı verinin yanına yazmak
// gerekiyor.
func renderRCAEvidenceCatalog(c rcaEvidenceCatalog) string {
	var b strings.Builder
	pos := []rcaEvidenceRef{}
	neg := []rcaEvidenceRef{}
	for _, r := range c.Refs {
		if r.Kind == rcaNegative {
			neg = append(neg, r)
		} else {
			pos = append(pos, r)
		}
	}

	b.WriteString("KANIT (E) — bir kök nedene dayanak OLABİLİR:\n")
	if len(pos) == 0 {
		b.WriteString("  (yok)\n")
	}
	for _, r := range pos {
		fmt.Fprintf(&b, "  %s: %s\n", r.ID, r.Text)
	}

	if len(neg) > 0 {
		b.WriteString("\nBULUNAMAYAN (N) — YALNIZ bir hipotezi ÇÜRÜTMEK için kullanılabilir.\n")
		b.WriteString("Bir N kaydı ASLA kök nedenin kanıtı DEĞİLDİR: aranmış ve\n")
		b.WriteString("bulunamamış olmak, olduğunun kanıtı değildir.\n")
		for _, r := range neg {
			fmt.Fprintf(&b, "  %s: %s\n", r.ID, r.Text)
		}
	}
	return b.String()
}

// rcaAllowedEntities — K3 beyaz listesi, sıralı.
//
// Katalogdaki pozitif varlıklar + ankor servisi. Sıralı çünkü şema
// enum'una gidiyor ve rastgele sıra istek gövdesini gereksiz
// değiştirirdi (copilot_schemas.go'daki sortedKeys ile aynı gerekçe).
func rcaAllowedEntities(c rcaEvidenceCatalog) []string {
	out := make([]string, 0, len(c.Entities))
	for e := range c.Entities {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}
