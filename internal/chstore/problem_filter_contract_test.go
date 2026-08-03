// v0.9.583 — ProblemFilter'da UYGULANMAYAN alan bulunmamalı.
//
// Öncelik okuma anında hesaplanır (v0.5.210), CH kolonu değildir —
// SQL'e inemez. Ama `Priority []string` alanı yıllarca filtre
// struct'ında durdu ve ListProblems gövdesi ona HİÇ bakmadı: sadece
// "Go'da daraltmayı unutma" notuydu.
//
// Bir filtre struct'ında uygulanmayan bir alan tutmak bir TUZAKTIR ve
// iki kez ısırdı:
//
//	v0.9.342 — yorum "ListProblems Limit'i bu filtreden SONRA uygular"
//	           diyordu; hiç uygulamadı.
//	v0.9.576 — MCP list_problems alanı doldurdu, daraltmayı unuttu;
//	           priority=P1 istemek filoda yüzlerce P1 varken SIFIR
//	           sonuç döndürebiliyordu.
//
// Bu test alanın geri gelmesini engelliyor. Alan yoksa yanlışlıkla
// güvenilemez — bu oturumun tekrar eden dersi: kuralı hatırlatma,
// derleyiciye yasaklat.
package chstore

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestProblemFilterHasNoPriorityField(t *testing.T) {
	ty := reflect.TypeOf(ProblemFilter{})
	if _, ok := ty.FieldByName("Priority"); ok {
		t.Error("ProblemFilter.Priority geri gelmiş — mağaza o alana BAKMAZ " +
			"(öncelik okuma anı hesabı). Alanı doldurup daraltmayı unutan " +
			"çağıran sessizce yanlış sonuç alır. Bunun yerine: " +
			"ProblemScanLimit ile taramayı aç, FilterProblemsByPriority ile daralt.")
	}
}

// ListProblems gövdesi öncelikten HİÇ bahsetmemeli: bahsediyorsa ya
// alan geri gelmiş ya da SQL'e indirilmeye çalışılmış (ki inemez).
func TestListProblemsDoesNotMentionPriority(t *testing.T) {
	b, err := os.ReadFile("problem.go")
	if err != nil {
		t.Fatalf("problem.go okunamadı: %v", err)
	}
	src := stripGoLineComments(string(b))

	i := strings.Index(src, "func (s *Store) ListProblems(")
	if i < 0 {
		t.Fatal("ListProblems bulunamadı")
	}
	body := src[i:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if strings.Contains(body, "Priority") {
		t.Error("ListProblems gövdesi Priority'den bahsediyor — öncelik CH " +
			"kolonu DEĞİL, SQL'e inemez; okuma anında hesaplanır")
	}
}

// Daraltma ve tarama-genişletme yardımcıları DURMALI: alan silindiğine
// göre çağıranın elinde bu ikisi kalmalı, yoksa doğru yolu yapamaz.
func TestPriorityNarrowingHelpersExist(t *testing.T) {
	if got := ProblemScanLimit(25, true); got <= 25 {
		t.Errorf("ProblemScanLimit daraltmada taramayı açmıyor (%d) — çağıran "+
			"sayfa boyutu kadar tarar ve P1'ler pencereye giremez", got)
	}
	rows := []Problem{{ID: "a", Priority: "P1"}, {ID: "b", Priority: "P2"}}
	if got := FilterProblemsByPriority(rows, []string{"P1"}); len(got) != 1 {
		t.Errorf("FilterProblemsByPriority daraltmıyor: %d satır", len(got))
	}
}
