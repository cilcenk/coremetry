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
			if got.Project != c.wantProject {
				t.Errorf("Project=%q, istenen %q", got.Project, c.wantProject)
			}
		})
	}
}

// TestResolveRepoPinCarriesNoProject — elle pin proje ÖNERMEZ.
//
// Pin, konvansiyonu atlar; atlanan konvansiyondan proje türetmek, kullanıcı
// başka bir projedeki bir depoyu pinlediğinde onu yanlış projede aratırdı.
// Ayardaki açık Project bu durumda tek doğru kaynaktır.
func TestResolveRepoPinCarriesNoProject(t *testing.T) {
	got := ResolveRepo("bsa-payments-core-prod", "https://tfs.example.com/DefaultCollection/OTHER/_git/payments-core", ResolveConfig{})
	if got.Source != RepoSourcePin {
		t.Fatalf("Source=%q, pin bekleniyordu", got.Source)
	}
	if got.Project != "" {
		t.Errorf("pin yolunda Project=%q — konvansiyon atlanmışken proje "+
			"türetmek başka projedeki bir depoyu yanlış yerde aratır", got.Project)
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
