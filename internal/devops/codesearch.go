package devops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// codesearch.go — ORGANİZASYON GENELİNDE KOD ARAMA (v0.10.74).
//
// ── NEDEN ───────────────────────────────────────────────────────────────
//
// Operatör: "Coremetry repoda bulamazsa tüm organizationı da arayabilir…
// metod stacktrace'ten search çalıştırıp ilgili repoyu bulup kontrol
// etse daha iyi olur."
//
// Teşhis doğru ve somut: depo SERVİS adından konvansiyonla çözülüyor, ama
// bir stack birden çok BİLEŞENE yayılıyor. Hatanın atıldığı sınıf çoğu
// zaman BAŞKA bir depoda yaşıyor ve konvansiyon onu ASLA bulamıyor —
// v0.10.71'de "eşleşmeyen" diye raporlanan frame'lerin gerçek sebebi bu.
//
// Arama o boşluğu kapatıyor: sınıf+metot ile organizasyon aranıyor, sonuç
// hem DEPOYU hem YOLU birden veriyor.
//
// ── ⚠ ARAMA TEK BAŞINA BELİRSİZ ─────────────────────────────────────────
//
// Operatörün ekranında aynı sınıf YEDİ sonuçta birden çıktı: aynı kodun
// Development / INT / UAT / Prod kopyaları (TFVC yolları) ve bir Git
// deposu. Yani "ilk sonucu al" YANLIŞ dosyayı kanıt diye sunar — ve
// yanlış kanıt, kanıt yokluğundan kötüdür.
//
// Sıralama bu yüzden SAF ve testli. Kurallar, en güçlüden:
//
//  1. TFVC yolları (`$/…`) ELENİR. İki gerekçe var ve İKİNCİSİ daha
//     güçlü: (a) çekim yolu yalnız Git konuşuyor, (b) operatör:
//     "TFVC olanlar eski kod, TFS zamanından; biz şimdi Git'e geçtik."
//     Yani o sonuçlar yalnız çekilemez değil, BAYAT — kanıt diye
//     sunulsalardı çalışan sürümle ilgisiz kod gösterilirdi.
//  2. Konvansiyonun çözdüğü depo varsa O kazanır (operatörün kendi
//     adlandırma sözleşmesi, bir tahminden güçlü).
//  3. Frame'in PAKET yoluyla örtüşen yol kazanır (com/x/y/… ).
//  4. GÜNCELLİK: operatörün AYARLADIĞI branş sırasında (release, master…)
//     önde olan dal kazanır. Operatör "daha güncel dosyaları dikkate
//     alabilirsin" dedi; arama sonucu tarih taşımıyor, ama kurulumun
//     kendi branş sözleşmesi güncelliğin en iyi vekili — ve tahmin
//     değil, operatörün yazdığı bir karar.
//  5. Kalanlar deterministik sırayla (proje, depo, yol) — aynı hata iki
//     kez farklı kanıt üretmesin.

// CodeSearchHit — arama sonucunun kullandığımız yarısı.
type CodeSearchHit struct {
	Project    string `json:"-"`
	Repository string `json:"-"`
	Path       string `json:"path"`
	// Branch — sonucun geldiği dal; boşsa çağıran deponun varsayılanına
	// düşer.
	Branch string `json:"-"`
}

// isTFVCPath — TFVC (server path) sonucu mu.
//
// Çekim yolu Git API'siyle konuşuyor; `$/Proje/…` bir Git yolu DEĞİL ve
// denemek 404 üretir. Elemek, boşa istek ve yanlış kanıt riskini birlikte
// kapatıyor.
func isTFVCPath(p string) bool { return strings.HasPrefix(strings.TrimSpace(p), "$/") }

// PickSearchHit — adaylardan en iyisini seçer. SAF.
//
// preferRepo: konvansiyonun çözdüğü depo (boş olabilir).
// frame: paket yolu örtüşmesi için (Class'ından türer).
//
// Hiçbir aday uygun değilse boş döner — çağıran o zaman "bulunamadı"
// der; uydurma bir eşleşme sunmaz.
func PickSearchHit(hits []CodeSearchHit, preferRepo string, frame stackparse.Frame, branchOrder []string) (CodeSearchHit, bool) {
	var usable []CodeSearchHit
	for _, h := range hits {
		if strings.TrimSpace(h.Path) == "" || isTFVCPath(h.Path) {
			continue
		}
		if strings.TrimSpace(h.Repository) == "" {
			continue
		}
		usable = append(usable, h)
	}
	if len(usable) == 0 {
		return CodeSearchHit{}, false
	}

	pkg := frame.PackagePath() // "com/x/y" ya da ""
	score := func(h CodeSearchHit) int {
		s := 0
		if preferRepo != "" && strings.EqualFold(h.Repository, preferRepo) {
			s += 100
		}
		if pkg != "" && strings.Contains(h.Path, pkg) {
			s += 10
		}
		// Güncellik vekili: ayardaki branş sırasında önde olan dal.
		// Sıradaki her basamak bir puan; listede olmayan dal 0 alır.
		if b := ShortBranch(h.Branch); b != "" {
			for i, want := range branchOrder {
				if strings.EqualFold(b, ShortBranch(want)) {
					s += len(branchOrder) - i
					break
				}
			}
		}
		return s
	}
	sort.SliceStable(usable, func(i, j int) bool {
		si, sj := score(usable[i]), score(usable[j])
		if si != sj {
			return si > sj
		}
		// Deterministik kuyruk: aynı hata iki kez AYNI kanıtı üretmeli.
		if usable[i].Project != usable[j].Project {
			return usable[i].Project < usable[j].Project
		}
		if usable[i].Repository != usable[j].Repository {
			return usable[i].Repository < usable[j].Repository
		}
		return usable[i].Path < usable[j].Path
	})
	return usable[0], true
}

// SearchQueryForFrame — frame'den arama metni.
//
// `Sınıf.metot` biçimi, operatörün elle yaptığı aramanın aynısı ve en
// seçici olanı: yalnız sınıf adı aramak, aynı adı taşıyan her dosyayı
// getirir; yalnız metot adı ise gürültü denizidir.
//
// Sınıf adı paketten SOYULUYOR: arama motoru tam nitelikli adı gövdede
// nadiren görür (kod `EmailSender.getX()` yazar, `com.x.EmailSender.getX()`
// değil).
func SearchQueryForFrame(f stackparse.Frame) string {
	cls := f.Class
	if i := strings.LastIndexByte(cls, '.'); i >= 0 {
		cls = cls[i+1:]
	}
	// İç sınıf: `Outer$Inner` → arama için `Inner` daha isabetli.
	if i := strings.LastIndexByte(cls, '$'); i >= 0 {
		cls = cls[i+1:]
	}
	m := strings.TrimSpace(f.Method)
	switch {
	case cls == "":
		return ""
	case m == "" || strings.HasPrefix(m, "<") || strings.HasPrefix(m, "lambda$"):
		// <init>/<clinit>/lambda: metot adı arama için işe yaramaz.
		return cls
	default:
		return cls + "." + m
	}
}

// ── AĞ YARISI ───────────────────────────────────────────────────────────

// codeSearchLimit — kaç ıskalayan frame için arama yapılır.
//
// 2: arama pahalı (ayrı bir servis, ayrı indeks) ve asıl kanıt zaten
// konvansiyon deposundan gelen pencerelerdir. İki arama, "hata başka bir
// bileşende" durumunu yakalamaya yetiyor; daha fazlası açıklamayı
// bekletir.
const codeSearchLimit = 2

// codeSearchTop — arama motorundan istenen sonuç sayısı.
//
// 20: operatörün ekranında aynı sınıf 7 sonuç verdi (env kopyaları).
// Sıralamanın doğru olanı seçebilmesi için adayların TAMAMINI görmesi
// gerekiyor; ilk birkaçını almak, elemeyi motora bırakmak olurdu.
const codeSearchTop = 20

// searchCodeBody — arama isteğinin gövdesi.
type searchCodeBody struct {
	SearchText string         `json:"searchText"`
	Top        int            `json:"$top"`
	Filters    map[string]any `json:"filters,omitempty"`
}

// searchCodeResponse — yanıtın kullandığımız yarısı.
//
// Alanlar TOLERANSLI okunuyor: uç sürümleri arasında zarf değişebiliyor
// ve eksik bir alan tüm aramayı düşürmemeli.
type searchCodeResponse struct {
	Results []struct {
		Path       string `json:"path"`
		Repository struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"repository"`
		Project struct {
			Name string `json:"name"`
		} `json:"project"`
		Versions []struct {
			BranchName string `json:"branchName"`
		} `json:"versions"`
	} `json:"results"`
}

// searchURL — {base}/{collection}/_apis/search/codesearchresults
//
// api-version 7.0 (v0.10.98, operatör-raporlu CANLI ilk koşu): 7.1,
// on-prem Azure DevOps Server'da "out of range for this server; latest
// supported is 7.0" ile HTTP 400 veriyordu — arama özelliği açıldığı
// gün hiç çalışmadan düştü. Kod arama ucu 7.0'da birebir aynı sözleşme;
// 7.0 bulutta da geçerli. Sunucunun kendi cevabı zemin gerçeği.
func searchURL(cfg Settings) string {
	return collectionURL(cfg) + "/_apis/search/codesearchresults?api-version=7.0"
}

// SearchCode — organizasyonda kod arar.
//
// Hata durumunda BOŞ liste + hata döner ve çağıran sessizce devam eder:
// arama bir EK yol, kod bağlamının ön koşulu değil. Uzantı kurulu
// değilse (404) ya da PAT'in arama kapsamı yoksa (401) açıklama yine
// üretilmeli.
func SearchCode(ctx context.Context, cli *http.Client, cfg Settings, text string) ([]CodeSearchHit, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	body, err := json.Marshal(searchCodeBody{SearchText: text, Top: codeSearchTop})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchURL(cfg), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "coremetry-devops/1.0")
	if cfg.PAT != "" || cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.PAT)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, fileBodyCap))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kod arama http %d: %s", resp.StatusCode, firstLine(string(raw)))
	}
	var out searchCodeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("kod arama yanıtı çözümlenemedi: %w", err)
	}
	hits := make([]CodeSearchHit, 0, len(out.Results))
	for _, r := range out.Results {
		h := CodeSearchHit{
			Project: r.Project.Name, Repository: r.Repository.Name, Path: r.Path,
		}
		// TFVC sonuçlarında repository.type "tfvc" gelir; PickSearchHit
		// yol önekinden de eliyor ama tip varsa onu da kullanıyoruz.
		if strings.EqualFold(r.Repository.Type, "tfvc") {
			h.Repository = ""
		}
		if len(r.Versions) > 0 {
			h.Branch = strings.TrimPrefix(r.Versions[0].BranchName, "refs/heads/")
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// ── PROJE ÇIKMAZI YEDEĞİ (v0.10.85) ─────────────────────────────────────
//
// Operatör-raporlu: servis BAŞKA bir DevOps projesi altında yaşıyor;
// Ayarlar'daki Project boş, katalog pini yok, önek de tutmuyor. Üç kaynak
// birden ıskalayınca FetchCode "proje adı çözülemedi" duvarına çarpıyor ve
// v0.10.74'ün organizasyon araması — tam bu iş için yazılmış makine — HİÇ
// koşmuyordu, çünkü sırada ondan ÖNCE duruyordu
// ([[feedback-tested-but-unreachable]]). Arama koleksiyon kapsamlı ve
// isabet PROJE adını da taşıyor: çıkmazın cevabı zaten elimizdeydi.

// searchOffRemedyTR — çıkmaz cümlesine eklenen dördüncü çare. TEK YAZIM:
// FetchCode ve dry-run aynı sabiti basar; iki elde iki yazım sessizce
// ayrışırdı ([[feedback-gate-single-spelling]]).
const searchOffRemedyTR = " — ya da Ayarlar → Kod entegrasyonu'nda kod aramasını açın: " +
	"organizasyon genelinde arayıp depoyu ve projeyi stacktrace'ten kendisi bulur"

// searchResolveProjectRepo — proje çıkmazında organizasyon aramasıyla
// (proje, depo) çözümü. Ağ yarısı `search` ile enjekte edilir; gerisi SAF.
//
// Pencere avından (huntSearchWindows) farkı: orada proje ZATEN çözülmüş
// ve arama yalnız ıskalayan frame'in dosyasını arıyor; burada aranan şey
// deponun KİMLİĞİ. Bu yüzden projesiz isabet burada İŞE YARAMAZ (item
// URL'i projesiz kurulamaz) ve elenir.
//
// Dönen note her iki dalda da doludur: bulunduysa kaynak künyesi,
// bulunamadıysa nedeni — sessiz bir başarısızlık "aradık, yok" ile
// "hiç aramadık"ı ayırt edilemez kılardı.
func searchResolveProjectRepo(
	ctx context.Context,
	targets []stackparse.Frame,
	preferRepo string,
	branchOrder []string,
	search func(context.Context, string) ([]CodeSearchHit, error),
) (project, repo, note string, ok bool) {
	tried := 0
	for _, f := range targets {
		if tried >= codeSearchLimit || ctx.Err() != nil {
			break
		}
		q := SearchQueryForFrame(f)
		if q == "" {
			continue
		}
		tried++
		hits, err := search(ctx, q)
		if err != nil {
			return "", "", "organizasyon araması başarısız: " + firstLine(err.Error()), false
		}
		var withProject []CodeSearchHit
		for _, h := range hits {
			if strings.TrimSpace(h.Project) != "" {
				withProject = append(withProject, h)
			}
		}
		h, hok := PickSearchHit(withProject, preferRepo, f, branchOrder)
		if !hok {
			continue
		}
		return h.Project, h.Repository,
			"depo organizasyon aramasıyla bulundu: " + h.Project + "/" + h.Repository +
				" (arama: " + q + ")", true
	}
	if tried == 0 {
		return "", "", "organizasyon araması koşamadı: aranabilir frame yok", false
	}
	return "", "", "organizasyon araması da eşleşme bulamadı", false
}

// huntSearchWindows — ISKALAYAN frame'ler için organizasyon araması.
//
// ⚠ SIRA BİLİNÇLİ: konvansiyon + depo ağacı ÖNCE, arama SONRA. Operatör
// aramanın "ilk seçenek" olmasını önerdi ve gerekçesi güçlü, ama ekranı
// aynı zamanda aramanın BELİRSİZ olduğunu gösterdi (aynı sınıf yedi
// sonuçta, env kopyaları). Konvansiyon isabet ettiğinde sonuç KESİN;
// arama ise sıralamaya güveniyor. Kesin olanı önce denemek, belirsizi
// yalnız boşlukta kullanmak daha az yanlış kanıt üretir.
//
// Arama bugün çalışan hiçbir çözümü DEĞİŞTİRMİYOR: yalnız ıskalayanlara
// bakıyor.
func huntSearchWindows(
	ctx context.Context,
	missed []stackparse.Frame,
	preferRepo string,
	branchOrder []string,
	radius int,
	search func(context.Context, string) ([]CodeSearchHit, error),
	fetchIn func(ctx context.Context, project, repo, branch, path string) (string, error),
) ([]CodeWindow, []string) {
	var out []CodeWindow
	var notes []string
	tried := 0
	for _, f := range missed {
		if tried >= codeSearchLimit || ctx.Err() != nil {
			break
		}
		q := SearchQueryForFrame(f)
		if q == "" {
			continue
		}
		tried++
		hits, err := search(ctx, q)
		if err != nil {
			// Arama bir EK yol: uzantı yoksa (404) ya da PAT'in kapsamı
			// yetmiyorsa (401) açıklama yine üretilmeli. Sebep NOTA
			// yazılıyor — sessiz kalmak "aradık, yok" demek olurdu.
			notes = append(notes, "kod araması başarısız: "+firstLine(err.Error()))
			return out, notes
		}
		h, ok := PickSearchHit(hits, preferRepo, f, branchOrder)
		if !ok {
			continue
		}
		// v0.10.85 — isabetin PROJESİ de geçer. Eskiden yalnız depo adı
		// geçiyordu ve BAŞKA projedeki bir isabet, yürürlükteki projenin
		// URL'iyle çekilip 404'e düşüyor, sessizce atlanıyordu: arama
		// depoyu bulmuş, çekim onu kaybetmişti.
		body, ferr := fetchIn(ctx, h.Project, h.Repository, h.Branch, h.Path)
		if ferr != nil || strings.TrimSpace(body) == "" {
			continue
		}
		w := WindowAround(body, f.Line, radius)
		if w.Content == "" {
			continue
		}
		w.Path, w.Frame, w.Line, w.Segment = h.Path, f.String(), f.Line, f.Segment
		// Depo adı yola yazılıyor: pencere BAŞKA bir depodan geliyor ve
		// operatör bunu görmeli, yoksa yolu kendi deposunda arar.
		w.Path = h.Repository + ":" + h.Path
		out = append(out, w)
	}
	if len(out) > 0 {
		notes = append(notes, fmt.Sprintf("%d pencere organizasyon aramasıyla BAŞKA depodan geldi", len(out)))
	}
	return out, notes
}

// errCodeSearchLimit — hata-kodu token'ı başına değil TOPLAM arama tavanı.
// Frame aramasıyla aynı gerekçe (codeSearchLimit): arama pahalı, asıl
// kanıt pencereleri başka yoldan da geliyor.
const errCodeSearchLimit = 2

// huntErrorCodeWindows — hata-kodu token'larıyla DİL-BAĞIMSIZ organizasyon
// araması (v0.10.100). Frame aramasından farkı: sorgu bir sınıf.metot değil
// hata kodunun kendisi, hedef satır da frame'in satırı değil token'ın
// dosyada İLK geçtiği satır — uzantı ne olursa olsun (.cs dahil) fırlatan
// satırın çevresi pencerelenir.
func huntErrorCodeWindows(
	ctx context.Context,
	tokens []string,
	branchOrder []string,
	radius int,
	search func(context.Context, string) ([]CodeSearchHit, error),
	fetchIn func(ctx context.Context, project, repo, branch, path string) (string, error),
) ([]CodeWindow, []string) {
	var out []CodeWindow
	var notes []string
	tried := 0
	for _, tok := range tokens {
		if tried >= errCodeSearchLimit || ctx.Err() != nil {
			break
		}
		tried++
		hits, err := search(ctx, tok)
		if err != nil {
			notes = append(notes, "hata-kodu araması başarısız: "+firstLine(err.Error()))
			return out, notes
		}
		// Boş frame: paket-yolu bonusu yok; TFVC eleme + branş rütbesi +
		// deterministik kuyruk aynen işler.
		h, ok := PickSearchHit(hits, "", stackparse.Frame{}, branchOrder)
		if !ok {
			continue
		}
		body, ferr := fetchIn(ctx, h.Project, h.Repository, h.Branch, h.Path)
		if ferr != nil || strings.TrimSpace(body) == "" {
			continue
		}
		line := 0
		for i, ln := range strings.Split(body, "\n") {
			if strings.Contains(ln, tok) {
				line = i + 1
				break
			}
		}
		if line == 0 {
			continue // arama gövde eşleşmesi vermiş olabilir; satır yoksa pencere yok
		}
		w := WindowAround(body, line, radius)
		if w.Content == "" {
			continue
		}
		w.Line = line
		w.Frame = "hata kodu: " + tok
		w.Path = h.Repository + ":" + h.Path
		out = append(out, w)
	}
	if len(out) > 0 {
		notes = append(notes, fmt.Sprintf("%d pencere hata-kodu aramasıyla bulundu (dil-bağımsız)", len(out)))
	}
	return out, notes
}
