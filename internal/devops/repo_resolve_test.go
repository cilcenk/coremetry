package devops

import (
	"os"
	"strings"
	"testing"
)

// repo_resolve_test.go — v0.9.830. Servis adları operatörün
// konvansiyonunun ŞEKLİNİ taşır; müşteri sunucusu/koleksiyonu geçmez.

func TestResolveRepo(t *testing.T) {
	tests := []struct {
		name    string
		service string
		meta    string
		cfg     ResolveConfig
		repo    string
		source  string
	}{
		{
			// Spec'in pinli örneği: önek + ortam eki birlikte soyulur.
			name:    "önek + ortam eki",
			service: "bsa-digital-mobile-pushconfirm-prod",
			repo:    "digital-mobile-pushconfirm", source: RepoSourceConvention,
		},
		{
			name:    "önek var, ortam eki yok",
			service: "bsa-digital-mobile-pushconfirm",
			repo:    "digital-mobile-pushconfirm", source: RepoSourceConvention,
		},
		{
			name:    "önek yok, ortam eki var",
			service: "payment-gateway-uat",
			repo:    "payment-gateway", source: RepoSourceConvention,
		},
		{name: "hiç ek yok", service: "payment-gateway", repo: "payment-gateway", source: RepoSourceConvention},
		{name: "-int eki", service: "bsa-core-int", repo: "core", source: RepoSourceConvention},
		{name: "-prep eki", service: "bsa-core-prep", repo: "core", source: RepoSourceConvention},
		{
			// Tek ek soyulur: -prod-prod gerçek bir konvansiyon değil,
			// döngüsel soyma sürprizi olurdu.
			name:    "yalnız SON ortam eki soyulur",
			service: "bsa-core-uat-prod",
			repo:    "core-uat", source: RepoSourceConvention,
		},
		{
			name:    "ad tamamen önekten ibaret → soyma yok",
			service: "bsa-",
			repo:    "bsa-", source: RepoSourceConvention,
		},
		{
			name:    "ad tamamen ortam ekinden ibaret → soyma yok",
			service: "-prod",
			repo:    "-prod", source: RepoSourceConvention,
		},
		{
			name:    "özel önek listesi",
			service: "acme_billing-uat",
			cfg:     ResolveConfig{RepoPrefixes: []string{"acme_"}},
			repo:    "billing", source: RepoSourceConvention,
		},
		{
			name:    "birden çok önek — ilk EŞLEŞEN uygulanır",
			service: "svc-orders-prod",
			cfg:     ResolveConfig{RepoPrefixes: []string{"bsa-", "svc-"}},
			repo:    "orders", source: RepoSourceConvention,
		},
		// --- pin kazanır ---
		{
			name:    "pin düz ad",
			service: "bsa-digital-mobile-pushconfirm-prod", meta: "pushconfirm-legacy",
			repo: "pushconfirm-legacy", source: RepoSourcePin,
		},
		{
			name:    "pin _git URL'si",
			service: "bsa-core-prod",
			meta:    "https://devops.example.local/tfs/DefaultCollection/Payments/_git/core-service",
			repo:    "core-service", source: RepoSourcePin,
		},
		{
			name:    "pin _git URL'si + sorgu parçası",
			service: "bsa-core-prod",
			meta:    "https://devops.example.local/tfs/Coll/Proj/_git/core-service?version=GBrelease",
			repo:    "core-service", source: RepoSourcePin,
		},
		{
			name:    "pin ssh/.git uzantılı",
			service: "bsa-core-prod", meta: "git@devops.example.local:Proj/core-service.git",
			repo: "core-service", source: RepoSourcePin,
		},
		{
			name:    "pin sondaki eğik çizgi",
			service: "bsa-core-prod", meta: "https://devops.example.local/Coll/Proj/_git/core-service/",
			repo: "core-service", source: RepoSourcePin,
		},
		{
			// Pin, konvansiyonu EZER: soyma uygulanmaz. Operatör
			// deposunu "bsa-" ile adlandırdıysa öyle kalır.
			name:    "pin konvansiyona uğramaz",
			service: "bsa-core-prod", meta: "bsa-core",
			repo: "bsa-core", source: RepoSourcePin,
		},
		{
			name:    "yalnız boşluktan ibaret pin → konvansiyon",
			service: "bsa-core-prod", meta: "   ",
			repo: "core", source: RepoSourceConvention,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveRepo(tt.service, tt.meta, tt.cfg)
			if got.Repo != tt.repo || got.Source != tt.source {
				t.Fatalf("Repo/Source=%q/%q, istenen %q/%q (reason=%q)",
					got.Repo, got.Source, tt.repo, tt.source, got.Reason)
			}
		})
	}
}

func TestResolveRepoEmptyService(t *testing.T) {
	got := ResolveRepo("  ", "", ResolveConfig{})
	if got.Repo != "" || got.Source != RepoSourceNone {
		t.Fatalf("boş servis çözülmemeliydi: %+v", got)
	}
	if got.Reason == "" {
		t.Error("Reason boş — fail-open yolunda operatöre NEDEN söylenmeli")
	}
}

func TestPickBranch(t *testing.T) {
	tests := []struct {
		name      string
		available []string
		order     []string
		want      string
	}{
		{
			name:      "release ve master varsa release",
			available: []string{"refs/heads/master", "refs/heads/release", "refs/heads/feature/x"},
			want:      "release",
		},
		{
			name:      "release yoksa master",
			available: []string{"refs/heads/develop", "refs/heads/master"},
			want:      "master",
		},
		{
			name:      "hiçbiri yoksa boş (çağıran varsayılan branşa düşer)",
			available: []string{"refs/heads/develop", "refs/heads/main"},
			want:      "",
		},
		{
			name:      "kısa adlar da kabul",
			available: []string{"develop", "master"},
			want:      "master",
		},
		{
			name:      "özel sıra",
			available: []string{"refs/heads/main", "refs/heads/master"},
			order:     []string{"main", "master"},
			want:      "main",
		},
		{
			name:      "sıradaki ad refs/heads/ önekli yazılmış",
			available: []string{"refs/heads/main"},
			order:     []string{"refs/heads/main"},
			want:      "main",
		},
		{name: "hiç branş yok", available: nil, want: ""},
		{
			name:      "boş sıra → varsayılan sıra",
			available: []string{"refs/heads/master", "refs/heads/release"},
			order:     []string{},
			want:      "release",
		},
		// --- v0.9.1236: HARF DUYARSIZ eşleşme ---------------------
		//
		// Eski davranış sessizce "" dönüyor, çağıran deponun VARSAYILAN
		// branşına düşüyor ve kod pencereleri YANLIŞ BRANŞTAN, yani
		// yanlış satırlardan kesiliyordu. Uyarı yoktu.
		{
			name:      "Release büyük harfli — yine de seçilir",
			available: []string{"refs/heads/Master", "refs/heads/Release"},
			want:      "Release",
		},
		{
			name:      "sunucunun KANONİK yazımı döner (git ref harf duyarlı)",
			available: []string{"refs/heads/RELEASE"},
			want:      "RELEASE",
		},
		{
			name:      "Master var, Release yok → sıra korunur",
			available: []string{"refs/heads/Develop", "refs/heads/Master"},
			want:      "Master",
		},
		{
			name:      "ayardaki yazım büyük harfli, sunucudaki küçük",
			available: []string{"refs/heads/master", "refs/heads/release"},
			order:     []string{"Release", "Master"},
			want:      "release",
		},
		{
			// BİREBİR eşleşme, harf duyarsıza YEĞLENİR.
			name:      "ikisi de varsa birebir kazanır",
			available: []string{"Release", "release"},
			order:     []string{"release"},
			want:      "release",
		},
		{
			// SIRA semantiği bozulmamalı: harf duyarsız eşleşme
			// sıradaki İKİNCİ adayı öne geçirmez.
			name:      "sıra > basamak: Release (fold) master'dan (exact) önce gelir",
			available: []string{"refs/heads/master", "refs/heads/Release"},
			order:     []string{"release", "master"},
			want:      "Release",
		},
		{
			name:      "çoklu yazım varsa ilk gelen kazanır (deterministik)",
			available: []string{"refs/heads/RELEASE", "refs/heads/Release"},
			order:     []string{"release"},
			want:      "RELEASE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PickBranch(tt.available, tt.order); got != tt.want {
				t.Fatalf("PickBranch=%q, istenen %q", got, tt.want)
			}
		})
	}
}

// TestEnvSuffixesMirrorChstore — devops paketi chstore'a bilinçli
// bağlanmıyor, dolayısıyla ortam eki listesi bir KOPYA. Ayrışırlarsa
// aynı servis bir yüzeyde repoya çözülür, diğerinde çözülmez — sessiz
// ve teşhisi zor. Emsal: chstore/job_service_test.go ↔ logstore.
func TestEnvSuffixesMirrorChstore(t *testing.T) {
	b, err := os.ReadFile("../chstore/job_service.go")
	if err != nil {
		t.Fatalf("chstore kaynağı okunamadı: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "EnvSuffixes = []string{")
	if i < 0 {
		t.Fatal("chstore.EnvSuffixes bulunamadı — ayna kaynağı taşınmış olabilir")
	}
	j := strings.Index(src[i:], "}")
	if j < 0 {
		t.Fatal("chstore.EnvSuffixes bildirimi kapanmıyor")
	}
	block := src[i : i+j]
	for _, suf := range envSuffixes {
		if !strings.Contains(block, `"`+suf+`"`) {
			t.Errorf("%q devops'ta var, chstore.EnvSuffixes'te YOK", suf)
		}
	}
	// Ters yön: chstore'a eklenen bir ek buraya da gelmeli.
	for _, tok := range strings.Split(block, `"`) {
		if !strings.HasPrefix(tok, "-") {
			continue
		}
		found := false
		for _, suf := range envSuffixes {
			if suf == tok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q chstore.EnvSuffixes'te var, devops'ta YOK", tok)
		}
	}
}

// ── v0.9.1183 — servis önekinden PROJE türetme ─────────────────────────
//
// Operatör isteği: "service_name başında bsa- yazıyorsa direkt project BSA
// olduğunu anlasın."
//
// Bağlam: kod bağlamı, Project ayarı boş olduğu için hiç çalışmıyordu
// ("DevOps ayarında Project boş"). Oysa kurulumun kendi adlandırma
// sözleşmesi cevabı ZATEN taşıyor — önek eşleşmesi hangi projede
// olduğumuzu söylüyor. Aynı bilgiyi operatörden ikinci kez istemek
// gereksiz bir el işiydi.
//
// Türetme bir TAHMİN: ayardaki açık Project her zaman kazanır (FetchCode
// sırası) ve buradaki testler yalnız ÖNERİYİ ölçer.
func TestResolveRepoProjectFromPrefix(t *testing.T) {
	cases := []struct {
		name        string
		service     string
		prefixes    []string
		wantRepo    string
		wantProject string
	}{
		{
			"varsayılan önek → BSA",
			"bsa-cashmanagement-cashflow-prod", nil,
			"cashmanagement-cashflow", "BSA",
		},
		{
			"ortam eki proje türetmesini etkilemez",
			"bsa-payments-core-int", nil,
			"payments-core", "BSA",
		},
		{
			"ortam eki yoksa da çalışır",
			"bsa-payments-core", nil,
			"payments-core", "BSA",
		},
		{
			"özel önek kendi projesini söyler",
			"acme_billing-api-prod", []string{"acme_"},
			"billing-api", "ACME",
		},
		{
			// Önek eşleşmezse proje ÖNERİLMEZ. Uydurma bir proje adı
			// göndermek, sunucuda sessiz bir 404'e dönüşürdü.
			//
			// Depo adında ortam eki YİNE de soyulur: ek soyma önekten
			// BAĞIMSIZ bir kural (ayar ekranının kendi ifadesi: "ortam eki
			// her hâlükârda soyulur"). Türeyen tek şey proje.
			"önek eşleşmiyor → proje önerisi YOK, ek yine soyulur",
			"standalone-service-prod", nil,
			"standalone-service", "",
		},
		{
			"birden çok önek: EŞLEŞEN kazanır",
			"ops-gateway-prod", []string{"bsa-", "ops-"},
			"gateway", "OPS",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveRepo(c.service, "", ResolveConfig{RepoPrefixes: c.prefixes})
			if got.Repo != c.wantRepo {
				t.Errorf("Repo=%q, istenen %q", got.Repo, c.wantRepo)
			}
			if got.Project.Value != c.wantProject {
				t.Errorf("Project=%q, istenen %q", got.Project.Value, c.wantProject)
			}
		})
	}
}

// TestResolveRepoPinProjectWinsOverPrefix — pinin KENDİ projesi türetimi
// ezer (v0.9.1240).
//
// Bu test v0.9.1183'te TestResolveRepoPinCarriesNoProject olarak yazıldı
// ve doğru bir KORKUYU kodluyordu: çapraz-proje pinini servis önekinden
// türetilen projede aratmak (burada OTHER yerine BSA) sunucuda sessiz bir
// 404 üretir. Korunan davranış aynen duruyor — Project ASLA "BSA"
// olmamalı — ama v0.9.1183'ün çözümü fazla genişti: türetimi kapatmakla
// kalmayıp pinin AÇIKÇA yazdığı projeyi de atıyordu, yani doğru cevap
// elde dururken çıkmaza düşülüyordu. Artık pinin bileşeni kazanıyor.
func TestResolveRepoPinProjectWinsOverPrefix(t *testing.T) {
	got := ResolveRepo("bsa-payments-core-prod", "https://tfs.example.com/DefaultCollection/OTHER/_git/payments-core", ResolveConfig{})
	if got.Source != RepoSourcePin {
		t.Fatalf("Source=%q, pin bekleniyordu", got.Source)
	}
	if got.Project.Value == "BSA" {
		t.Fatalf("pinin projesi (OTHER) yerine servis önekinden türetilen BSA " +
			"kullanılmış — çapraz-proje pini yanlış projede aranır")
	}
	if got.Project.Value != "OTHER" || got.Project.Source != RepoSourcePin {
		t.Errorf("Project=%+v, istenen {OTHER pin} — pinin taşıdığı proje atılmamalı",
			got.Project)
	}
}

// TestResolveRepoPinWithoutProjectDerives — pin YALNIZ depo adı
// taşıyorsa v0.9.1183 önek türetimi pinli yolda da koşar (v0.9.1240).
//
// Kaybın şekli: operatör konvansiyonun tutmadığı bir depoyu pinliyor,
// Ayarlar'da Project boş; pin kısa devresi türetimi hiç çalıştırmadığı
// için kod bağlamı "proje yok" çıkmazına düşüyordu. Pin deponun adını
// söylüyor, projeyi söylemiyor — söylenmemiş olanı doldurmak pinin
// iradesine dokunmaz.
func TestResolveRepoPinWithoutProjectDerives(t *testing.T) {
	got := ResolveRepo("bsa-payments-core-prod", "pushconfirm-legacy", ResolveConfig{})
	if got.Repo != "pushconfirm-legacy" || got.Source != RepoSourcePin {
		t.Fatalf("Repo/Source=%q/%q — pin depo adını vermeli", got.Repo, got.Source)
	}
	if got.Project.Value != "BSA" || got.Project.Source != RepoSourceConvention {
		t.Fatalf("Project=%+v, istenen {BSA convention} — pin projeyi söylemiyorsa "+
			"önek türetimi koşmalı", got.Project)
	}
}

// TestResolveRepoProjectMissReason — proje çözülemediğinde neden ÜÇ
// kaynağı da anlatmalı (v0.9.1240). Reason'ın pin yarısı ve önek yarısı
// burada, Ayarlar yarısı FetchCode'da (TestProjectDeadEndNamesAllThree).
func TestResolveRepoProjectMissReason(t *testing.T) {
	cases := []struct {
		name    string
		service string
		meta    string
		want    []string
	}{
		{
			"pinli, önek tutmuyor",
			"standalone-service-prod", "pushconfirm-legacy",
			[]string{"katalog pini yalnız depo adı taşıyor", "standalone-service-prod", "bsa-"},
		},
		{
			"pinsiz, önek tutmuyor",
			"standalone-service-prod", "",
			[]string{"katalogda depo pini yok", "standalone-service-prod", "bsa-"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveRepo(c.service, c.meta, ResolveConfig{})
			if got.Project.Value != "" {
				t.Fatalf("Project=%q — bu vakada öneri olmamalı", got.Project.Value)
			}
			for _, w := range c.want {
				if !strings.Contains(got.Project.Reason, w) {
					t.Errorf("Reason=%q, %q içermeliydi", got.Project.Reason, w)
				}
			}
		})
	}
}

// TestParsePinnedRepo — pin alanının ÜÇ biçimi + bozuk girdiler.
//
// v0.9.1240. Proje bileşeni MUHAFAZAKÂR çıkarılır: kaçırılan proje önek
// türetimiyle telafi edilir, yanlış proje edilmez (her isteği 404'e
// çeviren sessiz bir hata olurdu).
func TestParsePinnedRepo(t *testing.T) {
	cases := []struct {
		name              string
		in                string
		wantProj, wantRep string
	}{
		// (1) düz depo adı
		{"düz ad", "pushconfirm-legacy", "", "pushconfirm-legacy"},
		{"düz ad — boşluklu", "  bsa-core  ", "", "bsa-core"},
		// (2) proje/depo
		{"proje/depo", "BSA/payments-core", "BSA", "payments-core"},
		{"proje/depo — .git eki", "BSA/payments-core.git", "BSA", "payments-core"},
		{"üç parça, _git yok → proje TAHMİN EDİLMEZ", "Coll/BSA/payments-core", "", "payments-core"},
		// (3) tam URL
		{
			"URL — koleksiyon + proje",
			"https://devops.example.local/tfs/DefaultCollection/Payments/_git/core-service",
			"Payments", "core-service",
		},
		{
			"URL — sorgu parçası atılır",
			"https://devops.example.local/tfs/Coll/Proj/_git/core-service?version=GBrelease",
			"Proj", "core-service",
		},
		{
			"URL — fragment atılır",
			"https://devops.example.local/Coll/Proj/_git/core-service#readme",
			"Proj", "core-service",
		},
		{
			"URL — sondaki eğik çizgi",
			"https://devops.example.local/Coll/Proj/_git/core-service/",
			"Proj", "core-service",
		},
		{
			"URL — _git sonrası fazladan segment (web arayüzü linki)",
			"https://devops.example.local/Coll/Proj/_git/core-service/commit/abc123",
			"Proj", "core-service",
		},
		{
			"URL — dev.azure.com (org + proje)",
			"https://dev.azure.com/contoso/Payments/_git/core-service",
			"Payments", "core-service",
		},
		{
			// Koleksiyon kapsamlı link: _git'ten önce TEK parça var ve o
			// koleksiyondur. Proje sanmak her isteği 404'e çevirirdi.
			"URL — koleksiyon kapsamlı, proje YOK",
			"https://devops.example.local/DefaultCollection/_git/core-service",
			"", "core-service",
		},
		{
			"scp-benzeri SSH", "git@devops.example.local:Proj/core-service.git",
			"Proj", "core-service",
		},
		// bozuk / boş girdiler
		{"boş", "", "", ""},
		{"yalnız boşluk", "   ", "", ""},
		{"yalnız eğik çizgi", "///", "", ""},
		{"yalnız sorgu", "?version=GBmaster", "", ""},
		{"yalnız .git", ".git", "", ""},
		{"_git sonrası boş", "https://devops.example.local/Coll/Proj/_git/", "", ""},
		{"host var, yol yok", "https://devops.example.local", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotProj, gotRep := parsePinnedRepo(c.in)
			if gotRep != c.wantRep {
				t.Errorf("repo=%q, istenen %q", gotRep, c.wantRep)
			}
			if gotProj != c.wantProj {
				t.Errorf("proje=%q, istenen %q", gotProj, c.wantProj)
			}
		})
	}
}

func TestProjectFromPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"bsa-", "BSA"},
		{"bsa", "BSA"},
		{"acme_", "ACME"},
		{"team.x.", "TEAM.X"},
		{"  ops-  ", "OPS"},
		{"-", ""},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := projectFromPrefix(c.in); got != c.want {
			t.Errorf("projectFromPrefix(%q) = %q, istenen %q", c.in, got, c.want)
		}
	}
}
