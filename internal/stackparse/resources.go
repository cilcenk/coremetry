package stackparse

import (
	"regexp"
	"sort"
	"strings"
)

// resources.go — HATA METNİNİN ANDIĞI KAYNAK DOSYALAR (v0.10.73).
//
// ── NEDEN ───────────────────────────────────────────────────────────────
//
// Kod bağlamı bugüne dek YALNIZ stack frame'lerinin `.java` dosyalarını
// çekiyordu. Ama bir sorgu hatasında asıl kanıt çoğu zaman kodda değil,
// KAYNAK dosyasındadır: mapper XML'i, SQL parçası, konfigürasyon.
//
// Operatörün ekranında model `IntTfraudMapper.xml` diye bir dosyadan söz
// etti — o dosya HİÇ GÖNDERİLMEMİŞTİ; model adı çıkarımla üretmişti.
// v0.10.72'den sonra bunu yapamıyor (uydurma yasak), yani dosya gerçekten
// gönderilmedikçe kanıt eksik kalıyor.
//
// ── SİNYAL NEREDE ───────────────────────────────────────────────────────
//
// İki yerde ve ikisi de metinde:
//
//	1. AÇIK DOSYA ADI — "IntTfraudMapper.xml", "queries.sql".
//	2. NİTELİKLİ STATEMENT ID — MyBatis/iBatis hataları
//	   `com.x.y.IntTfraudMapper.ariCTelefonSelect` biçiminde bir kimlik
//	   basıyor; sondan bir önceki parça MAPPER SINIFIDIR ve dosya adı
//	   ona eşittir.
//
// ⚠ Çıkarım FRAMEWORK'E DEĞİL, METNE dayanıyor: "X.Y" biçiminde nitelikli
// bir ad gören her yerde aday üretiliyor. MyBatis en yaygın üreteci ama
// kural onun adını ANMIYOR — başka bir kütüphane aynı biçimi basarsa
// kendiliğinden çalışır.
//
// ── NEDEN YALNIZ ADAY ───────────────────────────────────────────────────
//
// Bu paket ağa çıkmaz ve depoyu bilmez. Ürettiği şey ADAY LİSTESİDİR;
// hangisinin gerçekten var olduğuna depo ağacına bakan taraf karar verir.
// Aday üretmek ucuz, yanlış aday zararsız (ağaçta eşleşmez).

// resourceExts — kod bağlamına anlamlı katkı yapan kaynak uzantıları.
//
// Liste KISA tutuluyor: her uzantı, ağaçta yanlış eşleşme ve boşa çekim
// riskini büyütüyor. `.json` bilerek YOK — depolarda çok sayıda alakasız
// json bulunuyor ve hata metninde geçen bir json adı genelde veri, kod
// değil.
var resourceExts = []string{".xml", ".sql", ".yaml", ".yml", ".properties"}

// explicitFileRe — metinde açıkça geçen dosya adı.
var explicitFileRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_\-]*)\.(xml|sql|yaml|yml|properties)\b`)

// qualifiedIDRe — nitelikli kimlik: en az üç parça, parçalar nokta ile.
// `com.x.y.IntTfraudMapper.ariCTelefonSelect` → yakalanır.
var qualifiedIDRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*){2,})\b`)

// ResourceRef — aranacak bir kaynak dosya adayı.
type ResourceRef struct {
	// Base — uzantısız dosya adı ("IntTfraudMapper").
	Base string
	// Ext — biliniyorsa uzantı (".xml"); boşsa çağıran resourceExts'i dener.
	Ext string
}

// ResourceRefs — hata metninden kaynak dosya adaylarını çıkarır.
//
// Sıra DETERMİNİSTİK: açık dosya adları önce (kanıtı daha güçlü), sonra
// nitelikli kimliklerden türeyenler; her grup kendi içinde alfabetik.
// Deterministik sıra şart, çünkü çağıran ilk N adayı çekiyor — sıra
// rastgele olsaydı aynı hata iki kez farklı kanıt üretirdi.
func ResourceRefs(text string) []ResourceRef {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	explicit := map[string]string{} // base → ext
	for _, m := range explicitFileRe.FindAllStringSubmatch(text, -1) {
		explicit[m[1]] = "." + strings.ToLower(m[2])
	}

	derived := map[string]bool{}
	for _, m := range qualifiedIDRe.FindAllStringSubmatch(text, -1) {
		parts := strings.Split(m[1], ".")
		// Sondan bir önceki parça: `...Mapper.method` → `Mapper`.
		// Son parça metot adıdır ve dosya adı DEĞİLDİR.
		cand := parts[len(parts)-2]
		// Sınıf gibi görünmeyeni ele: dosya adları büyük harfle başlar.
		// Bu, `java.lang.String` gibi paket parçalarının aday olmasını da
		// engelliyor.
		if cand == "" || cand[0] < 'A' || cand[0] > 'Z' {
			continue
		}
		if _, seen := explicit[cand]; !seen {
			derived[cand] = true
		}
	}

	out := make([]ResourceRef, 0, len(explicit)+len(derived))
	for _, b := range sortedKeys(explicit) {
		out = append(out, ResourceRef{Base: b, Ext: explicit[b]})
	}
	for _, b := range sortedKeysBool(derived) {
		out = append(out, ResourceRef{Base: b})
	}
	return out
}

// ResourceExts — çağıranın deneyeceği uzantılar (Ext boş olan adaylar için).
func ResourceExts() []string { return append([]string(nil), resourceExts...) }

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ErrorCodeTokens — hata METNİNDEKİ nitelikli kimliklerin SON parçasından
// dil-bağımsız arama token'ları (v0.10.100, operatör-raporlu "Aslında kod
// var"): hata zinciri Java'dan .NET'e geçince fırlatan kod .cs dosyasında
// yaşıyor ve frame-türevi .java araması onu YAPISAL olarak bulamaz. Hata
// kodunun kendisi ("…CustomerCardsNoCreditCardSmFlag" gibi) ise dilden
// bağımsız: kodu fırlatan satır o token'ı içerir ve org araması onu her
// uzantıda bulur.
//
// Seçicilik bilinçli sıkı: SON parça, BüyükHarfle başlar, içinde küçük
// harf taşır (CamelCase — SABİT_YAZIM kimlikleri ve kısaltmalar elenir)
// ve ≥8 karakterdir; ilk görülme sırası korunur, tavan 3. Gevşek bir
// süzgeç org aramasını gürültüye boğar — yanlış kanıt, kanıt yokluğundan
// kötü (PickSearchHit başlığındaki ders).
func ErrorCodeTokens(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range qualifiedIDRe.FindAllStringSubmatch(text, -1) {
		parts := strings.Split(m[1], ".")
		last := parts[len(parts)-1]
		if len(last) < 8 || last[0] < 'A' || last[0] > 'Z' {
			continue
		}
		if strings.ToUpper(last) == last { // küçük harf yok → sabit/kısaltma
			continue
		}
		if !seen[last] {
			seen[last] = true
			out = append(out, last)
			if len(out) == 3 {
				break
			}
		}
	}
	return out
}
