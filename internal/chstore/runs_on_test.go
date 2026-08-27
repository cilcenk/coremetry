package chstore

// runs_on_test.go — v0.10.93, dikey eksen dilim ①.
//
// Üç sözleşme: (1) node: öneki kimlik sözlüğünde ve RUNS_ON yazıcısı
// AYNI yazımı kullanıyor; (2) çağrı-grafiği okuyan HER sorgu RUNS_ON
// kenarını dışlıyor — kapı elle yazılmış dosya listesine değil PAKETİN
// TAMAMINA kurulu ([[feedback-guard-bound-to-slice-name]]); (3) yazıcı
// JOIN'siz ve alan akmayan kurulumda boş küme üretecek şekilde
// has(res_keys,…) süzgeçli.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNodePrefixIdentity(t *testing.T) {
	kind, name := TopologyNodeIdentity("node:worker-3")
	if kind != NodeKindNode || name != "worker-3" {
		t.Fatalf("node: kimliği çözülmedi: kind=%q name=%q", kind, name)
	}
	if TopologyEndpointKind("node:worker-3") != NodeKindNode {
		t.Error("uç kind'ı node değil")
	}
	// Önek tablosunda TEK yazım: filtre sabiti aynı önekten türemeli.
	if !strings.Contains(TopoCallEdgeFilterSQL, "'node:'") {
		t.Errorf("dışlama parçası node: önekini taşımıyor: %q", TopoCallEdgeFilterSQL)
	}
	found := false
	for _, p := range TopologyNodeIDPrefixes {
		if p.Prefix == "node:" && p.Kind == NodeKindNode {
			found = true
		}
	}
	if !found {
		t.Error("node: öneki sözlükte yok — TopologyEndpointKind yanlış tipler")
	}
}

// chstoreSources — paketin test-olmayan .go kaynakları.
func chstoreSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(n))
		if err != nil {
			t.Fatal(err)
		}
		out[n] = string(b)
	}
	return out
}

// TestCallGraphReadersExcludeRunsOn — PAKET GENELİ tarama: topology_edges_5m
// OKUYAN her sorgu ya RUNS_ON'u dışlar ya açık bir node_kind eşitliğiyle
// zaten dar kapsamlıdır ya da bilinen kolon-probu'dur. Yeni bir okuma
// eklenirse ve süzgeçsizse bu test kırmızıya döner — dosya listesi değil,
// desen kapısı.
func TestCallGraphReadersExcludeRunsOn(t *testing.T) {
	probeRe := regexp.MustCompile(`SELECT cluster FROM topology_edges_5m LIMIT 1`)
	for name, src := range chstoreSources(t) {
		reads := strings.Count(src, "FROM topology_edges_5m")
		if reads == 0 {
			continue
		}
		// v0.10.94 — RUNS_ON-kapsamlı okuma da meşru: startsWith ile
		// yalnız node: kenarlarını seçen sorgu (GetRunsOnPlacements)
		// çağrı grafiği DEĞİL, dikey eksenin kendi okumasıdır. İğne
		// parça parça kuruluyor — Go backtick içinde backtick olmaz.
		scopedNeedle := "startsWith(child_node, '" + "`" + "+nodeIDPrefix+" + "`" + "')"
		covered := strings.Count(src, "TopoCallEdgeFilterSQL") +
			strings.Count(src, "node_kind = 'external'") +
			strings.Count(src, scopedNeedle) +
			len(probeRe.FindAllString(src, -1))
		// Sabitin TANIMI da bir sayım verirdi; tanım dosyası okuma
		// içermiyor (identity.go'da FROM yok) — yine de netlik için
		// tanımı düş.
		if name == "identity.go" {
			covered -= strings.Count(src, "const TopoCallEdgeFilterSQL")
		}
		if covered < reads {
			t.Errorf("%s: %d topology_edges_5m okuması, %d süzgeç — süzgeçsiz "+
				"okuma RUNS_ON kenarını ÇAĞRI kenarı sanır (korelatör kirlenir)",
				name, reads, covered)
		}
	}
}

// TestRunsOnPassShape — yazıcının yük taşıyan yazımları.
func TestRunsOnPassShape(t *testing.T) {
	src := chstoreSources(t)["topology.go"]
	for needle, why := range map[string]string{
		"topology bucket runs-on pass":     "4. pass hiç yok",
		"has(res_keys, 'k8s.node.name')":   "boş-küme süzgeci yok — alan akmayan kurulumda tüm span'ler taranır",
		"'runs_on'            AS protocol": "protokol damgası yok",
		// Çocuk ID öneki SABİTTEN türer (nodeIDPrefix) — kaynakta literal
		// değil sabit-kullanımı aranır: derleyici yazımı zaten kilitliyor,
		// bu iğne yalnız birinin sabiti literale çevirmesini yakalar.
		"nodeIDPrefix+`', node_name)":        "çocuk ID öneki sözlük sabitinden türemiyor",
		"topK(5)(pod)         AS top_labels": "pod etiketi düşmüş — operatörün 'hangi pod' sorusu cevapsız kalır",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("%s (aranan: %q)", why, needle)
		}
	}
	// JOIN'sizlik: pass'in gövdesinde JOIN geçmemeli. Pass metnini
	// runs-on yorumundan sonraki ilk Exec bloğuyla sınırla.
	i := strings.Index(src, "RUNS_ON pass")
	j := strings.Index(src[i:], "runs-on pass: %w")
	body := src[i : i+j]
	if strings.Contains(body, "JOIN spans") || strings.Contains(body, "INNER JOIN") {
		t.Error("RUNS_ON pass JOIN içeriyor — tasarım JOIN'siz tek tablo taramasıydı")
	}
}
