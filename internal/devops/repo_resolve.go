package devops

import (
	"strings"
)

// repo_resolve.go — servis adı → depo adı çözümü (v0.9.830). SAF,
// tablo-testli; ağ yok.
//
// İki yol var ve sıraları önemli:
//
//  1. service_metadata.repository DOLUYSA o kazanır. Elle pin,
//     konvansiyonu HER ZAMAN yener — konvansiyon bir tahmindir,
//     operatörün yazdığı ad bir karardır. (Mevcut merge/pin sözleşmesi:
//     deriver'ların yazdığı alanlar insan girdisini ezmez.)
//  2. Yoksa ad konvansiyonu: yapılandırılabilir bir ÖNEK (varsayılan
//     "bsa-") + chstore'un ORTAM EKİ (-prod/-int/-uat/-prep) soyulur.
//     bsa-digital-mobile-pushconfirm-prod → digital-mobile-pushconfirm
//
// Neden yapılandırılabilir: önek ve branş sırası kuruluma özgü. Kodda
// sabitlenirse ikinci müşteri için yeni sürüm gerekir; ayarda durursa
// operatör kendi konvansiyonunu yazar.

// DefaultRepoPrefixes / DefaultBranchOrder — ayar boşken kullanılan
// varsayılanlar. Ayarın kendisi devops_connection blob'unda.
func DefaultRepoPrefixes() []string { return []string{"bsa-"} }
func DefaultBranchOrder() []string  { return []string{"release", "master"} }

// envSuffixes — servis adlarındaki ortam ekleri.
//
// AYNA: chstore.EnvSuffixes ile AYNI küme olmak zorunda. Kopya, çünkü
// devops paketi bilinçli olarak chstore'a bağlanmıyor (paket doc'una
// bakın) — ayrışmayı repo_resolve_test.go chstore kaynağını OKUYARAK
// yakalıyor. Emsal birebir: chstore/job_service_test.go ↔
// logstore/env_suffix.go.
var envSuffixes = []string{"-prod", "-int", "-uat", "-prep"}

// ResolveConfig — çözücünün ayarlanabilir kısmı. Boş alanlar
// varsayılana düşer, yani sıfır değer çalışır bir yapılandırmadır.
type ResolveConfig struct {
	RepoPrefixes []string
	BranchOrder  []string
}

// withDefaults — boş alanları doldurur; girdi değiştirilmez.
func (c ResolveConfig) withDefaults() ResolveConfig {
	if len(c.RepoPrefixes) == 0 {
		c.RepoPrefixes = DefaultRepoPrefixes()
	}
	if len(c.BranchOrder) == 0 {
		c.BranchOrder = DefaultBranchOrder()
	}
	return c
}

// Repo çözüm kaynakları — yanıtın altındaki kaynak satırı ve testler
// bunları okur.
const (
	RepoSourcePin        = "pin"        // service_metadata.repository
	RepoSourceConvention = "convention" // önek + ortam eki soyma
	RepoSourceNone       = ""           // çözülemedi
)

// RepoResolution — çözüm sonucu + NEDEN. Reason boş bir sonuçta
// operatöre gösterilir ("neden kod yok" sorusunun cevabı).
type RepoResolution struct {
	Repo   string
	Source string
	Reason string
	// Project (v0.9.1183, operatör isteği: "service_name başında bsa-
	// yazıyorsa direkt project BSA olduğunu anlasın") — DevOps proje
	// ÖNERİSİ + önerinin kaynağı.
	//
	// Yalnız bir ÖNERİ: ayardaki açık Project her zaman kazanır (FetchCode
	// sırası). Önek zaten "bu servis şu projeye ait" bilgisini taşıyor
	// (kurulumun kendi adlandırma sözleşmesi), o yüzden aynı bilgiyi
	// ikinci bir alana daha yazdırmak gereksiz bir el işiydi — ve boş
	// bırakıldığında kod bağlamı sessizce hiç çalışmıyordu.
	Project ProjectHint
}

// ProjectHint — proje adı ÖNERİSİ, kaynağı ve (boşsa) neden boş kaldığı.
//
// v0.9.1240 — üçüncü alan Reason yeni: proje çözülemediğinde çıkmazın
// nedeni ÜÇ kaynağa dağılmış durumda (pinin proje bileşeni, Ayarlar'daki
// Project, önek türetimi) ve ikisini yalnız burası bilir. FetchCode
// yalnız Ayarlar'ı görüyor, o yüzden tek başına yazdığı cümle ("servis
// adı bilinen bir önekle başlamıyor") pin kısa devresi yüzünden düpedüz
// YANLIŞ olabiliyordu: önek tutuyor, türetim hiç koşmuyordu.
type ProjectHint struct {
	Value  string // önerilen proje ("" = öneri yok)
	Source string // RepoSourcePin | RepoSourceConvention | ""
	Reason string // Value=="" iken pin + önek kaynaklarının durumu
}

// projectFromPrefix — eşleşen servis önekinden proje adı.
// "bsa-" → "BSA". SAF.
//
// Kural: ayraçları (- _ . /) at, BÜYÜK harfe çevir. Büyük harf, Azure
// DevOps proje adlarının yaygın yazımı ve sunucu proje adını URL'de
// harf-duyarsız çözüyor; yani yanlış tahminin bedeli, doğru tahminin
// kazancından küçük. Yine de bir TAHMİN — ayardaki açık Project bunu
// ezer ve hata mesajı hangi projenin denendiğini SÖYLER, ki operatör
// tahmini görebilsin.
func projectFromPrefix(prefix string) string {
	p := strings.Trim(strings.TrimSpace(prefix), "-_./")
	if p == "" {
		return ""
	}
	return strings.ToUpper(p)
}

// ResolveRepo — servis adı + service_metadata.repository → depo adı.
//
// metaRepository tam bir URL de olabilir (operatörler katalog alanına
// çoğunlukla depo linkini yapıştırır); son yol parçası alınır ve
// ".git" eki atılır. "_git/" segmenti varsa ondan SONRASI alınır —
// Azure DevOps depo linklerinin kanonik şekli budur.
//
// Saf. Branş seçimi ayrıdır (PickBranch + refs API): branş varlığı
// sunucuya sorulmadan bilinemez.
func ResolveRepo(service, metaRepository string, cfg ResolveConfig) RepoResolution {
	cfg = cfg.withDefaults()
	svc := strings.TrimSpace(service)

	if pinProject, pinned := parsePinnedRepo(metaRepository); pinned != "" {
		out := RepoResolution{Repo: pinned, Source: RepoSourcePin}
		// v0.9.1240 — pinin KENDİ taşıdığı proje her şeyi ezer: operatör
		// tam URL ("…/BSA/_git/repo") ya da "BSA/repo" yazdıysa hangi
		// projede olduğunu AÇIKÇA söylemiştir. Eskiden bu bileşen
		// ayrıştırma sırasında atılıyordu ve çapraz-proje pini, servis
		// adından türetilen (yanlış) projede aranıyordu.
		if pinProject != "" {
			out.Project = ProjectHint{Value: pinProject, Source: RepoSourcePin}
			return out
		}
		// Pin YALNIZ depo adı taşıyor. Burada türetimi atlamak için bir
		// sebep yok: pin deponun adını söylüyor, projeyi söylemiyor —
		// söylenmemiş olanı konvansiyondan doldurmak, pinin iradesine
		// dokunmaz. v0.9.1183 türetimi pinli yolda hiç koşmadığı için
		// Ayarlar'da Project boşken pinli servis "proje yok" çıkmazına
		// düşüyordu; pin, kod bağlamını AÇMAK yerine KAPATIYORDU.
		_, project := matchRepoPrefix(svc, cfg.RepoPrefixes)
		out.Project = projectHintFor(project, svc, true, cfg.RepoPrefixes)
		return out
	}

	if svc == "" {
		return RepoResolution{Source: RepoSourceNone, Reason: "servis adı yok"}
	}

	// v0.9.1183 — EŞLEŞEN önek proje adını da söyler. Hangi önekin
	// tuttuğu yalnız burada biliniyor; çağırana taşımazsak bilgi
	// kaybolur ve operatör aynı şeyi bir kez daha, elle yazmak zorunda
	// kalır.
	name, project := matchRepoPrefix(svc, cfg.RepoPrefixes)
	name = stripEnvSuffix(name)

	if name == "" {
		return RepoResolution{Source: RepoSourceNone,
			Reason: "servis adı konvansiyona uymuyor: " + svc}
	}
	return RepoResolution{Repo: name, Source: RepoSourceConvention,
		Project: projectHintFor(project, svc, false, cfg.RepoPrefixes)}
}

// matchRepoPrefix — ilk EŞLEŞEN öneki soyar ve o önekten proje adını
// türetir. Eşleşme yoksa ad AYNEN döner, proje boş kalır. SAF.
//
// Tek yazım: hem konvansiyon yolu (depo + proje) hem pinli yol (yalnız
// proje) buradan okur. İki yerde ayrı ayrı yazılsaydı biri diğerinden
// sessizce ayrışırdı — pinli yolun türetimi ZATEN bu şekilde, hiç
// yazılmadığı için kayıptı.
func matchRepoPrefix(name string, prefixes []string) (rest, project string) {
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		// Adın TAMAMI önekse soymayız — boş depo adı üretmek yerine
		// "eşleşmedi" demek dürüst.
		if p != "" && len(name) > len(p) && strings.HasPrefix(name, p) {
			return name[len(p):], projectFromPrefix(p)
		}
	}
	return name, ""
}

// projectHintFor — öneriyi paketler; öneri boşsa ÇIKMAZIN nedenini
// yazar. SAF; tablo-testli.
//
// Cümle yalnız BU katmanın bildiği iki kaynağı anlatır (pin bileşeni +
// önek türetimi); Ayarlar'daki Project'i FetchCode ekler. Bölme
// bilinçli: her kaynağın durumunu onu OKUYAN katman söylesin, yoksa
// biri değiştiğinde öteki sessizce yalan söylemeye başlar.
func projectHintFor(project, service string, pinned bool, prefixes []string) ProjectHint {
	if project != "" {
		return ProjectHint{Value: project, Source: RepoSourceConvention}
	}
	pinPart := "katalogda depo pini yok"
	if pinned {
		pinPart = "katalog pini yalnız depo adı taşıyor (proje bileşeni yok)"
	}
	shown := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		if p = strings.TrimSpace(p); p != "" {
			shown = append(shown, p)
		}
	}
	prefixPart := "servis adı (" + service + ") bilinen öneklerden hiçbiriyle başlamıyor"
	if len(shown) > 0 {
		prefixPart = "servis adı (" + service + ") bilinen öneklerden (" +
			strings.Join(shown, ", ") + ") hiçbiriyle başlamıyor"
	}
	return ProjectHint{Reason: pinPart + ", " + prefixPart}
}

// projectDeadEnd — "proje adı çözülemedi" cümlesi (v0.9.1240). SAF.
//
// ÜÇ kaynağın durumu tek satırda: Ayarlar'daki Project (bu katman
// bilir), katalog pininin proje bileşeni ve önek türetimi (hint.Reason
// ile ResolveRepo'dan gelir). Eski cümle tek bir kaynağı suçluyordu
// ("servis adı bilinen bir önekle başlamıyor") ve pinli yolda bu
// çoğunlukla YANLIŞTI: önek tutuyordu, türetim hiç koşmuyordu. Operatör
// düzeltilecek şeyi arayıp bulamıyordu.
//
// Eylem üç kapıyı da açık bırakır; hangisinin ucuz olduğunu kurulumun
// sahibi bilir.
func projectDeadEnd(hint ProjectHint) string {
	msg := "proje adı çözülemedi: Ayarlar → Kod entegrasyonu'nda Project boş"
	if r := strings.TrimSpace(hint.Reason); r != "" {
		msg += ", " + r
	}
	return msg + " — Project'i doldurun, katalogdaki depo pinini proje bileşeniyle yazın " +
		"(BSA/depo ya da tam URL) ya da servis önekini Ayarlar → Kod entegrasyonu'na ekleyin"
}

// stripEnvSuffix — chstore.StripEnvSuffix'in aynadaki ikizi.
func stripEnvSuffix(name string) string {
	for _, suf := range envSuffixes {
		if len(name) > len(suf) && strings.HasSuffix(name, suf) {
			return name[:len(name)-len(suf)]
		}
	}
	return name
}

// parsePinnedRepo — katalogdaki `repository` alanını ayrıştırır:
// (proje, depo). SAF; tablo-testli. Boş/bozuk girdi → ("", "") ve
// çağıran konvansiyona düşer.
//
// Tanınan ÜÇ biçim (operatörler bu alana üçünü de yazıyor):
//
//	düz depo adı     "payments-core"                  → ("", "payments-core")
//	proje/depo       "BSA/payments-core"              → ("BSA", "payments-core")
//	tam URL          ".../BSA/_git/repo?version=GBx"  → ("BSA", "repo")
//	scp-benzeri SSH  "git@host:BSA/repo.git"          → ("BSA", "repo")
//
// v0.9.1240 — proje bileşeni ARTIK atılmıyor. Eskiden yalnız depo adı
// çıkarılıyordu; başka bir projedeki bir depoyu pinleyen operatörün
// yazdığı proje sessizce düşüyor ve depo YANLIŞ projede aranıyordu.
//
// Proje çıkarımı bilinçli olarak MUHAFAZAKÂR: host varken _git'ten
// önceki yolun en az İKİ parçası olmalı (koleksiyon/org + proje), çünkü
// koleksiyon kapsamlı bir link (".../DefaultCollection/_git/repo")
// koleksiyonu proje sanıp her isteği 404'e çevirirdi. Kaçırılan bir
// proje türetimle telafi edilir; YANLIŞ bir proje edilmez.
func parsePinnedRepo(meta string) (project, repo string) {
	m := strings.TrimSpace(meta)
	if m == "" {
		return "", ""
	}
	// Sorgu parçası ve fragment atılır: .../_git/foo?version=GBmaster
	if i := strings.IndexAny(m, "?#"); i >= 0 {
		m = m[:i]
	}
	m = strings.TrimRight(m, "/")

	// Şema + otorite ya da scp-benzeri "user@host:" öneki soyulur.
	// hadHost, YOLUN host'tan sonra başladığını söyler: o durumda ilk
	// parça en iyi ihtimalle koleksiyon/org'dur, proje değil.
	hadHost := false
	if i := strings.Index(m, "://"); i >= 0 {
		rest := m[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			m = rest[j+1:]
		} else {
			m = ""
		}
		hadHost = true
	} else if i := strings.Index(m, "@"); i >= 0 {
		// "git@host:BSA/repo.git" — ":" sonrası zaten PROJE kökünden
		// başlar, o yüzden hadHost FALSE kalır (host tüketildi).
		if j := strings.Index(m[i+1:], ":"); j >= 0 {
			m = m[i+1+j+1:]
		}
	}

	segs := splitNonEmpty(m, "/")
	if len(segs) == 0 {
		return "", ""
	}
	var before []string
	gitAt := -1
	for i, s := range segs {
		if s == "_git" {
			gitAt = i
		}
	}
	switch {
	case gitAt >= 0 && gitAt+1 < len(segs):
		// _git'ten SONRAKİ İLK parça depodur; web arayüzü linkleri
		// arkasına başka segment ekleyebiliyor (.../_git/repo/commit/…).
		repo, before = segs[gitAt+1], segs[:gitAt]
	case gitAt >= 0:
		return "", "" // ".../_git" — depo adı yok
	default:
		repo, before = segs[len(segs)-1], segs[:len(segs)-1]
	}
	repo = strings.TrimSpace(strings.TrimSuffix(repo, ".git"))
	if repo == "" {
		return "", ""
	}

	minBefore := 1
	if hadHost {
		minBefore = 2
	}
	switch {
	case gitAt >= 0 && len(before) >= minBefore:
		project = before[len(before)-1]
	case gitAt < 0 && !hadHost && len(before) == 1:
		// "BSA/payments-core" — _git yok ama iki parça var ve host
		// yok: tek makul okuma proje/depo. Üç ve daha fazla parçada
		// hangi parçanın proje olduğu belirsizdir; tahmin etmeyiz.
		project = before[0]
	}
	return strings.TrimSpace(project), repo
}

// PickBranch — sunucudan gelen branş adları arasından ayardaki SIRAYA
// göre ilk VAR OLANI seçer. Saf; tablo-testli.
//
// available "refs/heads/master" ya da düz "master" olabilir — refs API
// tam ref adı döner, karşılaştırma son parça üzerinden yapılır.
// Hiçbiri yoksa "" döner ve çağıran deponun VARSAYILAN branşına düşer:
// "release yok, master yok" bir hata değil, farklı bir konvansiyondur.
//
// v0.9.1236 — eşleşme HARF DUYARSIZ. Eskiden bayt-bayt eşleşiyordu ve
// "refs/heads/Release" taşıyan bir depo, ayardaki "release" ile hiç
// tutmuyordu: PickBranch "" dönüyor, çağıran sessizce deponun
// VARSAYILAN branşına (çoğunlukla Master/Develop) düşüyordu. Sonuç en
// kötü sınıftan bir hataydı — kod pencereleri YANLIŞ BRANŞTAN, yani
// yanlış satırlardan kesiliyor ve operatöre kanıt diye gösteriliyordu;
// hiçbir yerde bir uyarı yoktu.
//
// Basamak sırası: aynı `want` için ÖNCE birebir, sonra harf duyarsız.
// Sunucunun KANONİK yazımı döner — git ref URL'i harf duyarlıdır,
// operatörün ayardaki yazımını geri vermek 404 üretirdi.
// Ayardaki SIRA semantiği korunur: dış döngü order, iç döngü basamak.
func PickBranch(available []string, order []string) string {
	if len(available) == 0 {
		return ""
	}
	if len(order) == 0 {
		order = DefaultBranchOrder()
	}
	exact := make(map[string]string, len(available))
	fold := make(map[string]string, len(available))
	for _, a := range available {
		short := ShortBranch(a)
		if short == "" {
			continue
		}
		// İlk gelen kazanır: aynı kısa ada iki tam ref düşmez ama
		// deterministik olsun.
		if _, ok := exact[short]; !ok {
			exact[short] = short
		}
		if lo := strings.ToLower(short); lo != "" {
			if _, ok := fold[lo]; !ok {
				fold[lo] = short
			}
		}
	}
	for _, want := range order {
		want = ShortBranch(strings.TrimSpace(want))
		if want == "" {
			continue
		}
		if b, ok := exact[want]; ok {
			return b
		}
		if b, ok := fold[strings.ToLower(want)]; ok {
			return b
		}
	}
	return ""
}

// ShortBranch — "refs/heads/master" → "master". Zaten kısaysa aynen.
func ShortBranch(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return strings.Trim(ref, "/")
}
