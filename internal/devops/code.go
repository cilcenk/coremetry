package devops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// code.go — stack frame → kaynak kodu penceresi (v0.9.830).
//
// Akış: depo ağacını recursive listele (10 dk cache) → frame'in dosya
// adını yol SONEKİ olarak eşleştir (birden çok aday varsa paket yoluna
// en çok benzeyen kazanır) → dosya içeriğini çek → hatalı satırın ±30
// satırı. İlk 3 uygulama frame'i, toplam ~4.000 rune.
//
// İKİ SÖZLEŞME, İKİSİ DE PAZARLIKSIZ:
//
//  1. FAIL-OPEN. Bu yol bir açıklamayı ASLA düşürmez. Bağlantı yok,
//     depo bulunamadı, PAT'ın Code(Read) yetkisi yok, dosya adı ağaçta
//     geçmiyor — hepsi boş CodeContext + insan-okunur Reason ile döner.
//     Kod bağlamı bir BONUS'tur; onu zorunlu kılmak, çalışan bir
//     açıklamayı çalışmayan bir entegrasyona rehin vermek olurdu.
//  2. Code Search eklentisine GÜVENİLMEZ. `_apis/search` on-prem
//     kurulumlarda ayrı bir uzantıdır ve çoğu koleksiyonda kurulu
//     değildir; varlığını varsayan bir tasarım tam da sahada patlar.
//     Düz items API'si her kurulumda vardır.
const (
	// codeFrameLimit — kaç uygulama frame'i için kod çekilir.
	// 3: hata satırı + onu çağıran + bir üstü. Daha fazlası gemma4'ün
	// bağlamında kod-dışı kanıtı (trace, log) sıkıştırmaya başlar.
	codeFrameLimit = 3
	// codeWindowRadius — hatalı satırın etrafından ±N satır.
	codeWindowRadius = 30
	// codeBudgetRunes — tüm pencerelerin TOPLAM tavanı. Rune, byte
	// değil: Türkçe yorum satırı taşıyan kaynak dosyalarda byte
	// kesmesi karakter böler (v0.9.414 dersi).
	codeBudgetRunes = 4000
	// treeTTL — depo ağacı cache'i. Depo ağacı dakikalar ölçeğinde
	// değişmez; 10 dk aynı exception'a arka arkaya bakan operatörü
	// tek listelemeyle idare eder.
	treeTTL = 10 * time.Minute
	// treeMaxPaths — cache'te tutulan yol sayısı tavanı. Devasa bir
	// monorepo'nun ağacı pod'un RAM'ini yemesin.
	treeMaxPaths = 60000
	// treeMaxRepos — cache'teki depo sayısı tavanı (basit eviction).
	treeMaxRepos = 8
	// treeBodyCap — recursive listing yanıtı için okuma tavanı (8MB).
	// 60k dosyalık bir ağacın JSON'u birkaç MB tutar.
	treeBodyCap = 8 << 20
	// fileBodyCap — tek dosya içeriği için tavan (2MB).
	fileBodyCap = 2 << 20
)

// CodeWindow — tek frame için çekilen kaynak penceresi.
type CodeWindow struct {
	Path     string `json:"path"`     // depo içi tam yol
	Frame    string `json:"frame"`    // "com.x.Y.m(Y.java:246)"
	Line     int    `json:"line"`     // frame'in işaret ettiği satır
	FromLine int    `json:"fromLine"` // pencerenin ilk satırı (1-tabanlı)
	ToLine   int    `json:"toLine"`   // pencerenin son satırı
	Content  string `json:"content"`  // satır numarası ÖNEKLİ kaynak
}

// CodeContext — bir stacktrace için toplanan tüm kod bağlamı.
//
// Windows boşsa Reason DOLU olmalı: "kod yok" cevabının yanında
// "neden yok" olmadan operatör entegrasyonun bozuk mu yoksa sadece
// eşleşme mi bulamadığını ayırt edemez.
type CodeContext struct {
	Repo    string       `json:"repo,omitempty"`
	Branch  string       `json:"branch,omitempty"`
	Source  string       `json:"source,omitempty"` // pin | convention
	Windows []CodeWindow `json:"windows,omitempty"`
	Reason  string       `json:"reason,omitempty"`
}

// Empty — kod bağlamı yok mu?
func (c CodeContext) Empty() bool { return len(c.Windows) == 0 }

// Halved — kod bütçesini YARIYA indirir (v0.9.831).
//
// Sağlayıcı bağlam taşması 400'ü döndüğünde çağıran BİR kez bununla
// yeniden dener. Kod, prompt'a en son eklenen ve tek başına en büyük
// parçadır; taşmada ilk küçültülecek şey odur — exception bağlamının
// kendisi (stack, trace, loglar) kod olmadan da cevap üretebilir,
// tersi doğru değil.
//
// Yeni bir ağ isteği YOK: eldeki pencereler kırpılır.
func (c CodeContext) Halved() CodeContext {
	if c.Empty() {
		return c
	}
	c.Windows, _ = ClampCodeWindows(c.Windows, codeBudgetRunes/2)
	return c
}

// treeEntry — cache'lenen depo ağacı.
type treeEntry struct {
	paths []string
	at    time.Time
}

// repoListEntry — cache'lenen proje depo listesi (v0.9.1236).
type repoListEntry struct {
	names []string
	at    time.Time
}

// codeCache — Service'e iliştirilen ağaç cache'i. Ayrı mutex:
// listeleme saniyeler sürebilir, bu sırada Snapshot()'ı bloklamak
// ayar sayfasını dondururdu.
//
// v0.9.1236 — depo ADI listesi de burada duruyor (repo_catalog.go).
// İkinci bir cache açmak yerine mevcut disipline yerleşiyor: aynı
// mutex, aynı TTL ölçeği, aynı "tavana çarpınca en eskiyi at".
type codeCache struct {
	mu    sync.Mutex
	tree  map[string]treeEntry
	repos map[string]repoListEntry
}

// getRepos / putRepos — depo adı listesi cache'i (v0.9.1236).
func (c *codeCache) getRepos(key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.repos[key]
	if !ok || time.Since(e.at) > repoListTTL {
		return nil, false
	}
	return e.names, true
}

func (c *codeCache) putRepos(key string, names []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.repos == nil {
		c.repos = map[string]repoListEntry{}
	}
	if len(c.repos) >= repoListMaxProjects {
		oldestKey, oldestAt := "", time.Now()
		for k, v := range c.repos {
			if v.at.Before(oldestAt) {
				oldestKey, oldestAt = k, v.at
			}
		}
		if oldestKey != "" {
			delete(c.repos, oldestKey)
		}
	}
	c.repos[key] = repoListEntry{names: names, at: time.Now()}
}

func (c *codeCache) get(key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.tree[key]
	if !ok || time.Since(e.at) > treeTTL {
		return nil, false
	}
	return e.paths, true
}

func (c *codeCache) put(key string, paths []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tree == nil {
		c.tree = map[string]treeEntry{}
	}
	if len(c.tree) >= treeMaxRepos {
		// En eski girdiyi at. LRU değil FIFO-ish: 8 girdide fark
		// pratikte yok, kod ise bir satır.
		oldestKey, oldestAt := "", time.Now()
		for k, v := range c.tree {
			if v.at.Before(oldestAt) {
				oldestKey, oldestAt = k, v.at
			}
		}
		if oldestKey != "" {
			delete(c.tree, oldestKey)
		}
	}
	c.tree[key] = treeEntry{paths: paths, at: time.Now()}
}

// FetchCode — bir depodaki frame'ler için kod pencereleri toplar.
//
// repo boşsa ya da bağlantı yapılandırılmamışsa boş + Reason döner;
// HATA DÖNDÜRMEZ (fail-open sözleşmesi — imzada error yok ki çağıran
// yanlışlıkla açıklamayı düşürmesin).
// projectHint (v0.9.1183) — servis önekinden türetilen proje ÖNERİSİ;
// ayardaki açık Project boşsa kullanılır. Operatör isteği: "service_name
// başında bsa- yazıyorsa direkt project BSA olduğunu anlasın."
func (s *Service) FetchCode(ctx context.Context, repo, projectHint string, frames []stackparse.Frame) CodeContext {
	out := CodeContext{Repo: repo}
	if s == nil {
		return CodeContext{Reason: "DevOps istemcisi yok"}
	}
	if strings.TrimSpace(repo) == "" {
		return CodeContext{Reason: "servis için depo çözülemedi"}
	}
	cfg := s.CurrentSettings()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return CodeContext{Repo: repo, Reason: "DevOps bağlantısı yapılandırılmamış (Ayarlar → Kod entegrasyonu)"}
	}
	// v0.9.1183 — proje ayarda boşsa servis önekinden TÜRETİLİR
	// (bsa-… → BSA). Açık ayar HER ZAMAN kazanır: türetme bir tahmin,
	// operatörün yazdığı ad bir karar (ResolveRepo'daki pin sözleşmesinin
	// aynısı). Önceden burası kesin bir duvardı ve kurulumun kendi
	// adlandırma sözleşmesi zaten cevabı taşırken operatörden aynı bilgiyi
	// ikinci kez istiyordu.
	cfg.Project = strings.TrimSpace(cfg.Project)
	if cfg.Project == "" {
		cfg.Project = strings.TrimSpace(projectHint)
	}
	if cfg.Project == "" {
		// Depo ADIYLA çağırıyoruz; ada göre çözüm proje kapsamı ister.
		return CodeContext{Repo: repo, Reason: "DevOps ayarında Project boş ve servis adı bilinen bir önekle başlamıyor — Project'i doldurun ya da servis önekini Ayarlar → Kod entegrasyonu'na ekleyin"}
	}
	// v0.9.1235 — AppFrames artık EN DERİN "Caused by" segmentinden dışa
	// doğru seçiyor: üç pencerenin ilki kök nedenin fırlatıldığı satır,
	// wrapper'ın yeniden-fırlatma satırları arta kalan bütçeye düşüyor.
	targets := stackparse.AppFrames(frames, codeFrameLimit)
	if len(targets) == 0 {
		return CodeContext{Repo: repo, Reason: "stack'te dosya+satır taşıyan uygulama frame'i yok"}
	}

	cli := s.clientFor(cfg.InsecureSkipVerify)
	ver, branch, err := s.resolveBranch(ctx, cli, cfg, repo)
	// note — düzeltme izi. Boş kalırsa hiçbir şey değişmedi demektir.
	note := ""
	if err != nil {
		// KAÇIŞ KAPISI (v0.9.1236). Konvansiyon küçük harf üretir,
		// gerçek depo başka yazımda olabilir. Liste çağrısı YALNIZ
		// burada: mutlu yol tek fazladan istek bile görmez.
		if canon, near := s.recoverRepoName(ctx, cli, cfg, repo, err); canon != "" {
			if ver2, branch2, err2 := s.resolveBranch(ctx, cli, cfg, canon); err2 == nil {
				note = "depo adı sunucudan düzeltildi: " + repo + " → " + canon
				repo, ver, branch, err = canon, ver2, branch2, nil
				out.Repo = canon
			}
		} else if len(near) > 0 {
			err = fmt.Errorf("%v (sunucudaki en yakın adlar: %s)", err, strings.Join(near, ", "))
		}
		if err != nil {
			return CodeContext{Repo: repo, Reason: sanitize(err.Error(), cfg)}
		}
	}
	out.Branch = branch

	paths, err := s.repoTree(ctx, cli, cfg, ver, repo, branch)
	if err != nil {
		return CodeContext{Repo: repo, Branch: branch, Reason: withNote(note, sanitize(err.Error(), cfg))}
	}
	if len(paths) == 0 {
		// v0.9.1183 — NE DENENDİĞİ yazılıyor. Proje artık türetilebiliyor
		// (bsa-… → BSA) ve türetme bir tahmin; "depo ağacı boş döndü"
		// tek başına operatöre yanlış tahmini göstermez, oysa hatanın en
		// olası sebebi tam olarak yanlış proje/depo adıdır (ör. gerçek depo
		// farklı harf yazımında). Katalogdaki Repository pini bunu ezer.
		return CodeContext{Repo: repo, Branch: branch,
			Reason: withNote(note, "depo ağacı boş döndü (proje "+cfg.Project+", depo "+repo+", branş "+branch+")")}
	}

	var windows []CodeWindow
	var misses []string
	for _, f := range targets {
		p := BestPathForFrame(paths, f)
		if p == "" {
			misses = append(misses, f.File)
			continue
		}
		body, ferr := fetchItemContent(ctx, cli, cfg, ver, repo, branch, p)
		if ferr != nil {
			misses = append(misses, f.File+" (okunamadı)")
			continue
		}
		w := WindowAround(body, f.Line, codeWindowRadius)
		if w.Content == "" {
			misses = append(misses, f.File+" (satır aralığı boş)")
			continue
		}
		w.Path, w.Frame, w.Line = p, f.String(), f.Line
		windows = append(windows, w)
	}

	windows, trimmed := ClampCodeWindows(windows, codeBudgetRunes)
	out.Windows = windows
	switch {
	case len(windows) == 0 && len(misses) > 0:
		out.Reason = "ağaçta eşleşen dosya yok: " + strings.Join(misses, ", ")
	case len(windows) == 0:
		out.Reason = "kod penceresi kurulamadı"
	case trimmed:
		out.Reason = fmt.Sprintf("kod bütçesi (%d karakter) doldu — pencereler kısaltıldı", codeBudgetRunes)
	}
	// v0.9.1236 — düzeltme izi BAŞARILI çekmede de kalır. Kod geldi
	// diye susmak, operatörün katalogdaki/konvansiyondaki yanlış adı
	// hiç öğrenmemesi demek olurdu; ekrandaki "Kaynak: <depo>" satırı
	// beklediğinden farklı bir ad gösterirken sebebi de söylemeli.
	out.Reason = withNote(note, out.Reason)
	return out
}

// resolveBranch — refs API'sinden branşları çeker ve ayardaki sıraya
// göre seçer; hiçbiri yoksa deponun VARSAYILAN branşına düşer.
// Dönen ilk değer, bu depo için çalışan api-version'dur.
func (s *Service) resolveBranch(ctx context.Context, cli *http.Client, cfg Settings, repo string) (string, string, error) {
	order := s.ResolveConfig().BranchOrder
	var firstErr error
	for _, ver := range apiVersionCandidates(cfg) {
		u := repoURL(cfg, repo) + "/refs?filter=heads&api-version=" + ver
		body, err := doGet(ctx, cli, u, cfg)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var rr struct {
			Value []struct {
				Name string `json:"name"`
			} `json:"value"`
		}
		if err := json.Unmarshal(body, &rr); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("branş listesi çözümlenemedi")
			}
			continue
		}
		names := make([]string, 0, len(rr.Value))
		for _, r := range rr.Value {
			names = append(names, r.Name)
		}
		if b := PickBranch(names, order); b != "" {
			return ver, b, nil
		}
		if b := defaultBranch(ctx, cli, cfg, ver, repo); b != "" {
			return ver, b, nil
		}
		return ver, "", fmt.Errorf("depo %q: %v branşlarının hiçbiri yok ve varsayılan branş okunamadı", repo, order)
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("depo %q bulunamadı", repo)
	}
	return "", "", firstErr
}

// defaultBranch — deponun kendi varsayılan branşı. Konvansiyon
// tutmadığında ("release yok, master yok") susup boş dönmek yerine
// deponun gerçeğini kullanırız; bu bir hata değil, farklı bir
// konvansiyondur. Okunamazsa "" — çağıran fail-open'a düşer.
func defaultBranch(ctx context.Context, cli *http.Client, cfg Settings, ver, repo string) string {
	body, err := doGet(ctx, cli, repoURL(cfg, repo)+"?api-version="+ver, cfg)
	if err != nil {
		return ""
	}
	var r struct {
		DefaultBranch string `json:"defaultBranch"`
	}
	if json.Unmarshal(body, &r) != nil {
		return ""
	}
	return ShortBranch(r.DefaultBranch)
}

// repoTree — recursive listing, 10 dk cache (depo+branş anahtarlı).
// Yalnız blob'ların (dosya) yolu tutulur; ağaç düğümleri atılır.
func (s *Service) repoTree(ctx context.Context, cli *http.Client, cfg Settings, ver, repo, branch string) ([]string, error) {
	key := cfg.BaseURL + "|" + cfg.Collection + "|" + cfg.Project + "|" + repo + "@" + branch
	if paths, ok := s.code.get(key); ok {
		return paths, nil
	}
	u := repoURL(cfg, repo) + "/items?recursionLevel=Full" +
		"&versionDescriptor.versionType=branch&versionDescriptor.version=" + url.QueryEscape(branch) +
		"&api-version=" + ver
	body, err := doGetCapped(ctx, cli, u, cfg, treeBodyCap)
	if err != nil {
		return nil, err
	}
	var lr struct {
		Value []struct {
			Path          string `json:"path"`
			GitObjectType string `json:"gitObjectType"`
			IsFolder      bool   `json:"isFolder"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &lr); err != nil {
		// Kesilmiş gövde (tavana çarptı) burada patlar — dürüst mesaj.
		return nil, fmt.Errorf("depo ağacı çözümlenemedi (çok büyük ya da beklenmeyen yanıt)")
	}
	paths := make([]string, 0, len(lr.Value))
	for _, it := range lr.Value {
		if it.IsFolder || (it.GitObjectType != "" && it.GitObjectType != "blob") {
			continue
		}
		if it.Path == "" {
			continue
		}
		paths = append(paths, it.Path)
		if len(paths) >= treeMaxPaths {
			break
		}
	}
	s.code.put(key, paths)
	return paths, nil
}

// fetchItemContent — tek dosyanın metni. Önce JSON+includeContent
// (kanonik), o yol içerik döndürmezse $format=text.
//
// İki yol var çünkü eski TFS sürümleri includeContent'i sessizce
// yok sayıp içeriksiz metadata döndürebiliyor; "boş dosya" ile
// "bu sürüm bu parametreyi bilmiyor" ayırt edilemiyor.
func fetchItemContent(ctx context.Context, cli *http.Client, cfg Settings, ver, repo, branch, path string) (string, error) {
	base := repoURL(cfg, repo) + "/items?path=" + url.QueryEscape(path) +
		"&versionDescriptor.versionType=branch&versionDescriptor.version=" + url.QueryEscape(branch) +
		"&api-version=" + ver
	body, err := doGetCapped(ctx, cli, base+"&includeContent=true&$format=json", cfg, fileBodyCap)
	if err == nil {
		var r struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(body, &r) == nil && r.Content != "" {
			return r.Content, nil
		}
	}
	txt, terr := doGetText(ctx, cli, base+"&$format=text", cfg)
	if terr != nil {
		if err != nil {
			return "", err
		}
		return "", terr
	}
	return txt, nil
}

// doGetText — düz metin gövde çeken kardeş. doGet'in JSON muhafızı
// burada uygulanamaz (yanıt zaten text/plain), ama HTML sign-in
// sayfası muhafızı KALIR: 200 dönen bir giriş formu "dosya içeriği"
// diye modele gitmemeli.
func doGetText(ctx context.Context, cli *http.Client, rawURL string, cfg Settings) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "coremetry-devops/1.0")
	if cfg.PAT != "" || cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.PAT)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, fileBodyCap))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, firstLine(string(body)))
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "text/html") {
		return "", fmt.Errorf("dosya yerine HTML döndü — uç bir giriş sayfasının arkasında olabilir")
	}
	return string(body), nil
}

// reposURL — {base}/{collection}/{project}/_apis/git/repositories
// (koleksiyon kapsamlı depo KÖKÜ). Hem ada göre çözüm hem de
// v0.9.1236 kaçış kapısının listeleme çağrısı buradan türer; iki yer
// aynı yolu ayrı ayrı kursaydı biri diğerinden sessizce ayrışırdı.
func reposURL(cfg Settings) string {
	u := collectionURL(cfg)
	if p := strings.Trim(strings.TrimSpace(cfg.Project), "/"); p != "" {
		u += "/" + url.PathEscape(p)
	}
	return u + "/_apis/git/repositories"
}

// repoURL — {base}/{collection}/{project}/_apis/git/repositories/{repo}
func repoURL(cfg Settings, repo string) string {
	return reposURL(cfg) + "/" + url.PathEscape(strings.Trim(repo, "/"))
}

// apiVersionCandidates — denenecek api-version'lar, tekrarsız.
// Canlı yapılandırmada bir tespit varsa o BAŞA gelir: ayar sayfasında
// zaten çalıştığı görülmüş sürümü ikinci sıraya koymak her kod
// çekmesine bir reddedilmiş istek eklerdi.
func apiVersionCandidates(cfg Settings) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range candidateFlavors(cfg) {
		v := apiVersionFor(f)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ---------------------------------------------------------------
// Saf yardımcılar — tablo-testli (code_test.go). Ağ yok.
// ---------------------------------------------------------------

// BestPathForFrame — depo ağacındaki yollar arasından frame'in
// dosyasına en iyi eşleşen. Eşleşme yoksa "".
//
// Kural: yol, "/" + dosya adı ile BİTMELİ (sonek eşleşmesi) — böylece
// CardService.java, MyCardService.java'yı yakalamaz. Birden çok aday
// varsa frame'in PAKET YOLUNA en çok benzeyen kazanır; eşitlikte KISA
// yol (üretilmiş/gölge kopyalar genelde daha derinde durur), o da
// eşitse alfabetik — sonuç deterministik olmak zorunda, yoksa aynı
// exception iki tıkta iki farklı dosya gösterir.
func BestPathForFrame(paths []string, f stackparse.Frame) string {
	if f.File == "" {
		return ""
	}
	suffix := "/" + f.File
	pkg := f.PackagePath()
	best, bestScore := "", -1
	for _, p := range paths {
		if !strings.HasSuffix(p, suffix) && p != f.File {
			continue
		}
		sc := packageAffinity(p, pkg)
		switch {
		case sc > bestScore:
			best, bestScore = p, sc
		case sc == bestScore && best != "":
			if len(p) < len(best) || (len(p) == len(best) && p < best) {
				best = p
			}
		}
	}
	return best
}

// packageAffinity — yolun, paket yolunun kaç SONDAKİ parçasını arka
// arkaya taşıdığı. com/example/card ile
// /src/main/java/com/example/card/X.java → 3.
//
// Sondan sayılır çünkü depo kökü kuruluma göre değişir
// (src/main/java, app/src, modules/x/src/main/java); değişmeyen şey
// paketin dizin hiyerarşisine bire bir düşmesidir.
func packageAffinity(path, pkg string) int {
	if pkg == "" {
		return 0
	}
	dir := path
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i]
	}
	dirSegs := splitNonEmpty(dir, "/")
	pkgSegs := splitNonEmpty(pkg, "/")
	n := 0
	for n < len(dirSegs) && n < len(pkgSegs) {
		if dirSegs[len(dirSegs)-1-n] != pkgSegs[len(pkgSegs)-1-n] {
			break
		}
		n++
	}
	return n
}

func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// WindowAround — dosya içeriğinden `line` merkezli ±radius satırlık
// pencere; satırlar numaralandırılır ("  246| kod"). Numaralar şart:
// modelin "246. satırdaki null kontrolü" diyebilmesi, operatörün de
// cevabı dosyada bulabilmesi için.
//
// line dosyanın dışındaysa (kaynak stack'ten sonra değişmiş) pencere
// dosya sınırlarına kırpılır — boş dönmek yerine yakını göstermek
// daha faydalı, ve satır numaraları zaten gerçeği söylüyor.
func WindowAround(content string, line, radius int) CodeWindow {
	if strings.TrimSpace(content) == "" {
		return CodeWindow{}
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	// Sondaki tek boş satır dosya sonu newline'ıdır, satır sayılmaz.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) == 0 {
		return CodeWindow{}
	}
	if line <= 0 {
		line = 1
	}
	from, to := line-radius, line+radius
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from > len(lines) {
		from = len(lines)
	}
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%d| %s\n", i, lines[i-1])
	}
	return CodeWindow{FromLine: from, ToLine: to, Content: strings.TrimRight(b.String(), "\n")}
}

// ClampCodeWindows — pencereleri TOPLAM rune bütçesine sığdırır.
// trimmed=true: en az bir pencere kısaldı ya da tümüyle düştü.
//
// Sıra korunur: ilk pencere kök nedene en yakın olandır (AppFrames
// v0.9.1235'ten beri en derin "Caused by" segmentini başa koyuyor),
// bütçe daralınca düşecek olan SON penceredir — yani dıştaki
// wrapper/yeniden-fırlatma kodu. Kesme rune bazlı ve pencere içindeki
// SATIR sınırında yapılır — yarım satır kod, kod değildir.
func ClampCodeWindows(ws []CodeWindow, maxRunes int) ([]CodeWindow, bool) {
	if len(ws) == 0 {
		return nil, false
	}
	if maxRunes <= 0 {
		return nil, true
	}
	out := make([]CodeWindow, 0, len(ws))
	used, trimmed := 0, false
	for _, w := range ws {
		n := utf8.RuneCountInString(w.Content)
		if used+n <= maxRunes {
			out = append(out, w)
			used += n
			continue
		}
		trimmed = true
		left := maxRunes - used
		cut, lastLine := cutToLineBoundary(w.Content, left)
		if cut == "" {
			break // kalan bütçe tek satırı bile almıyor
		}
		w.Content = cut
		if lastLine > 0 {
			w.ToLine = lastLine
		}
		out = append(out, w)
		break
	}
	if len(out) < len(ws) {
		trimmed = true
	}
	return out, trimmed
}

// cutToLineBoundary — numaralı içeriği n rune'a, SATIR sınırında
// keser. İkinci dönen değer korunan son satırın numarası (0 =
// çıkarılamadı).
func cutToLineBoundary(content string, n int) (string, int) {
	if n <= 0 {
		return "", 0
	}
	var kept []string
	used, lastLine := 0, 0
	for _, ln := range strings.Split(content, "\n") {
		c := utf8.RuneCountInString(ln) + 1 // + satır sonu
		if used+c > n {
			break
		}
		kept = append(kept, ln)
		used += c
		if num, ok := lineNumberOf(ln); ok {
			lastLine = num
		}
	}
	if len(kept) == 0 {
		return "", 0
	}
	return strings.Join(kept, "\n"), lastLine
}

// lineNumberOf — "246| kod" → 246.
func lineNumberOf(ln string) (int, bool) {
	i := strings.Index(ln, "|")
	if i <= 0 {
		return 0, false
	}
	n := 0
	for _, r := range ln[:i] {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n > 0
}

// PromptBlock — kod bağlamının prompt'a giren metni. Boş bağlam → "".
//
// Blok bir BÜTÜN olarak taşınır: ai_calls maskeleyicisi bu metni
// prompt'un içinde tek parça bulup özetiyle değiştirir (emsal:
// clampDrawerEvidence'in LogsBlock'u aynı şekilde tek parça geçer).
func (c CodeContext) PromptBlock() string {
	if c.Empty() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nKOD BAĞLAMI (depo: %s", c.Repo)
	if c.Branch != "" {
		fmt.Fprintf(&b, ", branş: %s", c.Branch)
	}
	b.WriteString("):")
	for _, w := range c.Windows {
		fmt.Fprintf(&b, "\n\n%s (satır %d-%d) — %s\n```java\n%s\n```",
			w.Path, w.FromLine, w.ToLine, w.Frame, w.Content)
	}
	b.WriteString("\n\nKodu stack'le BİRLİKTE oku: hatanın atıldığı satırı göster ve kök nedeni o satırdaki koşula/çağrıya dayandır. Kodda görmediğin bir davranışı UYDURMA — pencere dışında kalan kısım hakkında \"bu pencerede görünmüyor\" de.")
	return b.String()
}

// LogSummary — maskeli ai_calls kaydına giren tek satırlık özet.
// Kod GÖVDESİ değil, yalnız nereden geldiği: `[kod: repo/dosya:aralık
// · N satır]`. Operatör hangi dosyanın modele gittiğini görür, kaynak
// kodun kendisi telemetri deposuna yazılmaz.
func (c CodeContext) LogSummary() string {
	if c.Empty() {
		return ""
	}
	parts := make([]string, 0, len(c.Windows))
	for _, w := range c.Windows {
		lines := w.ToLine - w.FromLine + 1
		if lines < 0 {
			lines = 0
		}
		parts = append(parts, fmt.Sprintf("[kod: %s%s:%d-%d · %d satır]",
			c.Repo, w.Path, w.FromLine, w.ToLine, lines))
	}
	return "\n\n" + strings.Join(parts, "\n")
}

// MaskCodeInPrompt — prompt'un LOG KOPYASINDA kod bloğunu özetiyle
// değiştirir. Saf.
//
// Sağlayıcıya giden gerçek prompt'a DOKUNMAZ — çağıran bunu yalnız
// ai_calls kaydı için üretir. block prompt'un içinde bulunamazsa
// prompt aynen döner: maskeleme bir "en iyi çaba" değil, bir
// sözleşmedir; bulunamadığında sessizce yanlış bir şey yazmaktansa
// hiçbir şey değiştirmemek doğrudur (çağıran zaten bloğu kendi
// eklemiştir).
func MaskCodeInPrompt(full, block, summary string) string {
	if block == "" || full == "" || !strings.Contains(full, block) {
		return full
	}
	return strings.Replace(full, block, summary, 1)
}
