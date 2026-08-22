package devops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
// satırı. En fazla 3 PENCERE (aday frame değil, v0.9.1237), toplam
// ~4.000 rune.
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
	// codeWindowLimit — kaç pencere KESİLİNCE av biter.
	// 3: hata satırı + onu çağıran + bir üstü. Daha fazlası gemma4'ün
	// bağlamında kod-dışı kanıtı (trace, log) sıkıştırmaya başlar.
	//
	// v0.9.1237 — bu sayı ADAYLARI kırpıyordu (AppFrames(frames, 3)).
	// Üç ıska — ağaçta olmayan dosya, okunamayan içerik — avı
	// bitiriyordu; dördüncü frame isabet edecekken hiç denenmiyordu.
	// Tavan artık ÇIKTIYA bakıyor: 3 pencere kesilene kadar aday
	// listesi yürünür. Sayının kendisi (3) ve gerekçesi değişmedi.
	codeWindowLimit = 3
	// codeCandidateLimit — AppFrames'ten alınan aday frame sayısı.
	// 10: derin bir JBoss stack'inde kök neden segmentinin uygulama
	// frame'leri bu aralığa rahat sığar (AppFrames v0.9.1235'ten beri
	// en derin "Caused by" segmentini başa koyuyor, yani sıra zaten
	// doğru); daha uzun bir liste sabır tavanına nasılsa takılır.
	codeCandidateLimit = 10
	// codeLookupLimit — toplam DENEME tavanı. Iska bütçe harcamaz ama
	// SABIR harcar: 60.000 yollu bir ağaçta on frame'i tek tek arayıp
	// her birinde ıskalayan patolojik bir stack açıklamayı bekletmesin.
	// 6 = 3 pencere + 3 ıska payı.
	codeLookupLimit = 6
	// codeFetchDeadline — FetchCode'un TOPLAM süre tavanı (v0.9.1237).
	// İstek başına 20 sn'lik client tavanı vardı ama N ARDIŞIK isteğin
	// tavanı yoktu: kara delik bir host'ta zincir (2 refs + varsayılan
	// branş + ağaç + frame başına 2 dosya çağrısı) dakikalara çıkabilir.
	// Üstelik buildCodeContext deliverExplain'den ÖNCE koşar — o süre
	// boyunca tek bayt yazılmaz ve 60 sn'lik ingress proxy_read_timeout
	// tam burada bağlantıyı keserek fail-open sözleşmesini boşa çıkarır.
	// 25 sn: takılan TEK isteğin 20 sn'si içeri sığar, ingress tavanına
	// 35 sn pay kalır. Tavan dolarsa elde ne varsa o döner — kısmi
	// sonuç, hiçbir şeyden iyidir.
	codeFetchDeadline = 25 * time.Second
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
	// Segment — frame'in "Caused by" zincirindeki derinliği
	// (stackparse.Frame.Segment, v0.9.1235). 0 = en dış wrapper
	// exception, 1+ = Caused by bölümleri. v0.9.1239'dan beri prompt
	// başlığına yazılıyor: pencere sırası kök-nedene göre kuruluyor
	// (AppFrames en derini başa alır) ama modele bu SIRA hiç
	// söylenmiyordu — üç pencereyi eşit ağırlıkta okuyordu.
	Segment int `json:"segment,omitempty"`
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
	// Outcome — bu denemenin SINIFI (v0.9.1243), sayaçlarla BİREBİR
	// aynı taksonomi. v0.9.1241'de sınıf yalnız sayaca gidiyordu; tek
	// bir çağrının kaydına (ai_calls maskeli kopyası) hangi çıkmaza
	// düşüldüğü yazılamıyordu. Sınıfı Reason METNİNDEN çıkarmak
	// alternatifti ve reddedildi: metin operatöre hitap eden, serbestçe
	// değişen bir cümle; sınıf bir sözleşme.
	Outcome CodeOutcome `json:"outcome,omitempty"`
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
// v0.9.1243 — küçültme KAYIPTIR ve kayda öyle yazılır. Öncesinde
// yeniden denemenin maskeli kopyası, ilk denemeninkiyle aynı dilde
// "kod geldi" diyordu; oysa modele giden pencereler kırpılmış ya da
// düşmüştü. Not BAŞA yazılıyor: gerekçe tavana çarparsa kesilecek olan
// eski kuyruk olsun, yeni kayıp değil.
func (c CodeContext) Halved() CodeContext {
	if c.Empty() {
		return c
	}
	before := len(c.Windows)
	var trimmed bool
	c.Windows, trimmed = ClampCodeWindows(c.Windows, codeBudgetRunes/2)
	if dropped := before - len(c.Windows); trimmed || dropped > 0 {
		note := "bağlam taşması — kod bütçesi yarıya indirildi"
		if dropped > 0 {
			note += fmt.Sprintf(", %d pencere düştü", dropped)
		}
		c.Outcome = CodePartial
		c.Reason = withNote(note, c.Reason)
	}
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
// hint (v0.9.1183, v0.9.1240'ta yapılandırıldı) — proje ÖNERİSİ +
// önerinin kaynağı, öneri boşsa çıkmazın nedeni. Ayardaki açık Project
// boşsa kullanılır. Operatör isteği: "service_name başında bsa-
// yazıyorsa direkt project BSA olduğunu anlasın."
//
// v0.9.1241 — her çıkış SAYILIR. Dönüş DEĞERİ adlandırıldı ve sayaç
// tek bir defer'den geçiyor: on dört çıkışın her birine elle sayaç
// eklemek, on beşinci eklendiğinde sessizce eksik kalırdı. Sınıfı
// atamayan bir dal "other" kovasına düşer — görünür bir eksik, sessiz
// bir "ok" değil.
func (s *Service) FetchCode(ctx context.Context, repo string, hint ProjectHint, frames []stackparse.Frame) (out CodeContext) {
	class := CodeOther
	// v0.9.1243 — sınıf sayaca GİDERKEN bağlamın kendisine de yazılır.
	// Aynı defer BİLİNÇLİ: on dört çıkışın sınıfı zaten burada tek
	// yerde okunuyor, ikinci bir yazım noktası açmak ilk yeni çıkışta
	// sayaç ile kayıt arasında sessiz bir ayrışma demekti.
	defer func() {
		out.Outcome = class
		s.RecordCodeOutcome(class, out.Reason)
	}()
	out = CodeContext{Repo: repo}
	if s == nil {
		class = CodeUnconfigured
		return CodeContext{Reason: "DevOps istemcisi yok"}
	}
	if strings.TrimSpace(repo) == "" {
		class = CodeRepoUnresolved
		return CodeContext{Reason: "servis için depo çözülemedi"}
	}
	cfg := s.CurrentSettings()
	if strings.TrimSpace(cfg.BaseURL) == "" {
		class = CodeUnconfigured
		return CodeContext{Repo: repo, Reason: "DevOps bağlantısı yapılandırılmamış (Ayarlar → Kod entegrasyonu)"}
	}
	// v0.9.1237 — TOPLAM süre tavanı. parent AYRI tutulur: tavanın
	// dolmasıyla çağıranın (tarayıcı gitti) vazgeçmesini ayırt etmeden
	// "DevOps 25 sn'de yanıt vermedi" demek yanlış suçlama olurdu.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, s.fetchDeadline())
	defer cancel()
	// v0.9.1183 — proje ayarda boşsa servis önekinden TÜRETİLİR
	// (bsa-… → BSA). Açık ayar HER ZAMAN kazanır: türetme bir tahmin,
	// operatörün yazdığı ad bir karar (ResolveRepo'daki pin sözleşmesinin
	// aynısı). Önceden burası kesin bir duvardı ve kurulumun kendi
	// adlandırma sözleşmesi zaten cevabı taşırken operatörden aynı bilgiyi
	// ikinci kez istiyordu.
	project, _, dead := pickProject(cfg, hint)
	if dead != "" {
		// Depo ADIYLA çağırıyoruz; ada göre çözüm proje kapsamı ister.
		class = CodeProjectDeadEnd
		return CodeContext{Repo: repo, Reason: dead}
	}
	cfg.Project = project
	// v0.9.1235 — AppFrames artık EN DERİN "Caused by" segmentinden dışa
	// doğru seçiyor: üç pencerenin ilki kök nedenin fırlatıldığı satır,
	// wrapper'ın yeniden-fırlatma satırları arta kalan bütçeye düşüyor.
	// v0.9.1237 — aday listesi GENİŞ (10), tavan çıktıda (3 pencere).
	targets := stackparse.AppFrames(frames, codeCandidateLimit)
	if len(targets) == 0 {
		// v0.9.1264 (denetim [3/XS]) — tek cümle ÜÇ farklı vazgeçişi
		// katıyordu ve üçünün operatör aksiyonu farklı: (a) hiç frame
		// çözülemedi → stack Java kalıbında değil; (b) frame'ler var ama
		// hepsi çerçeve/JDK → uygulama kodu enstrümante değil ya da
		// paket önekleri app sayılmıyor; (c) uygulama frame'i var ama
		// dosya/satır taşımıyor → debug bilgisi olmadan derlenmiş.
		class = CodeNoStack
		return CodeContext{Repo: repo, Reason: frameGiveUpReason(frames)}
	}

	cli := s.clientFor(cfg.InsecureSkipVerify)
	ch := s.resolveChain(ctx, parent, cli, cfg, repo)
	repo, out.Repo, out.Branch = ch.repo, ch.repo, ch.branch
	if ch.class != "" {
		class = ch.class
		return CodeContext{Repo: ch.repo, Branch: ch.branch, Reason: ch.reason}
	}
	// note — düzeltme izi. Boş kalırsa hiçbir şey değişmedi demektir.
	note, ver, branch, paths := ch.note, ch.ver, ch.branch, ch.paths

	hunt := huntWindows(ctx, targets,
		huntLimits{windows: codeWindowLimit, lookups: codeLookupLimit, radius: codeWindowRadius},
		func(f stackparse.Frame) string { return BestPathForFrame(paths, f) },
		func(c context.Context, p string) (string, error) {
			return fetchItemContent(c, cli, cfg, ver, repo, branch, p)
		})

	windows, trimmed := ClampCodeWindows(hunt.windows, codeBudgetRunes)
	out.Windows = windows
	ours := deadlineHit(parent, ctx)
	switch {
	case len(windows) == 0 && hunt.timedOut && ours:
		class = CodeDeadline
		out.Reason = deadlineReason(s.fetchDeadline())
	case len(windows) == 0 && hunt.timedOut:
		class = CodeCancelled
		out.Reason = "istek iptal edildi — kod bağlamı toplanamadı"
	case len(windows) == 0 && len(hunt.misses) > 0:
		class = CodeTreeMiss
		out.Reason = "ağaçta eşleşen dosya yok: " + strings.Join(hunt.misses, ", ")
	case len(windows) == 0:
		class = CodeWindowFailed
		out.Reason = "kod penceresi kurulamadı"
	case trimmed && len(hunt.windows) > len(windows):
		// v0.9.1239 — DÜŞEN pencere ayrı söylenir. Kesme artık hata
		// satırını merkezde tutuyor; sığdıramadığı pencereyi kırpmak
		// yerine düşürüyor ve bu, "kısaltıldı" ile aynı cümleye
		// sığmayacak kadar farklı bir kayıp.
		out.Reason = fmt.Sprintf("kod bütçesi (%d karakter) doldu — %d pencere düştü, kalanlar hata satırı çevresinde kısaltıldı",
			codeBudgetRunes, len(hunt.windows)-len(windows))
	case trimmed:
		out.Reason = fmt.Sprintf("kod bütçesi (%d karakter) doldu — pencereler hata satırı çevresinde kısaltıldı", codeBudgetRunes)
	}
	// v0.9.1241 — TAM/KISMİ ayrımı KAYIPTAN türetilir, Reason
	// METNİNDEN değil. Başarılı bir çekmede de not olabiliyor (depo
	// adı düzeltme izi, v0.9.1236) ve "reason doluysa kısmi" demek
	// tertemiz bir isabeti kısmi göstererek oranı haksızca düşürürdü.
	// Kayıp = bütçe kesmesi, ıska, süre ya da deneme tavanı. Pencere
	// TAVANINA çarpmak (hunt.untried) kayıp DEĞİLDİR: üç pencere
	// zaten istenen cevabın kendisi.
	if len(windows) > 0 {
		class = CodeOK
		if trimmed || len(hunt.misses) > 0 || hunt.timedOut || hunt.patience {
			class = CodePartial
		}
	}
	// v0.9.1237 — KISMİ sonuç da rapor edilir. En az bir pencere
	// kesildiğinde eski kod susuyordu: ıskalanan frame'ler ve tavana
	// çarpma hiçbir yere yazılmıyor, "kod geldi" cevabının yanında
	// NEYİN gelmediği görünmüyordu.
	out.Reason = withNote(out.Reason, hunt.note(len(windows), len(targets), cutoffLabel(ours, s.fetchDeadline())))
	// v0.9.1236 — düzeltme izi BAŞARILI çekmede de kalır. Kod geldi
	// diye susmak, operatörün katalogdaki/konvansiyondaki yanlış adı
	// hiç öğrenmemesi demek olurdu; ekrandaki "Kaynak: <depo>" satırı
	// beklediğinden farklı bir ad gösterirken sebebi de söylemeli.
	out.Reason = withNote(note, out.Reason)
	return out
}

// ProjectSourceSettings — projenin Ayarlar'daki açık alandan geldiğini
// söyleyen kaynak etiketi (v0.9.1242). Öbür iki değer RepoSourcePin /
// RepoSourceConvention; üçü birlikte "bu proje adı NEREDEN geldi"
// sorusunun tüm cevap kümesi.
const ProjectSourceSettings = "settings"

// pickProject — yürürlükteki proje adı + KAYNAĞI; ikisi de boşsa
// çıkmazın gerekçesi. SAF; tablo-testli (v0.9.1242'de FetchCode'un
// gövdesinden çıkarıldı — davranış AYNEN korundu).
//
// Sıra pazarlıksız: Ayarlar'daki açık Project HER ZAMAN kazanır,
// öneri (pin bileşeni / önek türetimi) yalnız o boşken kullanılır —
// türetme bir tahmin, operatörün yazdığı ad bir karar.
//
// Kaynak etiketi v0.9.1242'de eklendi: dry-run ekranında "proje: BSA"
// tek başına yetmiyor, operatörün asıl sorusu "bunu nereden buldun"
// (Ayarlar mı, pin mi, önek mi) — üçünün düzeltmesi üç ayrı yerde.
// FetchCode etiketi kullanmaz; ona yalnız adın kendisi lazım.
func pickProject(cfg Settings, hint ProjectHint) (project, source, deadEnd string) {
	if p := strings.TrimSpace(cfg.Project); p != "" {
		return p, ProjectSourceSettings, ""
	}
	if p := strings.TrimSpace(hint.Value); p != "" {
		return p, hint.Source, ""
	}
	return "", "", projectDeadEnd(hint)
}

// chainResult — AĞ zincirinin (branş + kaçış kapısı + ağaç) ürünü.
//
// class doluysa zincir ÇIKMAZA düştü ve reason onun gerekçesidir;
// boşsa ver/branch/paths kullanılabilir.
type chainResult struct {
	ver    string
	repo   string // sunucudan düzeltildiyse KANONİK ad
	branch string
	note   string // düzeltme izi ("" = düzeltme yok)
	paths  []string
	// at — zincirin DURDUĞU adım (chainStepBranch | chainStepTree).
	// class boşken anlamsız. Dry-run hangi adımın kırmızı yanacağını
	// buradan okur: durumdan ÇIKARMAK ("branch boşsa branş adımı
	// patlamıştır") ilk yeniden yazımda sessizce kayardı.
	at     string
	class  CodeOutcome // "" = zincir tamamlandı
	reason string
}

// Zincir adımlarının adları — dry-run ve testler bunları okur.
const (
	chainStepBranch = "branch"
	chainStepTree   = "tree"
)

// resolveChain — depo adı → branş → ağaç. FetchCode'un ağ zinciri,
// TEK yazımda (v0.9.1242).
//
// Neden ayrı: Ayarlar'daki "çözümü dene" (resolve_dryrun.go) TAM
// OLARAK bu adımları koşmak zorunda. Kopya bir zincir yazmak, iki
// gövdenin ilk düzeltmede ayrışması ve dry-run'ın gerçekte olmayan
// bir davranışı "doğrulaması" demekti — bir teşhis aracının
// yapabileceği en pahalı hata. Buradan sonrası (pencere avı) yalnız
// FetchCode'a ait; dry-run zinciri burada BİTİRİR ve ağaçtaki dosya
// SAYISINDAN başka bir şey okumaz.
//
// Sayaçlara dokunmaz: RecordCodeOutcome hâlâ YALNIZ FetchCode'un
// defer'ında. Dry-run bu fonksiyonu çağırır, FetchCode'u değil —
// isabet oranı yapay denemelerle şişmez/sönmez.
func (s *Service) resolveChain(ctx, parent context.Context, cli *http.Client, cfg Settings, repo string) chainResult {
	res := chainResult{repo: repo}
	ver, branch, err := s.resolveBranch(ctx, cli, cfg, repo)
	if err != nil {
		// KAÇIŞ KAPISI (v0.9.1236). Konvansiyon küçük harf üretir,
		// gerçek depo başka yazımda olabilir. Liste çağrısı YALNIZ
		// burada: mutlu yol tek fazladan istek bile görmez.
		if canon, near := s.recoverRepoName(ctx, cli, cfg, repo, err); canon != "" {
			if ver2, branch2, err2 := s.resolveBranch(ctx, cli, cfg, canon); err2 == nil {
				res.note = "depo adı sunucudan düzeltildi: " + repo + " → " + canon
				res.repo, ver, branch, err = canon, ver2, branch2, nil
			}
		} else if len(near) > 0 {
			err = fmt.Errorf("%v (sunucudaki en yakın adlar: %s)", err, strings.Join(near, ", "))
		}
		if err != nil {
			res.at = chainStepBranch
			if deadlineHit(parent, ctx) {
				res.class, res.reason = CodeDeadline, withNote(res.note, deadlineReason(s.fetchDeadline()))
				return res
			}
			res.class, res.reason = CodeBackendError, sanitize(err.Error(), cfg)
			return res
		}
	}
	res.ver, res.branch = ver, branch

	paths, err := s.repoTree(ctx, cli, cfg, ver, res.repo, branch)
	if err != nil {
		res.at = chainStepTree
		if deadlineHit(parent, ctx) {
			res.class, res.reason = CodeDeadline, withNote(res.note, deadlineReason(s.fetchDeadline()))
			return res
		}
		res.class, res.reason = CodeBackendError, withNote(res.note, sanitize(err.Error(), cfg))
		return res
	}
	if len(paths) == 0 {
		// v0.9.1183 — NE DENENDİĞİ yazılıyor. Proje artık türetilebiliyor
		// (bsa-… → BSA) ve türetme bir tahmin; "depo ağacı boş döndü"
		// tek başına operatöre yanlış tahmini göstermez, oysa hatanın en
		// olası sebebi tam olarak yanlış proje/depo adıdır (ör. gerçek depo
		// farklı harf yazımında). Katalogdaki Repository pini bunu ezer.
		res.at, res.class = chainStepTree, CodeEmptyTree
		res.reason = withNote(res.note,
			"depo ağacı boş döndü (proje "+cfg.Project+", depo "+res.repo+", branş "+branch+")")
		return res
	}
	res.paths = paths
	return res
}

// fetchDeadline — yürürlükteki toplam süre tavanı.
func (s *Service) fetchDeadline() time.Duration {
	if s.codeDeadline > 0 {
		return s.codeDeadline
	}
	return codeFetchDeadline
}

// deadlineHit — duran şey BİZİM tavanımız mı? parent hâlâ canlıyken
// türetilmiş ctx'in DeadlineExceeded olması tek kesin işarettir;
// çağıran vazgeçtiyse (tarayıcı kapandı) parent de hatalıdır ve suçu
// DevOps'a yıkmayız.
func deadlineHit(parent, ctx context.Context) bool {
	return parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// cutoffLabel — döngüyü kesen şeyin ADI. ours=false ise kesen biz
// değiliz (tarayıcı gitti, üst ctx düştü): "süre tavanı" yazmak
// operatörü olmayan bir DevOps yavaşlığının peşine düşürürdü.
func cutoffLabel(ours bool, dl time.Duration) string {
	if ours {
		return fmt.Sprintf("süre tavanı (%s)", dl)
	}
	return "istek iptal edildi"
}

// deadlineReason — süre tavanının insan-okunur karşılığı. Süre
// PARAMETRE: ekranda yazan sayı, gerçekten uygulanan tavan olmalı.
func deadlineReason(dl time.Duration) string {
	return fmt.Sprintf("DevOps %s içinde yanıt vermedi — kodsuz analiz", dl)
}

// huntLimits — döngü disiplininin üç vidası.
type huntLimits struct {
	windows int // kaç pencere kesilince durulur
	lookups int // kaç deneme sonrası sabır biter
	radius  int // pencere yarıçapı (satır)
}

// huntOutcome — döngünün ürünü + NEDEN durduğu.
type huntOutcome struct {
	windows  []CodeWindow
	misses   []string
	dupes    int  // birebir tekrar olduğu için hiç denenmeyen frame
	untried  int  // tavana çarpıldığı için denenmeyen aday
	patience bool // deneme tavanı doldu
	timedOut bool // süre tavanı doldu
}

// huntWindows — kod çekme döngüsünün çekirdeği (v0.9.1237). Ağı
// BİLMEZ: yol çözümü ve içerik çekimi enjekte edilir, böylece "hangi
// frame denenir, ne zaman durulur, ne rapor edilir" kararı tablo
// testlenebilir kalır.
//
// Üç kural:
//
//  1. TAVAN ÇIKTIDA. windows tavanı KESİLEN pencereyi sayar, denenen
//     frame'i değil. Iska avı bitirmez; dördüncü frame'in isabet etme
//     ihtimali üçüncünün ıskasına kurban gitmez.
//  2. TEKRAR İSTEK ETMEZ. Birebir aynı (dosya,satır) frame'i —
//     özyineleme ve wrapper kalıplarında sık — atlanır: ne istek ne
//     sabır harcar, ve aynı pencerenin kopyası bütçeyi yiyip daha
//     derin bir frame'i dışarı itmez. Aynı DOSYANIN başka satırı ise
//     eldeki içerikten kesilir; ikinci bir GET yok.
//  3. IŞKA SABIR HARCAR. Denenen her frame sabırdan düşer; lookups
//     tavanı, ağaçta hiçbir şeyi tutmayan patolojik bir stack'in
//     açıklamayı bekletmesini engeller.
func huntWindows(
	ctx context.Context,
	targets []stackparse.Frame,
	lim huntLimits,
	find func(stackparse.Frame) string,
	fetch func(context.Context, string) (string, error),
) huntOutcome {
	var out huntOutcome
	tried := map[string]bool{}    // (dosya,satır) — birebir tekrar muhafızı
	bodies := map[string]string{} // yol → içerik (aynı dosya, başka satır)
	lookups := 0
	for i, f := range targets {
		if len(out.windows) >= lim.windows {
			out.untried = len(targets) - i
			break
		}
		if lookups >= lim.lookups {
			out.untried, out.patience = len(targets)-i, true
			break
		}
		if ctx.Err() != nil {
			out.untried, out.timedOut = len(targets)-i, true
			break
		}
		key := f.File + ":" + strconv.Itoa(f.Line)
		if tried[key] {
			out.dupes++
			continue
		}
		tried[key] = true
		lookups++

		p := find(f)
		if p == "" {
			out.misses = append(out.misses, f.File)
			continue
		}
		body, cached := bodies[p]
		if !cached {
			b, ferr := fetch(ctx, p)
			if ferr != nil {
				// Süre tavanı bir ıska DEĞİLDİR: dosya orada, biz
				// bekleyemedik. Ayırmazsak "ağaçta eşleşen dosya yok"
				// diye yanlış teşhis yazardık.
				if ctx.Err() != nil {
					out.untried, out.timedOut = len(targets)-i-1, true
					break
				}
				out.misses = append(out.misses, f.File+" (okunamadı)")
				continue
			}
			bodies[p], body = b, b
		}
		w := WindowAround(body, f.Line, lim.radius)
		if w.Content == "" {
			out.misses = append(out.misses, f.File+" (satır aralığı boş)")
			continue
		}
		w.Path, w.Frame, w.Line, w.Segment = p, f.String(), f.Line, f.Segment
		out.windows = append(out.windows, w)
	}
	return out
}

// note — KISMİ sonucun dürüst özeti. kept: bütçeden sonra ELDE KALAN
// pencere sayısı; total: aday frame sayısı.
//
// Pencere hiç yoksa boş döner — o hâli çağıran zaten daha açık bir
// cümleyle anlatıyor; buranın işi "kod geldi ama şu eksik" demek.
// Tekrar atlanan frame'ler rapor edilmez: onlar kayıp değil, tasarruf.
func (h huntOutcome) note(kept, total int, cutoff string) string {
	if kept == 0 {
		return ""
	}
	var parts []string
	if h.timedOut {
		parts = append(parts, fmt.Sprintf("%s: %d adaydan %d pencere kesildi",
			cutoff, total, kept))
	}
	if h.patience {
		parts = append(parts, fmt.Sprintf("deneme tavanı (%d) doldu — %d frame denenmedi",
			codeLookupLimit, h.untried))
	}
	if len(h.misses) > 0 {
		parts = append(parts, "eşleşmeyen: "+strings.Join(h.misses, ", "))
	}
	return strings.Join(parts, " · ")
}

// resolveBranch — refs API'sinden branşları çeker ve ayardaki sıraya
// göre seçer; hiçbiri yoksa deponun VARSAYILAN branşına düşer.
// Dönen ilk değer, bu depo için çalışan api-version'dur.
func (s *Service) resolveBranch(ctx context.Context, cli *http.Client, cfg Settings, repo string) (string, string, error) {
	order := s.ResolveConfig().BranchOrder
	var firstErr error
	for _, ver := range s.apiVersionCandidates(cfg) {
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
//
// v0.9.1263 — yorum v0.9.829'dan beri bunu VAADEDİYORDU ama fonksiyon
// receiver'sızdı ve tespiti (s.detVersion) hiç okumuyordu: auto
// flavor'da her kod çekmesi bir mahkûm istekle başlıyordu (denetim
// [2/XS]: "yorum ile kod çelişiyor"). Artık Service methodu; tespit
// kilit altında okunur ve listenin başına alınır. detVersion boşsa
// davranış bayt-bayt eski (saf çekirdek apiVersionOrder testli).
func (s *Service) apiVersionCandidates(cfg Settings) []string {
	det := ""
	if s != nil {
		s.mu.RLock()
		if s.cfg.BaseURL == cfg.BaseURL && s.cfg.Collection == cfg.Collection {
			det = s.detVersion
		}
		s.mu.RUnlock()
	}
	return apiVersionOrder(cfg, det)
}

// apiVersionOrder — saf çekirdek: aday sıralama + tespit öne alma.
func apiVersionOrder(cfg Settings, detected string) []string {
	var out []string
	seen := map[string]bool{}
	if detected != "" {
		seen[detected] = true
		out = append(out, detected)
	}
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
//
// # Kesme MERKEZDEN yapılır (v0.9.1239)
//
// Pencere hata satırının ÜSTÜNDEN başlar (line-30). Kırpma baştan
// saymayla yapılırsa — v0.9.1239 öncesi hâli — kalan bütçe pencerenin
// yarısından azken korunan satırlar hata satırına VARMADAN biter:
// prompt başlığı hâlâ "… (Y.java:246)" diye satırı gösterir, o satır
// pencerede YOKTUR. Küçük model bunu "246 bu blokta bir yerde" diye
// okuyup gördüğü rastgele satırdan kök neden uydurur. Halved() bütçeyi
// 2000'e indirdiğinde ilk pencere bile merkezini kaybediyordu, yani
// taşma yeniden-denemesi tam da en çok kanıt gereken anda pencereyi
// işe yaramaz hâle getiriyordu.
//
// Artık kırpma frame satırı MERKEZDE kalacak şekilde iki yandan
// daraltılır. Hata satırı tek başına bile bütçeye sığmıyorsa pencere
// DÜŞER: kullanılamaz bir pencere, hiç pencere olmamasından kötüdür
// (çağıran düşüşü Reason'a yazar). Line=0 olan pencerede (frame satırı
// bilinmiyor) eski baştan-kesme davranışı korunur.
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
		if w.Line > 0 {
			cut, from, to := centerToBudget(w.Content, w.Line, left)
			if cut == "" {
				// Hata satırı bile sığmıyor → pencereyi hiç gönderme.
				break
			}
			w.Content = cut
			if from > 0 {
				w.FromLine = from
			}
			if to > 0 {
				w.ToLine = to
			}
			out = append(out, w)
			break
		}
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

// centerToBudget — numaralı içeriği n rune'a, `line` numaralı satır
// MERKEZDE kalacak şekilde daraltır. Dönen: kesilmiş içerik + korunan
// ilk/son satır numaraları. İçerik "" ise pencere kullanılamaz
// (frame satırı yok ya da tek başına bütçeye sığmıyor) — çağıran onu
// düşürmeli.
//
// Genişleme dönüşümlü, ÖNCE YUKARI: hata satırının üstündeki koşul ve
// atamalar kök nedeni okumak için altındaki satırlardan daha
// değerlidir, tek satırlık artan bütçe oraya gitsin.
func centerToBudget(content string, line, n int) (string, int, int) {
	if n <= 0 || line <= 0 || content == "" {
		return "", 0, 0
	}
	lines := strings.Split(content, "\n")
	center := -1
	for i, ln := range lines {
		if num, ok := lineNumberOf(ln); ok && num == line {
			center = i
			break
		}
	}
	if center < 0 {
		return "", 0, 0
	}
	// Satır maliyeti cutToLineBoundary ile AYNI sayılır (rune + satır
	// sonu): iki kesici aynı bütçeyi farklı sayarsa toplam tavan kayar.
	cost := func(i int) int { return utf8.RuneCountInString(lines[i]) + 1 }
	used := cost(center)
	if used > n {
		return "", 0, 0
	}
	lo, hi := center, center
	for {
		grew := false
		if lo > 0 && used+cost(lo-1) <= n {
			lo--
			used += cost(lo)
			grew = true
		}
		if hi < len(lines)-1 && used+cost(hi+1) <= n {
			hi++
			used += cost(hi)
			grew = true
		}
		if !grew {
			break
		}
	}
	kept := lines[lo : hi+1]
	from, _ := lineNumberOf(kept[0])
	to, _ := lineNumberOf(kept[len(kept)-1])
	return strings.Join(kept, "\n"), from, to
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
// v0.9.1239 — pencere içi İŞARET + konum etiketi + dile göre çit.
// Üçü de aynı boşluğu kapatıyor: modelin pencerede NEYE bakacağı.
// Öncesinde her satır tıpatıp aynı ("246| kod") görünüyordu ve
// pencereler sırasız bir yığındı; 4B'lik bir modelden başlıktaki
// ":246" ile satır önekini kendi eşleştirmesi ve pencere sırasının
// kök-nedene göre kurulduğunu tahmin etmesi bekleniyordu.
func (c CodeContext) PromptBlock() string {
	if c.Empty() {
		return ""
	}
	deepest := 0
	for _, w := range c.Windows {
		if w.Segment > deepest {
			deepest = w.Segment
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nKOD BAĞLAMI (depo: %s", c.Repo)
	if c.Branch != "" {
		fmt.Fprintf(&b, ", branş: %s", c.Branch)
	}
	b.WriteString("). Satır başındaki sayı GERÇEK dosya satırıdır; " +
		frameMarker + " ile işaretli satır stack'in gösterdiği hata satırıdır — analizini oradan başlat:")
	for i, w := range c.Windows {
		fmt.Fprintf(&b, "\n\npencere %d/%d%s\n%s (satır %d-%d) — %s\n```%s\n%s\n```",
			i+1, len(c.Windows), segmentLabel(w.Segment, deepest),
			w.Path, w.FromLine, w.ToLine, w.Frame,
			fenceLang(w.Path), markFrameLine(w.Content, w.Line))
	}
	b.WriteString("\n\nKodu stack'le BİRLİKTE oku: hatanın atıldığı satırı göster ve kök nedeni o satırdaki koşula/çağrıya dayandır. Kodda görmediğin bir davranışı UYDURMA — pencere dışında kalan kısım hakkında \"bu pencerede görünmüyor\" de.")
	return b.String()
}

// frameMarker — hata satırının pencere içi işareti. TEK yazım: hem
// markFrameLine hem başlıktaki açıklama buradan okur, yoksa model
// tarif edilmeyen bir işaretle karşılaşır.
const frameMarker = ">>>"

// markFrameLine — numaralı içerikte `line` satırının başına işaret
// koyar. Saf; satır bulunamazsa içerik AYNEN döner.
//
// İşaret render anında ekleniyor, CodeWindow.Content'e YAZILMIYOR:
// Content kanonik kalmalı — bütçe kesicisi (centerToBudget /
// cutToLineBoundary) ve lineNumberOf satır numarasını satır BAŞINDAN
// okuyor, frontend de aynı içeriği kendi biçimiyle gösteriyor.
func markFrameLine(content string, line int) string {
	if line <= 0 || content == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		if num, ok := lineNumberOf(ln); ok && num == line {
			lines[i] = frameMarker + " " + ln
			return strings.Join(lines, "\n")
		}
	}
	return content
}

// segmentLabel — pencerenin "Caused by" zincirindeki yeri. Boş dize =
// söylenecek bir şey yok (zincirsiz stack).
//
// deepest, ELDEKİ pencerelerin en derin segmentidir; kök neden
// segmentinden pencere kesilemediyse (dosya ağaçta yok) hiçbir
// pencereye "kök neden" demeyiz — modele olmayan bir istihkakı var
// diye okutmak, işaretsiz bırakmaktan kötüdür.
func segmentLabel(seg, deepest int) string {
	switch {
	case deepest == 0:
		return "" // tek segmentli stack: zincir yok
	case seg == deepest:
		return fmt.Sprintf(" — kök neden segmenti (Caused by #%d)", seg)
	case seg == 0:
		return " — dış (wrapper) exception"
	default:
		return fmt.Sprintf(" — Caused by #%d", seg)
	}
}

// fenceLang — kod çitinin dil etiketi, dosya UZANTISINDAN. Bilinmeyen
// uzantı → etiketsiz çit; yanlış dil etiketi, etiketsizden kötüdür.
//
// stackparse.ParseJava Java/Kotlin/Scala stack'lerini birlikte
// çözüyor ve BestPathForFrame uzantıya bakmadan eşleştiriyor, yani
// .kt/.scala pencereleri buraya GELİYOR; hepsi ```java diye
// etiketleniyordu.
func fenceLang(path string) string {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return ""
	}
	switch strings.ToLower(path[i:]) {
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".scala", ".sc":
		return "scala"
	case ".groovy":
		return "groovy"
	default:
		return ""
	}
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
	// v0.9.1243 — KISMİ isabette kayıp da yazılır. Pencere listesi tek
	// başına "kod geldi" der ve NEYİN gelmediğini gizler: üç frame'den
	// biri ağaçta bulunamadıysa ya da bütçe bir pencereyi düşürdüyse,
	// kayda bakan operatör modelin eksik kanıtla konuştuğunu göremezdi.
	// Sınıf CodePartial'dan okunuyor, Reason metninden değil (v0.9.1241
	// ayrımı: başarılı çekmede de not olabiliyor).
	if c.Outcome == CodePartial {
		if note := capRunes(c.Reason, codeNoteRuneCap); note != "" {
			parts[len(parts)-1] += " (kısmi: " + note + ")"
		}
	}
	return "\n\n" + strings.Join(parts, "\n")
}

// codeNoteRuneCap — maskeli kayda giren serbest metnin rune tavanı.
// Rune (bayt değil): Türkçe gerekçeler çok baytlı karakter taşıyor ve
// bayt sayarak kesmek UTF-8 dizisini ortasından bölerdi.
const codeNoteRuneCap = 120

// capRunes — metni rune tavanına indirir; kesildiyse "…" ekler. Saf.
func capRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

// LogMissSummary — "kod İSTENDİ ama gelmedi" işareti (v0.9.1243).
//
// Neden var: maskeli ai_calls kopyası, kod geldiğinde `[kod: …]`
// özetini taşıyor; gelmediğinde HİÇBİR ŞEY taşımıyordu. Yani bir
// ai_calls satırına bakan operatör "kod hiç istenmedi" ile "istendi,
// ıskaladı" hâllerini AYIRT EDEMİYORDU — /ai analizi ve her postmortem
// ıska yarısını sessizce eksik sayıyordu (isabet oranı olduğundan iyi
// görünüyordu; v0.9.1241 sayaçlarının çözdüğü sorunun satır-düzeyi
// ikizi).
//
// Dolu bağlamda "" döner: işaret yalnız ıskanın işareti.
func (c CodeContext) LogMissSummary() string {
	if !c.Empty() {
		return ""
	}
	return FormatCodeMissNote(c.Outcome, c.Reason)
}

// FormatCodeMissNote — ıska işaretinin TEK yazımı. Saf; tablo-testli.
//
// Sınıf varsa sınıf yazılır: taksonomi sabit bir sözlük, kayıtlar
// üzerinde gruplanabilir. Sınıf yoksa (çıkmaz FetchCode taksonomisinin
// dışında — ör. bağlam taşmasında kod bloğunun düşürülmesi) gerekçe
// metni tavanlanarak yazılır; ikisi de yoksa sessiz kalmak yerine
// "bilinmiyor" denir — işaretin YOKLUĞU "hiç istenmedi" demektir ve
// tam olarak bu karışıklığı kapatmaya çalışıyoruz.
func FormatCodeMissNote(class CodeOutcome, reason string) string {
	label := strings.TrimSpace(string(class))
	if label == "" {
		label = capRunes(reason, codeNoteRuneCap)
	}
	if label == "" {
		label = "sebep bilinmiyor"
	}
	return "\n\n[kod alınamadı: " + label + "]"
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


// frameGiveUpReason — v0.9.1264: AppFrames boş dönünce HANGİ sebepten
// boş döndüğünü söyler. SAF, tablo-testli. Üç sınıf üç ayrı aksiyon
// gerektirir; tek cümle üçünü de "stack işe yaramaz"a indiriyordu.
func frameGiveUpReason(frames []stackparse.Frame) string {
	if len(frames) == 0 {
		return "stacktrace çözümlenemedi — Java stack kalıbı bulunamadı"
	}
	app := 0
	for _, f := range frames {
		if f.IsApp {
			app++
		}
	}
	if app == 0 {
		return "stack yalnız çerçeve/JDK frame'leri taşıyor — uygulama kodu görünmüyor"
	}
	return "uygulama frame'leri dosya/satır taşımıyor (debug bilgisi olmadan derlenmiş olabilir)"
}
