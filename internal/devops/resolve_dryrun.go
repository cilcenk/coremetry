package devops

import (
	"context"
	"strconv"
	"strings"
)

// resolve_dryrun.go — servis → depo çözümünün LLM'SİZ provası
// (v0.9.1242).
//
// SORUN (2026-08-19 ops olayının kökü): adlandırma konvansiyonunu
// (önek, branş sırası, proje) test etmenin TEK yolu, stack taşıyan
// gerçek bir exception bulup "Kodu da incele"yi tıklamak ve tam bir
// LLM turu ödemekti. Üstelik zincirin her adımı FAIL-OPEN, yani
// yanlış önek ekrana ancak cevabın altındaki bir `reason` satırı
// olarak düşüyordu — deneme başına dakikalar ve bir çıkarım.
//
// ÇÖZÜM: aynı zincir, sağlayıcıya hiç uğramadan. Operatör servis
// adını yazar, her adımın {ok, detail} sonucunu saniyeler içinde
// görür.
//
// ÜÇ PAZARLIKSIZ KARAR:
//
//  1. GERÇEK ZİNCİR. Branş + kaçış kapısı + ağaç, code.go'daki
//     resolveChain'in TA KENDİSİ; repo/proje çözümü ResolveRepo ve
//     pickProject'in ta kendisi. Kopya bir zincir, ilk düzeltmede
//     ayrışır ve dry-run olmayan bir davranışı "doğrulamaya" başlar.
//     Bir teşhis aracının yapabileceği en pahalı hata budur.
//  2. SAYAÇLARA KARIŞMAZ. v0.9.1241'in kod-çekme sayaçları gerçek
//     isabet oranını ölçüyor. Dry-run FetchCode'a HİÇ GİRMEZ —
//     RecordCodeOutcome yalnız onun defer'ında — yani deneme
//     sayısını da oranı da kıpırdatmaz. Mekanizma yapısal, bir
//     bayrak değil: "sayma" bayrağı unutulabilir, çağrılmayan
//     fonksiyon unutulamaz. resolve_dryrun_test.go bunu kilitliyor.
//  3. AĞAÇTAN YALNIZ SAYI. Dosya ADLARI dönmez. Soru "bu depo
//     okunabiliyor mu", "içinde ne var" değil; yol listesi hem
//     yanıtı şişirir hem de ekranı asıl cevaptan uzaklaştırır.
//
// PAT hiçbir adımın detail'ine girmez (Settings sözleşmesi): bağlantı
// adımı yalnız URL + koleksiyon yazar.

// Adım anahtarları — frontend rozetleri ve testler bunları okur.
const (
	DryRunStepConnection = "connection"
	DryRunStepPin        = "pin"
	DryRunStepRepo       = "repo"
	DryRunStepProject    = "project"
	DryRunStepBranch     = chainStepBranch
	DryRunStepTree       = chainStepTree
)

// PinRead — servis kataloğu okumasının SONUCU.
//
// devops paketi CH'yi bilmiyor (paket doc'u), o yüzden okumayı
// internal/api yapar ve sonucu buraya taşır. Şekil, gerçek yoldaki
// pinReadDecision'ın dönüşüyle birebir: (pin, iptal).
type PinRead struct {
	// Repo — katalogdaki `repository` alanı ("" = pin yok).
	Repo string
	// Abort — katalog OKUNAMADI, adım fail-CLOSED iptal edildi
	// (v0.9.1236). Doluysa zincir burada durur; gerçek yolda da
	// öyle durur.
	Abort string
}

// DryRunStep — tek adımın sonucu. Detail her iki hâlde de DOLU:
// başarıda ne bulunduğu, başarısızlıkta neden bulunamadığı.
type DryRunStep struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// DryRunResult — provanın tamamı. Steps SIRALI ve zincir nerede
// durduysa orada biter: yarısı yeşil, biri kırmızı, gerisi YOK.
// Koşmamış bir adımı "başarısız" göstermek, operatörü olmayan bir
// arızanın peşine düşürürdü.
type DryRunResult struct {
	Service string `json:"service"`
	// OK — zincirin TAMAMI yürüdü mü (ağaç dahil).
	OK    bool         `json:"ok"`
	Steps []DryRunStep `json:"steps"`
	// Özet alanlar — ekranın başlık satırı. Adımlardan türer;
	// çözülemeyen alan boş kalır.
	Repo    string `json:"repo,omitempty"`
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch,omitempty"`
	// Source — depo adının kaynağı (pin | convention).
	Source string `json:"source,omitempty"`
	// FileCount — ağaçtaki dosya sayısı. Yollar DÖNMEZ.
	FileCount int `json:"fileCount,omitempty"`
}

// add — adımı listeye ekler ve "hepsi yeşil mi" bayrağını günceller.
func (r *DryRunResult) add(key, label string, ok bool, detail string) {
	r.Steps = append(r.Steps, DryRunStep{Key: key, Label: label, OK: ok, Detail: detail})
	r.OK = ok
}

// sourceLabel — kaynak kodunun insan karşılığı. Bilinmeyen değer
// AYNEN döner: yeni bir kaynak eklendiğinde ekranda boş bir parantez
// değil, ham etiket görünsün.
func sourceLabel(src string) string {
	switch src {
	case RepoSourcePin:
		return "katalog pini"
	case RepoSourceConvention:
		return "konvansiyon"
	case ProjectSourceSettings:
		return "Ayarlar"
	}
	return src
}

// connectionDetail — bağlantı adımının metni. PAT YOK, kullanıcı adı
// YOK: bu dize operatör ekranına gider.
func connectionDetail(cfg Settings) string {
	d := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if c := strings.TrimSpace(cfg.Collection); c != "" {
		d += " / " + c
	}
	return d
}

// ResolveDryRun — servis adı → depo/branş/ağaç, adım adım. LLM YOK,
// yazma YOK, sayaç YOK.
//
// Zincir ilk kırmızıda DURUR. Bağlantı yoksa hiç başlamaz: türetilmiş
// bir depo adını sunucuya sormadan göstermek, doğrulanmamış bir tahmini
// cevap gibi sunmak olurdu — bu ekranın var olma sebebi tam da o
// yanılgıyı bitirmek.
func (s *Service) ResolveDryRun(ctx context.Context, service string, pin PinRead) DryRunResult {
	out := DryRunResult{Service: strings.TrimSpace(service)}

	// 1 — bağlantı. CurrentSettings nil-Service'te sıfır Settings
	// döner, yani nil süreç de aynı dürüst cümleye çıkar.
	cfg := s.CurrentSettings()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		out.add(DryRunStepConnection, "DevOps bağlantısı", false,
			"DevOps bağlantısı yapılandırılmamış (Ayarlar → Kod entegrasyonu)")
		return out
	}
	out.add(DryRunStepConnection, "DevOps bağlantısı", true, connectionDetail(cfg))

	// 2 — katalog pini. Okuma HATASI fail-CLOSED (v0.9.1236): gerçek
	// yolda da burada durulur, dry-run onu olduğu gibi göstermeli.
	switch {
	case pin.Abort != "":
		out.add(DryRunStepPin, "Katalog pini", false, pin.Abort)
		return out
	case strings.TrimSpace(pin.Repo) != "":
		out.add(DryRunStepPin, "Katalog pini", true, strings.TrimSpace(pin.Repo))
	default:
		out.add(DryRunStepPin, "Katalog pini", true, "pin yok — konvansiyon kullanılacak")
	}

	// 3 — depo adı.
	res := ResolveRepo(out.Service, pin.Repo, s.ResolveConfig())
	if res.Repo == "" {
		reason := res.Reason
		if reason == "" {
			reason = "servis için depo çözülemedi"
		}
		out.add(DryRunStepRepo, "Depo adı", false, reason)
		return out
	}
	out.Repo, out.Source = res.Repo, res.Source
	out.add(DryRunStepRepo, "Depo adı", true, res.Repo+" (kaynak: "+sourceLabel(res.Source)+")")

	// 4 — proje. ÜÇ kaynak, üçünün de düzeltmesi ayrı yerde; çıkmaz
	// cümlesi (projectDeadEnd) üçünü birden anlatır.
	project, psrc, dead := pickProject(cfg, res.Project)
	if dead != "" {
		out.add(DryRunStepProject, "Proje", false, dead)
		return out
	}
	out.Project = project
	out.add(DryRunStepProject, "Proje", true, project+" (kaynak: "+sourceLabel(psrc)+")")
	cfg.Project = project

	// 5-6 — branş + ağaç: GERÇEK zincir (code.go/resolveChain).
	// Süre tavanı FetchCode'unkiyle aynı; parent AYRI tutulur ki
	// "tarayıcı gitti" ile "DevOps yanıt vermedi" karışmasın.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, s.fetchDeadline())
	defer cancel()
	ch := s.resolveChain(ctx, parent, s.clientFor(cfg.InsecureSkipVerify), cfg, res.Repo)
	if ch.repo != "" {
		out.Repo = ch.repo
	}
	if ch.class != "" && ch.at == DryRunStepBranch {
		out.add(DryRunStepBranch, "Branş", false, ch.reason)
		return out
	}
	out.Branch = ch.branch
	out.add(DryRunStepBranch, "Branş", true, withNote(ch.note, branchDetail(ch.branch)))
	if ch.class != "" {
		out.add(DryRunStepTree, "Depo ağacı", false, ch.reason)
		return out
	}
	out.FileCount = len(ch.paths)
	out.add(DryRunStepTree, "Depo ağacı", true, strconv.Itoa(len(ch.paths))+" dosya")
	return out
}

// branchDetail — branş adımının metni. Boş ad, deponun VARSAYILAN
// branşının da okunamadığı ama zincirin yine de yürüdüğü hâl
// (resolveBranch sözleşmesi); "" yazmak yerine ne olduğunu söyle.
func branchDetail(branch string) string {
	if strings.TrimSpace(branch) == "" {
		return "branş adı okunamadı — deponun varsayılanı kullanılacak"
	}
	return branch
}
