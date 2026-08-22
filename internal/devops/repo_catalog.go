package devops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// repo_catalog.go — depo adı KAÇIŞ KAPISI (v0.9.1236).
//
// Sorun: depo adı konvansiyondan türetiliyor (repo_resolve.go) ve
// konvansiyon KÜÇÜK HARF üretiyor — servis adları küçük harf. Gerçek
// depo ise "CashManagement.CashFlow" ya da "cash_management-CashFlow"
// yazımında olabilir. Azure DevOps Server ada göre çözümü harf
// duyarsız yapmayı genelde tolere eder, ama AYRAÇ farkı (- _ .)
// koşulsuz 404'tür. Bugüne kadarki tek çare servis başına elle pin
// atmaktı ve operatör bunu ancak opak bir "depo bulunamadı"dan sonra
// anlıyordu (2026-08-19 ops notu: bilinen açık risk).
//
// Çözüm: 404'ten SONRA (mutlu yolda ASLA) projenin depo listesi BİR
// kez çekilir, 10 dk cache'lenir ve istenen ad iki basamakta aranır:
//
//	1. EqualFold          — yalnız harf yazımı farklı
//	2. ayraç-soyulmuş fold — - _ . ve boşluk atıldıktan sonra fold
//
// Eşleşirse sunucunun KANONİK adıyla yeniden denenir ve düzeltme izi
// Reason'a yazılır: sessiz bir düzeltme, operatörün katalogdaki yanlış
// adı hiç fark etmemesi demek olurdu.
//
// Eşleşme yoksa bugünkü hata + EN YAKIN 2-3 ad. Listenin tamamı
// DÖKÜLMEZ: 800 depolu bir projede tam liste, hata mesajını
// okunamaz yapıp asıl cevabı ("bu ad yok") gömerdi.

const (
	// repoListTTL — depo listesi cache'i. Ağaç cache'iyle aynı ölçek:
	// bir projeye depo eklenmesi dakikalar ölçeğinde olmaz, ve bu liste
	// yalnız hata yolunda okunuyor.
	repoListTTL = 10 * time.Minute
	// repoListMaxProjects — cache'teki proje sayısı tavanı. Tek kurulum
	// pratikte 1-2 proje görür; tavan bir kaçak sınırı.
	repoListMaxProjects = 4
	// repoListBodyCap — liste yanıtı okuma tavanı (4MB). Azure DevOps
	// depo başına ~600 bayt metadata döndürür; 4MB birkaç bin depoyu
	// karşılar, ötesi bir hata sayfasıdır.
	repoListBodyCap = 4 << 20
	// repoListMaxNames — bellekte tutulan ad sayısı tavanı.
	repoListMaxNames = 5000
	// repoNearMax — eşleşme yokken gösterilen en yakın ad sayısı.
	repoNearMax = 3
	// repoNearMinPrefix — "yakın" sayılmak için gereken en kısa ortak
	// önek (normalize edilmiş biçimde, rune). 3'ten kısası gürültü:
	// tek harf ortaklığı bütün listeyi aday yapardı.
	repoNearMinPrefix = 3
)

// RepoNameMatch — istenen ad ↔ sunucudaki kanonik adlar eşleşmesi.
//
// Name boşsa eşleşme yok ve Near doludur (varsa). Rung hangi basamağın
// tuttuğunu söyler — düzeltme izini yazan taraf bunu okumaz ama test
// okur: "fold" beklenen yerde "loose" tutuyorsa normalizasyon çok
// gevşemiş demektir.
type RepoNameMatch struct {
	Name string   // sunucudaki kanonik ad ("" = eşleşme yok)
	Rung string   // exact | fold | loose
	Alts []string // AYNI basamakta eşleşen diğer adaylar (deterministik)
	Near []string // eşleşme yoksa en yakın adlar (≤ repoNearMax)
}

// Eşleşme basamakları — testler ve hata mesajları bunları okur.
const (
	RepoRungExact = "exact"
	RepoRungFold  = "fold"  // yalnız harf yazımı farklı
	RepoRungLoose = "loose" // ayraçlar da soyulduktan sonra
)

// MatchRepoName — SAF eşleştirici; tablo-testli, ağ yok.
//
// Basamaklar SIRAYLA denenir ve ilk DOLU basamak kazanır: harf-yazımı
// eşleşmesi varken ayraç-soyulmuş bir adaya geçmek, daha zayıf kanıtı
// daha güçlüsüne tercih etmek olurdu.
//
// Aynı basamakta birden çok aday varsa seçim DETERMİNİSTİK: adlar
// sıralanır, ilki kazanır, kalanlar Alts'a düşer. Sunucunun liste
// sırasına güvenmek, aynı sorgunun iki farklı depoyu açması demekti —
// yanlış depodan kod göstermek bu özelliğin en pahalı hatası.
func MatchRepoName(want string, have []string) RepoNameMatch {
	w := strings.TrimSpace(want)
	if w == "" || len(have) == 0 {
		return RepoNameMatch{}
	}
	var exact, fold, loose []string
	wLower := strings.ToLower(w)
	wNorm := normalizeRepoName(w)
	seen := map[string]bool{}
	for _, h := range have {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		switch {
		case h == w:
			exact = append(exact, h)
		case strings.ToLower(h) == wLower:
			fold = append(fold, h)
		case wNorm != "" && normalizeRepoName(h) == wNorm:
			loose = append(loose, h)
		}
	}
	for _, rung := range []struct {
		name string
		hits []string
	}{
		{RepoRungExact, exact}, {RepoRungFold, fold}, {RepoRungLoose, loose},
	} {
		if len(rung.hits) == 0 {
			continue
		}
		sort.Strings(rung.hits)
		return RepoNameMatch{Name: rung.hits[0], Rung: rung.name, Alts: rung.hits[1:]}
	}
	return RepoNameMatch{Near: nearestRepoNames(w, have)}
}

// normalizeRepoName — ayraçları at, küçük harfe çevir.
// "CashManagement.CashFlow" ve "cash_management-cashflow" → aynı.
//
// Ayraçlar: - _ . ve boşluk. Bunlar Azure DevOps depo adlarında
// serbestçe karışır (aynı kuruluşta üç yazım birden görülür); harf
// yazımı gibi bunlar da bir KİMLİK farkı değil, bir yazım tercihidir.
func normalizeRepoName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case '-', '_', '.', ' ':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// nearestRepoNames — eşleşme yokken operatöre gösterilecek adaylar.
//
// "Trivially available" olan tek sinyal kullanılıyor: normalize edilmiş
// biçimde ORTAK ÖNEK uzunluğu, artı içerme (biri diğerinin parçası).
// Levenshtein'a gerek yok — buradaki amaç sıralama yarışması kazanmak
// değil, "bunu mu demek istediniz" için 2-3 ad göstermek.
func nearestRepoNames(want string, have []string) []string {
	wn := normalizeRepoName(want)
	if wn == "" {
		return nil
	}
	type cand struct {
		name  string
		score int
	}
	var cands []cand
	seen := map[string]bool{}
	for _, h := range have {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hn := normalizeRepoName(h)
		if hn == "" {
			continue
		}
		score := commonPrefixLen(wn, hn)
		if strings.Contains(hn, wn) || strings.Contains(wn, hn) {
			// İçerme, önek uzunluğundan güçlü bir sinyal: "cashflow"
			// isterken "bsa-cashflow-api" tam da aranan depodur.
			score += 100
		}
		if score < repoNearMinPrefix {
			continue
		}
		cands = append(cands, cand{h, score})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].name < cands[j].name
	})
	out := make([]string, 0, repoNearMax)
	for _, c := range cands {
		if len(out) >= repoNearMax {
			break
		}
		out = append(out, c.name)
	}
	return out
}

// commonPrefixLen — rune cinsinden ortak önek uzunluğu.
func commonPrefixLen(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n := 0
	for n < len(ar) && n < len(br) && ar[n] == br[n] {
		n++
	}
	return n
}

// recoverRepoName — sunucudaki depo listesine karşı kaçış kapısı.
//
// YALNIZ hata yolundan çağrılır (code.go, resolveBranch başarısız
// olduktan sonra). Dönenler: kanonik ad (varsa) ve eşleşme yokken
// gösterilecek en yakın adlar. Liste okunamazsa ikisi de boş —
// çağıran ASIL hatasını korur: kaçış kapısının kendi arızası,
// operatörün ekranında asıl teşhisin (404 / yetki) yerine geçmemeli.
//
// cause, resolveBranch'in hatası: KİMLİK sınıfı (401/403) hatalarda
// liste hiç denenmez. O hatanın sebebi ad değil PAT'tır ve listeleme
// aynı duvara çarpıp yalnızca bir istek daha yakardı.
func (s *Service) recoverRepoName(ctx context.Context, cli *http.Client, cfg Settings, want string, cause error) (string, []string) {
	if isAuthClassErr(cause) {
		return "", nil
	}
	names, err := s.repoNames(ctx, cli, cfg)
	if err != nil || len(names) == 0 {
		return "", nil
	}
	m := MatchRepoName(want, names)
	if m.Name == "" {
		return "", m.Near
	}
	if m.Name == want {
		// Ad zaten birebir doğru: hata başka bir sebepten (yetki,
		// ağ). Yeniden denemek aynı hatayı bir kez daha üretirdi.
		return "", nil
	}
	return m.Name, nil
}

// isAuthClassErr — hata KİMLİK/YETKİ sınıfı mı? Saf; tablo-testli.
//
// doGetCapped 401/403'ü tek şekilde yazıyor ("http 401 — check the
// PAT…"), o yüzden metinden okumak burada kırılgan değil. Amaç dar:
// kaçış kapısını PAT arızasında hiç çalıştırmamak.
func isAuthClassErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "http 401") || strings.Contains(msg, "http 403")
}

// withNote — düzeltme izini Reason'ın BAŞINA koyar (v0.9.1236).
//
// Sıra bilinçli: "depo adı sunucudan düzeltildi" bilgisi, arkasından
// gelen her mesajın BAĞLAMIdır — sona eklenirse operatör önce
// beklemediği bir depoya ait hatayı okur, düzeltmeyi sonra görür.
func withNote(note, reason string) string {
	switch {
	case note == "":
		return reason
	case reason == "":
		return note
	}
	return note + " · " + reason
}

// repoNames — {collection}/{project}/_apis/git/repositories, 10 dk cache.
//
// TEK istek, TEK bütçe: çağrı, her diğer DevOps isteğiyle aynı
// http.Client'ı (20 sn tavan) ve aynı ctx'i kullanır — kaçış kapısına
// ayrı bir zaman aşımı takmak, aynı bütçeyi iki yerden yönetmek olurdu.
// Gövde repoListBodyCap ile, ad sayısı repoListMaxNames ile sınırlı.
func (s *Service) repoNames(ctx context.Context, cli *http.Client, cfg Settings) ([]string, error) {
	key := cfg.BaseURL + "|" + cfg.Collection + "|" + cfg.Project
	if names, ok := s.code.getRepos(key); ok {
		return names, nil
	}
	var firstErr error
	for _, ver := range s.apiVersionCandidates(cfg) {
		body, err := doGetCapped(ctx, cli, reposURL(cfg)+"?api-version="+ver, cfg, repoListBodyCap)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var lr struct {
			Value []struct {
				Name string `json:"name"`
			} `json:"value"`
		}
		if err := json.Unmarshal(body, &lr); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("depo listesi çözümlenemedi")
			}
			continue
		}
		names := make([]string, 0, len(lr.Value))
		for _, r := range lr.Value {
			if strings.TrimSpace(r.Name) == "" {
				continue
			}
			names = append(names, r.Name)
			if len(names) >= repoListMaxNames {
				break
			}
		}
		// Boş liste de cache'lenir: "bu projede depo göremiyorum"
		// cevabı da bir cevaptır ve her frame denemesinde yeniden
		// sorulmamalı.
		s.code.putRepos(key, names)
		return names, nil
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("depo listesi okunamadı")
	}
	return nil, firstErr
}
