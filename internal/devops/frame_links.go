package devops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// frame_links.go — stack frame → DevOps derin linki (v0.10.581,
// Aşama 1.5).
//
// SORUN: exception detayında stack trace düz metin. Operatör "şu
// satıra bakayım" dediğinde depoyu elle bulup, branşı seçip, paketi
// dizinde arayıp satıra iniyor. Zincirin her adımı zaten kodda var —
// yalnız bir arayüz affordance'ına bağlı değildi.
//
// DÖRT PAZARLIKSIZ DURUŞ:
//
//  1. FetchCode'a HİÇ GİRİLMEZ. Kod isabet sayaçları (RecordCodeOutcome)
//     YALNIZ FetchCode'un defer'ında; bu yol resolveChain'i doğrudan
//     çağırır, yani /ai isabet oranı bir link tıklamasıyla kıpırdamaz.
//     Mekanizma yapısal, bayrak değil — resolve_dryrun.go'nun aynı
//     kararı (v0.9.1242). frame_links_test.go bunu kilitliyor.
//  2. KOD GÖVDESİ DÖNMEZ. Yalnız künye + URL. copilot_code.go'nun
//     codePayload'ı da bilinçle Content kopyalamıyor ("kaynak modele
//     gider, tarayıcıya değil"); aynı sözleşme burada da geçerli, ve
//     bu dosya zaten hiçbir dosya İÇERİĞİ çekmiyor — yalnız ağaç.
//  3. AĞAÇ BİR KEZ. Tüm aday frame'ler TEK depo ağacıyla eşleşir;
//     frame başına ağaç sorgusu, 40 satırlık bir stack'te 40 ağ
//     çağrısı demekti.
//  4. REVİZYON ÇÖZÜMLEME YOK, VE BU SÖYLENİR. Link branşın UCUNA
//     gider, olay anındaki sürüme değil. Uyarı istisna değil KURAL:
//     link üretildiği her yanıtta dolu. Sessiz kalmak, kaymış bir
//     satır numarasını kanıt diye göstermek olurdu (PickBranch'in
//     v0.9.1236 dersi: yanlış branştan kesilen pencere, en pahalı
//     hata sınıfı).

const (
	// FrameLinkCandidateLimit — kaç UYGULAMA frame'i için yol
	// çözülür. Ağaç zaten tek sefer okunuyor, yani bu tavan ağ
	// çağrısını değil CPU'yu (yol listesi × frame) sınırlar: 60.000
	// yollu bir depoda 40 frame × 60.000 yol = 2.4M karşılaştırma,
	// üstelik operatörün ilk 10'dan sonrasına baktığı bir stack yok.
	// Sıra RankFrames'in: operatörün AppPrefixes'i tutan frame'ler
	// önce, sonra derin "Caused by" segmentleri.
	FrameLinkCandidateLimit = 10

	// frameLinkDeadlineCap — istek başına TOPLAM süre tavanı.
	// FetchCode'un 25 sn'lik tavanından ayrı ve daha kısa: bu uç bir
	// LLM turunun parçası değil, operatörün açtığı bir çekmecenin
	// arkasında duruyor ve 10 sn'den uzun süren bir "link üret"
	// isteği zaten cevapsız sayılır.
	frameLinkDeadlineCap = 10 * time.Second
)

// Frame gerekçeleri — url boş kaldığında NEDEN boş kaldığı. Sabit,
// çünkü hem frontend hem test bunları okur; iki yerde iki yazım
// sessizce ayrışırdı.
const (
	FrameReasonLibrary = "kütüphane frame'i"
	FrameReasonNoLine  = "satır numarası yok"
	FrameReasonNoPath  = "depo ağacında eşleşen yol yok"
	// Sayı ELDE yazılı (const ifadesi strconv çağıramaz).
	// frame_links_test.go metnin FrameLinkCandidateLimit ile
	// tutmasını pinliyor: tavan değişip cümle kalırsa test düşer.
	FrameReasonOverLimit = "çözüm tavanı doldu — yalnız ilk 10 uygulama frame'i denendi"
)

// frameReasonRuneCap — ağ/çözüm hatalarının ekrana giden tavanı. Ham
// hata metni bir Java yığın izinin altında bir satır olarak
// gösterilecek; paragraf olmamalı.
const frameReasonRuneCap = 160

// FrameLink — TEK frame'in link durumu. Kod gövdesi YOK, dosya yolu
// bile yok: yol yalnız URL'in içinde taşınır, çünkü ayrı bir alan
// olarak dönmesi frontend'i "yol var ama link yok" gibi imkânsız bir
// ara duruma açardı.
type FrameLink struct {
	// App — frame UYGULAMA kodu mu (efektif). stackparse.Frame.IsApp
	// ile aynı değil: operatörün açık AppPrefixes'i frameworkPrefixes
	// sabit listesini EZER (RankFrames sözleşmesi, v0.10.112), yani
	// "org.apache.myco" ayarda yazılıysa burada true döner.
	App bool
	// Tier — 0 = AppPrefixes'ten biriyle başlıyor, 1 = diğer. App
	// false iken anlamsız (1 kalır).
	Tier int
	// URL — DevOps web arayüzünde dosya + satır. Boş = çözülemedi,
	// Reason nedenini söyler.
	URL string
	// Reason — URL boşken NEDEN boş. URL doluyken boş.
	Reason string
}

// FrameLinks — bir stack'in tamamının çözümü.
//
// Links, çağırana verilen frames dilimiyle BİREBİR hizalı ve AYNI
// uzunlukta: çağıran ham satır indeksini kendi tarafında tutuyor ve
// eşleştirmeyi indeksle yapıyor. Harita döndürmek, "eksik anahtar"ı
// sessiz bir boşluğa çevirirdi.
type FrameLinks struct {
	// Configured — DevOps bağlantısı yapılandırılmış mı. false ise
	// geri kalan her alan boştur ve bu bir HATA DEĞİLDİR: eşleme
	// tanımlı değilse frame'ler düz metin kalır.
	Configured bool
	Repo       string
	Project    string
	Branch     string
	// RepoSource — pin | convention (RepoSourcePin/RepoSourceConvention).
	RepoSource string
	Links      []FrameLink
	// Revision (v0.10.590) — sürüm→ref çözümü. nil = sürüm verilmedi ya da
	// yer tutucuydu. Verified=false ise Note nedenini söyler ve linkler
	// branş ucundadır (uyarı kalır).
	Revision *Revision
}

// Revision — olay anındaki sürümün VCS karşılığı.
type Revision struct {
	Version  string
	Ref      string // "tags/release.1"
	SHA      string // commit; Verified=true iken dolu
	Verified bool
	Note     string
}

// frameLinkDeadline — yürürlükteki tavan. Operatörün daha KISA bir
// kod tavanı ayarladığı kurulumda ona uyar: bu uç, kod yolundan daha
// sabırlı olmayı hak etmiyor. Tek kaynak — ekranda yazan süre ile
// gerçekten uygulanan tavan aynı olsun (deadlineReason dersi).
func (s *Service) frameLinkDeadline() time.Duration {
	if d := s.fetchDeadline(); d > 0 && d < frameLinkDeadlineCap {
		return d
	}
	return frameLinkDeadlineCap
}

// frameLinkKey — aynı dosyanın aynı satırına düşen frame'leri TEK
// çözüme indirger. Özyinelemeli bir stack'te aynı satır onlarca kez
// geçer; her biri için ağaç taramak aynı cevabı onlarca kez üretirdi.
// Method anahtarda YOK: URL yalnız dosya + satırdan üretiliyor.
func frameLinkKey(f stackparse.Frame) string {
	return f.Class + "\x00" + f.File + "\x00" + strconv.Itoa(f.Line)
}

// classifyFrames — AĞ ÖNCESİ sınıflandırma. Her frame'e bir gerekçe
// yazar; ağ yürürse adayların gerekçesi linkle EZİLİR. SAF.
//
// Sıra önemli: önce "kütüphane mi", sonra "satırı var mı". Tersi,
// satırsız bir JDK frame'ine "satır numarası yok" derdi — doğru ama
// yanıltıcı, çünkü satırı olsa da link üretmezdik.
func classifyFrames(frames []stackparse.Frame, appPrefixes []string) []FrameLink {
	out := make([]FrameLink, len(frames))
	for i, f := range frames {
		tier := 1
		app := f.IsApp
		if stackparse.HasAppPrefix(f.Class, appPrefixes) {
			tier, app = 0, true
		}
		out[i] = FrameLink{App: app, Tier: tier}
		switch {
		case !app:
			out[i].Reason = FrameReasonLibrary
		case f.File == "" || f.Line <= 0:
			out[i].Reason = FrameReasonNoLine
		default:
			// Aday olabilir. RankFrames tavanı aşarsa bu gerekçe
			// olduğu gibi kalır — dürüst cevap "denenmedi", "yok" değil.
			out[i].Reason = FrameReasonOverLimit
		}
	}
	return out
}

// failEligible — çözüm zinciri çıkmaza düştüğünde, LİNK ALABİLECEK
// her frame'e aynı gerekçeyi yazar. Kütüphane / satırsız frame'lerin
// gerekçesi korunur: onların çözülememe sebebi zincir değil.
func failEligible(links []FrameLink, reason string) {
	reason = capRunes(reason, frameReasonRuneCap)
	if reason == "" {
		reason = "kod bağlantısı çözülemedi"
	}
	for i := range links {
		if links[i].Reason == FrameReasonOverLimit {
			links[i].Reason = reason
		}
	}
}

// ResolveFrameLinks — stack frame'leri → DevOps derin linkleri.
//
// Yazma YOK, LLM YOK, sayaç YOK, kod gövdesi YOK. Dönen Links dilimi
// frames ile aynı uzunlukta ve aynı sırada.
//
// Zincir FetchCode'unkiyle AYNI (resolveChain): kopya bir zincir ilk
// düzeltmede ayrışır ve operatöre gerçekte olmayan bir davranışı
// gösterirdi — resolve_dryrun.go'nun 1 numaralı kararı.
func (s *Service) ResolveFrameLinks(ctx context.Context, service string, pin PinRead, frames []stackparse.Frame, version string) FrameLinks {
	cfg := s.CurrentSettings()
	out := FrameLinks{Links: classifyFrames(frames, cfg.AppPrefixes)}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		// Yapılandırılmamış: HATA DEĞİL. Çağıran bunu 200 + boş liste
		// olarak yazar; operatör kuralı "eşleme tanımlı değilse
		// frame'ler düz metin kalsın, hata gösterme".
		return out
	}
	out.Configured = true

	// Aday YOKSA ağa hiç çıkma. Tamamı JDK olan bir stack'te depo
	// ağacını çekmek, cevabı değiştirmeyen saniyeler demekti.
	cands := stackparse.RankFrames(frames, FrameLinkCandidateLimit, cfg.AppPrefixes)
	if len(cands) == 0 {
		return out
	}

	// Katalog pini okunamadıysa fail-CLOSED: yanlış depoya link vermek,
	// link vermemekten kötü (pinReadDecision sözleşmesi, v0.9.1236).
	if pin.Abort != "" {
		failEligible(out.Links, pin.Abort)
		return out
	}
	res := ResolveRepo(service, pin.Repo, s.ResolveConfig())
	if res.Repo == "" {
		failEligible(out.Links, res.Reason)
		return out
	}
	out.Repo, out.RepoSource = res.Repo, res.Source

	project, _, dead := pickProject(cfg, res.Project)
	if dead != "" {
		failEligible(out.Links, dead)
		return out
	}
	out.Project = project
	cfg.Project = project

	// parent AYRI tutulur: "tarayıcı gitti" ile "DevOps yanıt vermedi"
	// karışmasın (deadlineHit sözleşmesi).
	dl := s.frameLinkDeadline()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, dl)
	defer cancel()
	ch := s.resolveChain(ctx, parent, s.clientFor(cfg.InsecureSkipVerify), cfg, res.Repo)
	if ch.repo != "" {
		out.Repo = ch.repo
	}
	out.Branch = ch.branch
	// v0.10.590 — sürüm → ref → commit. Başarılıysa AĞAÇ DA o commit'ten
	// okunur: link ile yol aynı ref'e bakmalı. Başarısızlık sessiz DEĞİL
	// (Revision.Note) ama link üretimini durdurmaz — branş ucu + uyarı.
	paths := ch.paths
	linkRef := RefSpec{Kind: "branch", Name: ch.branch}
	if ref, ok := ResolveVersionRef(cfg.VersionRef, version); ok && ch.class == "" && out.Repo != "" {
		out.Revision = &Revision{Version: strings.TrimSpace(version), Ref: ref}
		cli := s.clientFor(cfg.InsecureSkipVerify)
		sha, err := s.refCommit(ctx, cli, cfg, ch.ver, out.Repo, ref)
		switch {
		case err != nil:
			out.Revision.Note = "ref sorgusu başarısız: " + err.Error()
		case sha == "":
			out.Revision.Note = ref + " bulunamadı"
		default:
			tree, terr := s.repoTreeAt(ctx, cli, cfg, ch.ver, out.Repo, RefSpec{Kind: "commit", Name: sha})
			if terr != nil || len(tree.paths) == 0 {
				out.Revision.Note = "commit ağacı okunamadı: " + sha
			} else {
				paths, linkRef = tree.paths, RefSpec{Kind: "commit", Name: sha}
				out.Revision.SHA, out.Revision.Verified = sha, true
			}
		}
	}
	if ch.class != "" {
		reason := ch.reason
		if ch.class == CodeDeadline {
			// resolveChain süreyi s.fetchDeadline()'dan yazıyor; bizim
			// tavanımız daha kısa. Ekranda yazan sayı uygulanan tavan
			// olmalı, yoksa operatör olmayan bir yavaşlığın peşine düşer.
			reason = fmt.Sprintf("DevOps %s içinde yanıt vermedi", dl)
		}
		failEligible(out.Links, reason)
		return out
	}

	// Ağaç BİR KEZ okundu; tüm adaylar aynı yol listesiyle eşleşir.
	resolved := make(map[string]FrameLink, len(cands))
	for _, f := range cands {
		k := frameLinkKey(f)
		if _, done := resolved[k]; done {
			continue
		}
		link := FrameLink{Reason: FrameReasonNoPath}
		if p := BestPathForFrame(paths, f); p != "" {
			if u := FileURLAt(cfg, out.Project, out.Repo, linkRef, p, f.Line); u != "" {
				link = FrameLink{URL: u}
			}
		}
		resolved[k] = link
	}
	for i, f := range frames {
		// Yalnız ADAY OLABİLEN frame'in gerekçesi ezilir; kütüphane ya
		// da satırsız frame aynı anahtara düşemez (RankFrames ikisini
		// de eler) ama muhafızı yazılı tutuyoruz: sınıflandırma kuralı
		// değişirse burası sessizce yanlış link vermesin.
		if out.Links[i].Reason != FrameReasonOverLimit {
			continue
		}
		if l, ok := resolved[frameLinkKey(f)]; ok {
			out.Links[i].URL, out.Links[i].Reason = l.URL, l.Reason
		}
	}
	return out
}
