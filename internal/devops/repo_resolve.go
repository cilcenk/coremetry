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

	if pinned := repoNameFromMeta(metaRepository); pinned != "" {
		return RepoResolution{Repo: pinned, Source: RepoSourcePin}
	}

	svc := strings.TrimSpace(service)
	if svc == "" {
		return RepoResolution{Source: RepoSourceNone, Reason: "servis adı yok"}
	}

	name := svc
	for _, p := range cfg.RepoPrefixes {
		p = strings.TrimSpace(p)
		// Adın TAMAMI önekse soymayız — boş depo adı üretmek yerine
		// "eşleşmedi" demek dürüst.
		if p != "" && len(name) > len(p) && strings.HasPrefix(name, p) {
			name = name[len(p):]
			break
		}
	}
	name = stripEnvSuffix(name)

	if name == "" {
		return RepoResolution{Source: RepoSourceNone,
			Reason: "servis adı konvansiyona uymuyor: " + svc}
	}
	return RepoResolution{Repo: name, Source: RepoSourceConvention}
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

// repoNameFromMeta — katalogdaki `repository` alanından depo adı.
// Boş/yalnız-boşluk → "" (konvansiyona düşülür).
func repoNameFromMeta(meta string) string {
	m := strings.TrimSpace(meta)
	if m == "" {
		return ""
	}
	// Sorgu parçası ve fragment atılır: .../_git/foo?version=GBmaster
	if i := strings.IndexAny(m, "?#"); i >= 0 {
		m = m[:i]
	}
	m = strings.TrimRight(m, "/")
	// Azure DevOps depo linkinin kanonik şekli: .../_git/<repo>
	if i := strings.LastIndex(m, "/_git/"); i >= 0 {
		m = m[i+len("/_git/"):]
	} else if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = strings.TrimSuffix(m, ".git")
	return strings.TrimSpace(m)
}

// PickBranch — sunucudan gelen branş adları arasından ayardaki SIRAYA
// göre ilk VAR OLANI seçer. Saf; tablo-testli.
//
// available "refs/heads/master" ya da düz "master" olabilir — refs API
// tam ref adı döner, karşılaştırma son parça üzerinden yapılır.
// Hiçbiri yoksa "" döner ve çağıran deponun VARSAYILAN branşına düşer:
// "release yok, master yok" bir hata değil, farklı bir konvansiyondur.
func PickBranch(available []string, order []string) string {
	if len(available) == 0 {
		return ""
	}
	if len(order) == 0 {
		order = DefaultBranchOrder()
	}
	have := make(map[string]string, len(available))
	for _, a := range available {
		short := ShortBranch(a)
		if short == "" {
			continue
		}
		// İlk gelen kazanır: aynı kısa ada iki tam ref düşmez ama
		// deterministik olsun.
		if _, ok := have[short]; !ok {
			have[short] = short
		}
	}
	for _, want := range order {
		want = ShortBranch(strings.TrimSpace(want))
		if want == "" {
			continue
		}
		if b, ok := have[want]; ok {
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
