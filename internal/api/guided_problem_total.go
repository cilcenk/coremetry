package api

import (
	"context"
	"fmt"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// guided_problem_total.go — açık problem sayısının DÜRÜST toplamı
// (v0.10.21, Copilot denetimi bulgu #2).
//
// ── KUSUR: UYDURMADAN DAHA KÖTÜSÜ ───────────────────────────────────────
//
// `renderProblemsEvidenceTR` başlığı "toplam %d" derken `len(probs)`
// basıyordu. `probs` ise bir SQL LIMIT'inin çıktısı: guided yolların
// üçünde 10, ikisinde 50.
//
// 47 açık problemi olan bir serviste modele **"toplam 10"** gidiyordu.
// Prompt kurallarına MÜKEMMEL uyan bir model "10 açık problem var" diyor.
// Hiçbir anti-uydurma kuralı bunu yakalayamaz — çünkü model UYDURMUYOR,
// kendisine verilen yanlış sayıyı sadakatle aktarıyor. Kusurun kaynağı
// model değil, SUNUCU.
//
// Üstelik kırpma-ifşa dalı (`i >= guidedMaxLines`, guidedMaxLines = 10)
// limit=10 rotalarında YAPISAL OLARAK ULAŞILAMAZDI: döngü 0..9 arası
// dönüyor, indeks hiç 10'a çıkmıyor. Yani model yanlış toplamı, üstüne
// hiçbir uyarı almadan alıyordu.
//
// ── NEDEN "SADECE EN AZ N DE" YETMEZ ────────────────────────────────────
//
// Kırpmayı ilan etmek yalanı durdurur ama cevabı iyileştirmez: operatör
// "kaç açık problem var" diye soruyor ve "en az 10" faydasız bir cevap.
// Sayım ucuz (problems küçük bir ReplacingMergeTree) ve YALNIZ kırpma
// gerçekleştiğinde koşuyor — liste tavana dayanmadıysa `len(probs)`
// zaten gerçek toplamdır ve hiçbir ek sorgu atılmaz.
//
// ── ⚠ KAPSAM AYRIŞMASI: SAYIM HER SÜZGEÇLE UYUMLU DEĞİL ─────────────────
//
// `CountProblems` şu yüklemleri işliyor: Status, Service, Severity,
// RuleIDPrefix, RuleID, Env. `ListProblems` ise BUNLARA EK olarak
// Services (dilim), NotStatuses ve IDs biliyor.
//
// guidedProblemFilter yalnız Status+Service+Env kuruyor → kapsam BİREBİR.
// Ama bir guided yol `Services: svcs` (dilim) ile listeliyor; orada
// CountProblems TÜM servisleri sayar ve "düzeltme" yeni bir yalan üretir.
//
// Bu yüzden sayım yalnız sayılabilir süzgeçlerde deneniyor; aksi hâlde
// toplam BİLİNMİYOR kalıyor ve metin "en az N" diyor. Yanlış bir sayı
// basmaktansa belirsizliği söylemek doğru.

// problemsTotal — kaç açık problem VAR (kırpılmamış).
type problemsTotal struct {
	n uint64
	// known=false → sayım yapılamadı ya da kapsamı uyuşmuyor.
	// Metin o zaman kesin bir toplam iddia ETMEZ.
	known bool
}

// countableFilter — CountProblems bu süzgecin kapsamını birebir
// yeniden kurabiliyor mu.
//
// Services/NotStatuses/IDs, ListProblems'ın bildiği ama CountProblems'ın
// BİLMEDİĞİ yüklemler; biri doluysa sayım daha GENİŞ bir kümeyi sayar.
// ⚠ v0.10.68b/10.69 — SubjectKind EKLENDİ. v0.9.1342 ProblemFilter'a
// `SubjectKind` koydu ve ListProblems onu SQL'de (LIMIT'ten önce)
// uyguluyor; CountProblems onu HİÇ bilmiyor. Yani özne-türü şeridiyle
// daraltılmış bir liste, TÜM türler üzerinden sayılıyordu.
//
// Bu, listenin bilip sayımın bilmediği DÖRDÜNCÜ yüklem — ve üçü yazılıp
// dördüncüsü eklenmemişti ([[feedback-fixes-have-second-halves]]).
// Kapı artık iki listeyi KARŞILAŞTIRIYOR (guided_problem_total_test.go),
// yani beşincisi eklendiğinde burası kırmızıya döner.
func countableFilter(f chstore.ProblemFilter) bool {
	return f.Services == nil && len(f.NotStatuses) == 0 && f.IDs == nil &&
		f.SubjectKind == ""
}

// listDefaultLimit — ListProblems'in Limit==0 iken UYGULADIĞI tavan.
//
// ⚠ Ayna sabit. chstore.ListProblems `if f.Limit == 0 { f.Limit = 100 }`
// yapıyor; çağıran "limit yok" sanıyor ama sorgu 100'de kesiliyor.
// Aşağıdaki kırpma kontrolü bunu bilmezse, 250 açık problemli bir
// kurulumda cevap "toplam 100" der — ve bu, tam olarak bu dosyanın var
// olma sebebi olan v0.10.21 yalanıdır.
const listDefaultLimit = 100

// guidedProblemsWithTotal — listeyi ve dürüst toplamı birlikte döndürür.
//
// Ek sorgu YALNIZ kırpma olduğunda atılıyor: liste tavana dayanmadıysa
// uzunluğu zaten gerçek toplamdır.
func (s *Server) guidedProblemsWithTotal(
	ctx context.Context, f chstore.ProblemFilter,
) ([]chstore.Problem, problemsTotal, error) {
	probs, err := s.store.ListProblems(ctx, f)
	if err != nil {
		return nil, problemsTotal{}, err
	}
	// Tavana dayanmadı → uzunluk gerçek toplam. Sorgu yok.
	//
	// ⚠ Limit<=0 "tavan yok" DEMEK DEĞİL: ListProblems o durumda 100
	// uyguluyor. Eski hâl bu dalda uzunluğu KESİN toplam ilan ediyordu ve
	// 100 satır dönen bir kurulumda "toplam 100" diyordu.
	//
	// Bugünkü guided çağrılarının hepsi açık limit veriyor (10/50), yani
	// dal ŞU AN ulaşılamaz — ama biri "limitsiz" diye 0 geçtiği an yalan
	// geri gelirdi ve hiçbir test bunu söylemezdi.
	effLimit := f.Limit
	if effLimit <= 0 {
		effLimit = listDefaultLimit
	}
	if len(probs) < effLimit {
		return probs, problemsTotal{n: uint64(len(probs)), known: true}, nil
	}
	if !countableFilter(f) {
		// Kapsam yeniden kurulamıyor; "en az N" denecek.
		return probs, problemsTotal{}, nil
	}
	n, cerr := s.store.CountProblems(ctx, f)
	if cerr != nil || n < uint64(len(probs)) {
		// Sayım düştü ya da listeden KÜÇÜK geldi (yarış: arada bir
		// problem kapanmış olabilir). İkinci hâlde toplamı listeden az
		// göstermek görünür bir çelişki olurdu.
		return probs, problemsTotal{}, nil
	}
	return probs, problemsTotal{n: n, known: true}, nil
}

// problemsCountPhraseTR — başlıktaki sayı ifadesi.
//
// Saf: kusur bir CÜMLE kusuruydu, düzeltmesi de cümle düzeyinde
// test edilebilir olmalı.
func problemsCountPhraseTR(total problemsTotal, shown int) string {
	if total.known {
		return fmt.Sprintf("toplam %d", total.n)
	}
	// Bilinmiyor: kesin bir sayı iddia ETME.
	return fmt.Sprintf("en az %d", shown)
}

// problemsBreakdownCaveatTR — şiddet dağılımının kapsamı.
//
// Dağılım GÖSTERİLEN satırlardan sayılıyor. Liste kırpılmamışsa bu tam
// sayımdır ve açıklama gürültüdür — boş dize döner. Kırpıldıysa
// "toplam 47 (kritik 1, warning 1, info 0)" kendi içinde çelişir
// (1+1+0 ≠ 47) ve model bu çelişkiyi ya görmezden gelir ya da yanlış
// çözer; ikisi de kötü.
func problemsBreakdownCaveatTR(total problemsTotal, shown int) string {
	if total.known && total.n == uint64(shown) {
		return ""
	}
	return fmt.Sprintf(" — dağılım yalnız gösterilen %d satırdan", shown)
}

// problemsTruncationNoteTR — kırpma ifşası.
//
// Eski dal `i >= guidedMaxLines` idi ve limit=10 rotalarında hiç
// ateşlenmiyordu. Artık ifşa GÖSTERİLEN ile TOPLAM arasındaki farktan
// çıkıyor; satır tavanı ile sorgu tavanı ayrı ayrı ısırabiliyor.
//
// Boş dize → ilan edilecek bir şey yok.
func problemsTruncationNoteTR(total problemsTotal, shown, fetched int) string {
	switch {
	case total.known && uint64(shown) < total.n:
		return fmt.Sprintf("(%d satır gösteriliyor, toplam %d — kalanı için sorguyu daraltın)\n",
			shown, total.n)
	case !total.known && shown <= fetched:
		// Toplam bilinmiyor VE liste tavana dayanmış: sayım yapma,
		// ama sustuğunu da söyle.
		return fmt.Sprintf("(%d satır gösteriliyor; toplam sayı bu kapsamda ölçülemedi — "+
			"aşağıdaki liste EKSİK olabilir)\n", shown)
	}
	return ""
}
