// exception_env_test.go — v0.9.941 (UX denetimi B1/K8).
//
// /problems Exceptions sekmesi Topbar'ın `?env=` seçicisini GÖSTERİYOR ama
// uygulamıyordu (`envApplies={false}`): operatör prod'u seçip int/uat
// exception'larını listede görmeye devam ediyordu. Seçicinin VARLIĞI yalan
// söylüyordu — kapsam yalanı, boş liste değil, en pahalı türden.
//
// Bu testler eksen BİRLEŞİMİNİ pinliyor. Kesişimin kendisi
// intersectServices'te (v0.9.3xx) zaten testli; burada pinlenen, ortam
// eksenının o kesişime DOĞRU ŞEKİLDE girmesi — özellikle nil tuzağı:
// intersectServices'in sözleşmesi "nil = bu eksenden kısıt yok", yani
// "çözüldü ama üyesi yok" nil olarak geçirilirse ortam süzgeci SESSİZCE
// kaybolur ve operatör yine tüm ortamları görür.
package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestExceptionEnvTeamIntersection(t *testing.T) {
	cases := []struct {
		name        string
		team, env   []string
		want        []string
		wantEmptyOK bool // boş sonuç MEŞRU mu (boş sayfa) yoksa kısıtsız mı
	}{
		{
			name: "yalnız takım — ortam ekseni kısıt koymaz",
			team: []string{"a", "b"}, env: nil,
			want: []string{"a", "b"},
		},
		{
			name: "yalnız ortam — takım ekseni kısıt koymaz",
			team: nil, env: []string{"a-prod", "b-prod"},
			want: []string{"a-prod", "b-prod"},
		},
		{
			name: "ikisi birden — cevap KESİŞİM",
			team: []string{"a", "b", "c"}, env: []string{"b", "c", "d"},
			want: []string{"b", "c"},
		},
		{
			name: "kesişim boş — BOŞ SAYFA, kısıtsız değil",
			team: []string{"a"}, env: []string{"b"},
			want: []string{}, wantEmptyOK: true,
		},
		{
			// NİL TUZAĞI: "ortam çözüldü ama üyesi yok" boş DİLİM olarak
			// geçmeli. nil geçseydi intersectServices onu "kısıt yok"
			// sayar, takımın TÜM ortamlardaki servisleri dönerdi.
			name: "ortam çözüldü ama üyesiz — takımı ezmeli",
			team: []string{"a", "b"}, env: []string{},
			want: []string{}, wantEmptyOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := intersectServices(c.team, c.env)
			if len(got) == 0 && len(c.want) == 0 {
				if !c.wantEmptyOK && c.team == nil && c.env == nil {
					t.Fatal("iki eksen de kısıtsızken boş küme döndü — sayfa boşalırdı")
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("intersectServices(%v, %v) = %v, want %v", c.team, c.env, got, c.want)
			}
		})
	}
}

// TestExceptionEnvNilIsNotEmpty — sözleşmenin kendisi. Bu ayrım
// bozulursa yukarıdaki tablo hâlâ geçer ama handler yanlış tarafa düşer.
func TestExceptionEnvNilIsNotEmpty(t *testing.T) {
	team := []string{"a", "b"}
	if got := intersectServices(team, nil); !reflect.DeepEqual(got, team) {
		t.Errorf("nil ortam ekseni takımı daralttı: %v", got)
	}
	if got := intersectServices(team, []string{}); len(got) != 0 {
		t.Errorf("üyesiz ortam takımı daraltmadı: %v — operatör prod seçip "+
			"tüm ortamları görmeye devam ederdi", got)
	}
}

// TestListExceptionGroupsAppliesEnv — handler'ın env'i GERÇEKTEN okuyup
// filtreye bağladığının kaynak pini.
//
// Neden kaynak pini: kesişim mantığı saf ve testli, ama K8'in kendisi
// mantık hatası DEĞİLDİ — handler env'i hiç OKUMUYORDU. Saf fonksiyonu
// test etmek o sınıfı yakalamaz.
func TestListExceptionGroupsAppliesEnv(t *testing.T) {
	// YORUMSUZ kaynak: bu depoda kaynak-tarama testi İKİ KEZ, kendi
	// düzeltmesinin açıklayıcı yorumunda alıntılanan dizgeyle eşleşip
	// yanlış yeşil verdi (rca_record_test.go:140). Aşağıdaki dizgelerin
	// hepsi bu dosyanın ve api.go'nun yorumlarında da geçiyor.
	src := readAPISourceNoComments(t, "api.go")
	i := strings.Index(src, "func (s *Server) listExceptionGroups(")
	if i < 0 {
		t.Fatal("listExceptionGroups bulunamadı")
	}
	body := src[i+1:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{
		`q.Get("env")`,                         // env okunuyor
		"EnvMemberServices(ctx, env)",          // üye servislere çözülüyor
		"f.Services = envServices",             // filtreye BAĞLANIYOR
		"intersectServices(svcs, envServices)", // takımla kesişiyor
	} {
		if !contains(body, want) {
			t.Errorf("listExceptionGroups %q içermiyor — ortam süzgeci bağlı değil", want)
		}
	}
	// Soft-fail duruşu: env haritası hatası listeyi KISITSIZ bırakmalı
	// (kardeş yüzeylerle aynı — geçici bir CH tökezlemesi ateş eden bir
	// P1'i saklamamalı).
	// Soft-fail KODDA: hata dalı yalnız loglar, envScoped false kalır ve
	// filtre kısıtsız devam eder. (Yorum metnine bakamayız — yorumlar
	// ayıklanmış durumda, bilerek.)
	if !contains(body, `log.Printf("[exception-groups] env %q`) {
		t.Error("env çözüm hatasında soft-fail dalı yok — bir CH tökezlemesi " +
			"listeyi sessizce boşaltabilirdi ya da hata döndürürdü")
	}
}
