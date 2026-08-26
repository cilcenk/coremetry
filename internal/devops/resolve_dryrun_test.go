package devops

import (
	"context"
	"strings"
	"testing"
)

// resolve_dryrun_test.go — v0.9.1242 "çözümü dene" provası.
//
// Ölçülen şey iki başlık:
//
//  1. ADIM ZİNCİRİ. Her adım gerçek zinciri koşuyor mu, ilk kırmızıda
//     duruyor mu, ve KOŞMAMIŞ adım listeye düşmüyor mu (koşmamış bir
//     adımı kırmızı göstermek operatörü olmayan bir arızanın peşine
//     düşürür).
//  2. SAYAÇ KARIŞMAZLIĞI. v0.9.1241'in isabet-oranı sayaçları GERÇEK
//     "Kodu da incele" denemelerini ölçüyor. Dry-run FetchCode'a hiç
//     girmediği için sayaç kıpırdamamalı — mekanizma bir bayrak değil,
//     çağrılmayan bir fonksiyon. Bu test o yapıyı kilitler: biri
//     ResolveDryRun'ın içinden FetchCode'u (ya da RecordCodeOutcome'ı)
//     çağırırsa burası kırmızı yanar.

// stepByKey — adımı anahtarına göre bulur.
func stepByKey(res DryRunResult, key string) (DryRunStep, bool) {
	for _, st := range res.Steps {
		if st.Key == key {
			return st, true
		}
	}
	return DryRunStep{}, false
}

// wantSteps — adım anahtarlarının SIRASI birebir bu olmalı.
func wantSteps(t *testing.T, res DryRunResult, keys ...string) {
	t.Helper()
	var got []string
	for _, st := range res.Steps {
		got = append(got, st.Key)
	}
	if strings.Join(got, ",") != strings.Join(keys, ",") {
		t.Fatalf("adımlar=%v, istenen %v", got, keys)
	}
}

// lastStep — zincirin durduğu adım.
func lastStep(t *testing.T, res DryRunResult) DryRunStep {
	t.Helper()
	if len(res.Steps) == 0 {
		t.Fatal("hiç adım yok")
	}
	return res.Steps[len(res.Steps)-1]
}

func TestResolveDryRunSuccess(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{"/README.md", "/src/main/java/com/example/A.java"}
	svc := New()
	svc.Configure(f.settings())

	res := svc.ResolveDryRun(context.Background(), "bsa-core-service-prod", PinRead{})

	wantSteps(t, res, DryRunStepConnection, DryRunStepPin, DryRunStepRepo,
		DryRunStepProject, DryRunStepBranch, DryRunStepTree)
	if !res.OK {
		t.Fatalf("OK=false: %+v", res.Steps)
	}
	for _, st := range res.Steps {
		if !st.OK {
			t.Errorf("adım %q kırmızı: %s", st.Key, st.Detail)
		}
		if strings.TrimSpace(st.Detail) == "" {
			t.Errorf("adım %q detailsiz — ekranda boş satır", st.Key)
		}
	}
	// Konvansiyon: "bsa-" öneki + "-prod" ortam eki soyulur.
	if res.Repo != "core-service" {
		t.Errorf("Repo=%q, istenen core-service", res.Repo)
	}
	if res.Source != RepoSourceConvention {
		t.Errorf("Source=%q, istenen %q", res.Source, RepoSourceConvention)
	}
	// Ayarlar'daki Project açık: türetimi (BSA) EZMELİ.
	if res.Project != "Payments" {
		t.Errorf("Project=%q, istenen Payments (Ayarlar açık alanı)", res.Project)
	}
	if p, _ := stepByKey(res, DryRunStepProject); !strings.Contains(p.Detail, "Ayarlar") {
		t.Errorf("proje adımı kaynağı söylemiyor: %q", p.Detail)
	}
	// Branş sırası release > master (varsayılan).
	if res.Branch != "release" {
		t.Errorf("Branch=%q, istenen release", res.Branch)
	}
	if res.FileCount != 2 {
		t.Errorf("FileCount=%d, istenen 2", res.FileCount)
	}
	// Ağaç adımı yalnız SAYI söyler; yol listesi yanıtta yok.
	if tr, _ := stepByKey(res, DryRunStepTree); strings.Contains(tr.Detail, "/src") {
		t.Errorf("ağaç adımı dosya yolu sızdırdı: %q", tr.Detail)
	}
	// PAT hiçbir adımda görünmemeli.
	for _, st := range res.Steps {
		if strings.Contains(st.Detail, "test-pat") {
			t.Fatalf("PAT ekrana sızdı (%s): %q", st.Key, st.Detail)
		}
	}
}

// Konvansiyonun ürettiği küçük-harf ad sunucuda BAŞKA yazımda:
// kaçış kapısı (v0.9.1236) dry-run'da da açılmalı ve düzeltme izi
// ekrana çıkmalı — sessiz bir düzeltme, operatörün yanlış adı hiç
// öğrenmemesi demek olurdu.
func TestResolveDryRunRepoMissEscapeHatch(t *testing.T) {
	f := newFakeTFS(t)
	f.repos = []string{"Core-Service", "other-repo"}
	f.tree = []string{"/src/main/java/com/example/A.java"}
	svc := New()
	svc.Configure(f.settings())

	res := svc.ResolveDryRun(context.Background(), "bsa-core-service-prod", PinRead{})

	if !res.OK {
		t.Fatalf("kaçış kapısı açılmadı: %+v", res.Steps)
	}
	if res.Repo != "Core-Service" {
		t.Errorf("Repo=%q, istenen kanonik Core-Service", res.Repo)
	}
	br, ok := stepByKey(res, DryRunStepBranch)
	if !ok {
		t.Fatal("branş adımı yok")
	}
	if !strings.Contains(br.Detail, "düzeltildi") || !strings.Contains(br.Detail, "Core-Service") {
		t.Errorf("düzeltme izi branş adımında yok: %q", br.Detail)
	}
	if f.hits["list"] == 0 {
		t.Error("depo listesi hiç çağrılmadı — kaçış kapısı gerçekten koşmadı")
	}
}

// Sunucuda hiçbir yazımda yok: zincir branş adımında durur, SONRAKİ
// adım (ağaç) listeye HİÇ girmez.
func TestResolveDryRunRepoNotFoundStopsAtBranch(t *testing.T) {
	f := newFakeTFS(t)
	f.repos = []string{"totally-different"}
	svc := New()
	svc.Configure(f.settings())

	res := svc.ResolveDryRun(context.Background(), "bsa-core-service-prod", PinRead{})

	if res.OK {
		t.Fatal("OK=true, oysa depo yok")
	}
	wantSteps(t, res, DryRunStepConnection, DryRunStepPin, DryRunStepRepo,
		DryRunStepProject, DryRunStepBranch)
	last := lastStep(t, res)
	if last.Key != DryRunStepBranch || last.OK {
		t.Fatalf("son adım=%+v, istenen kırmızı branş", last)
	}
	// Sunucunun KENDİ reddi taşınıyor (404 + mesajı), jenerik bir
	// "olmadı" değil: operatörün bir sonraki hamlesi buna bakıyor.
	if !strings.Contains(last.Detail, "404") {
		t.Errorf("gerekçe sunucunun teşhisini taşımıyor: %q", last.Detail)
	}
}

// Proje çıkmazı: pin YALNIZ depo adı taşıyor, Ayarlar'da Project boş
// ve servis adı hiçbir öneke uymuyor → üç kaynağın da durumu tek
// cümlede (projectDeadEnd, v0.9.1240) ve ağ adımlarına HİÇ geçilmez.
func TestResolveDryRunProjectDeadEnd(t *testing.T) {
	f := newFakeTFS(t)
	cfg := f.settings()
	cfg.Project = ""
	svc := New()
	svc.Configure(cfg)

	res := svc.ResolveDryRun(context.Background(), "legacy-service",
		PinRead{Repo: "payments-core"})

	if res.OK {
		t.Fatal("OK=true, oysa proje çözülemedi")
	}
	wantSteps(t, res, DryRunStepConnection, DryRunStepPin, DryRunStepRepo, DryRunStepProject)
	last := lastStep(t, res)
	if last.OK {
		t.Fatalf("proje adımı yeşil: %+v", last)
	}
	for _, want := range []string{"Ayarlar", "pin", "önek"} {
		if !strings.Contains(last.Detail, want) {
			t.Errorf("çıkmaz cümlesi %q kaynağını anlatmıyor: %q", want, last.Detail)
		}
	}
	// Pin okundu → depo adı pinden geldi.
	if res.Repo != "payments-core" || res.Source != RepoSourcePin {
		t.Errorf("Repo=%q Source=%q, istenen payments-core/pin", res.Repo, res.Source)
	}
	// Ağ adımına geçilmediği için sunucuya refs isteği ÇIKMAMALI.
	if f.hits["refs"] != 0 {
		t.Errorf("proje çıkmazına rağmen %d refs isteği çıktı", f.hits["refs"])
	}
}

// Katalog okuması hata verdiyse zincir pin adımında DURUR (fail-CLOSED,
// v0.9.1236): konvansiyona düşüp başka bir uygulamanın deposunu
// "çözüm" diye göstermek, dry-run'ın en pahalı yalanı olurdu.
func TestResolveDryRunPinFailClosed(t *testing.T) {
	f := newFakeTFS(t)
	svc := New()
	svc.Configure(f.settings())

	const abort = "servis kataloğu okunamadı — yanlış depoya düşmemek için kod bağlamı atlandı"
	res := svc.ResolveDryRun(context.Background(), "bsa-core-service-prod",
		PinRead{Abort: abort})

	if res.OK {
		t.Fatal("OK=true, oysa katalog okunamadı")
	}
	wantSteps(t, res, DryRunStepConnection, DryRunStepPin)
	last := lastStep(t, res)
	if last.OK || last.Detail != abort {
		t.Fatalf("pin adımı=%+v, istenen kırmızı + iptal gerekçesi", last)
	}
	if res.Repo != "" {
		t.Errorf("Repo=%q — fail-CLOSED'da depo adı üretilmemeli", res.Repo)
	}
	if f.hits["refs"] != 0 {
		t.Errorf("fail-CLOSED'a rağmen %d refs isteği çıktı", f.hits["refs"])
	}
}

// Bağlantı yoksa İLK adım dürüst kırmızı döner — 500 değil, boş yanıt
// değil. Lokal/kurulmamış bir ortamda ekranın gösterdiği tek şey bu.
func TestResolveDryRunUnconfigured(t *testing.T) {
	svc := New()

	res := svc.ResolveDryRun(context.Background(), "bsa-core-service-prod", PinRead{})

	wantSteps(t, res, DryRunStepConnection)
	last := lastStep(t, res)
	if last.OK || !strings.Contains(last.Detail, "yapılandırılmamış") {
		t.Fatalf("bağlantı adımı=%+v, istenen kırmızı + 'yapılandırılmamış'", last)
	}
	if res.OK {
		t.Error("OK=true, oysa bağlantı yok")
	}
}

// SAYAÇ KARIŞMAZLIĞI (v0.9.1242). Dry-run, v0.9.1241'in kod-çekme
// sayaçlarına HİÇ dokunmamalı: mekanizma "FetchCode'a girmemek", ve
// bu test o yapıyı kilitler.
//
// Karşı kanıt aynı testte: GERÇEK bir FetchCode çağrısı sayacı
// artırıyor. Yoksa sayacın sıfır kalması "sayaç bozuk" ile "dry-run
// karışmıyor" arasında ayrım yapamazdı ([[fixture-counter]] dersi).
func TestResolveDryRunDoesNotTouchCodeCounters(t *testing.T) {
	f := newFakeTFS(t)
	f.tree = []string{"/src/main/java/com/example/A.java"}
	svc := New()
	svc.Configure(f.settings())
	ctx := context.Background()

	// Her sınıftan bir dry-run: başarı, kaçış kapısı, çıkmaz, iptal.
	svc.ResolveDryRun(ctx, "bsa-core-service-prod", PinRead{})
	svc.ResolveDryRun(ctx, "legacy-service", PinRead{})
	svc.ResolveDryRun(ctx, "bsa-core-service-prod", PinRead{Abort: "katalog okunamadı"})
	New().ResolveDryRun(ctx, "bsa-core-service-prod", PinRead{})

	if st := svc.CodeObservability(); st.Attempts != 0 || st.OK != 0 ||
		st.Partial != 0 || len(st.Misses) != 0 {
		t.Fatalf("dry-run sayaçlara karıştı: %+v", st)
	}

	// Sayaç GERÇEK yolda çalışıyor mu? (kapının kendisi ölçülüyor)
	svc.FetchCode(ctx, "core-service", ProjectHint{}, nil, nil)
	if st := svc.CodeObservability(); st.Attempts != 1 {
		t.Fatalf("FetchCode sayacı=%d, istenen 1 — sayaç zaten ölüyse üstteki sıfır bir şey kanıtlamaz", st.Attempts)
	}
}
