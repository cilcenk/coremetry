package logstore

import (
	"reflect"
	"testing"
)

// v0.9.1250 — /logs histogramının cluster + namespace kırılımı. ES'te
// bu iki değer TEK alanda yaşamıyor (pipeline'a göre farklı doküman
// yolu), terms agg ise tek alan ister. Çözüm: aday alan başına bir
// terms agg (tek _search) + Go tarafında GRUP ADINA göre birleştirme.
// Buradaki testler üç saf çekirdeği çakıyor: alan çözümü (400'ü
// önleyen kural), gövde kurucusu (v0.8.3 maliyet korumaları), ve
// birleştirme (belgeli çift-sayım vakası dahil).

func cap1(types ...string) traceFieldCap {
	return traceFieldCap{Types: types, Searchable: true, Aggregatable: true}
}

func TestResolveGroupAggFields(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		caps       map[string]traceFieldCap
		want       []string
	}{
		{
			// Yönetilen (OpenShift cluster-logging) şekil: alan DÜZ
			// keyword, .keyword alt-alanı YOK. Operatörün gerçek prod
			// şekli — yalnız .keyword agg'lansaydı eksen her şeye
			// "OTHER" derdi.
			name:       "düz keyword alan doğrudan agg'lanır",
			candidates: []string{"openshift.labels.cluster"},
			caps:       map[string]traceFieldCap{"openshift.labels.cluster": cap1("keyword")},
			want:       []string{"openshift.labels.cluster"},
		},
		{
			// Dinamik mapping: text + .keyword alt-alanı. Çıplak alana
			// terms agg = fielddata 400 → TÜM histogram isteği ölür.
			name:       "text+keyword çokluk alanında .keyword seçilir",
			candidates: []string{"kubernetes.namespace_name"},
			caps: map[string]traceFieldCap{
				"kubernetes.namespace_name":         {Types: []string{"text"}, Searchable: true},
				"kubernetes.namespace_name.keyword": cap1("keyword"),
			},
			want: []string{"kubernetes.namespace_name.keyword"},
		},
		{
			name:       "keyword yolu olmayan aday ATLANIR (istek-öldüren 400)",
			candidates: []string{"body"},
			caps:       map[string]traceFieldCap{"body": {Types: []string{"text"}, Searchable: true}},
			want:       []string{},
		},
		{
			name:       "aggregatable=false keyword de atlanır",
			candidates: []string{"cluster"},
			caps:       map[string]traceFieldCap{"cluster": {Types: []string{"keyword"}, Searchable: true}},
			want:       []string{},
		},
		{
			name:       "mapping'de olmayan adaylar sorguya HİÇ girmez",
			candidates: esClusterFields,
			caps:       map[string]traceFieldCap{"openshift.labels.cluster": cap1("keyword")},
			want:       []string{"openshift.labels.cluster"},
		},
		{
			name:       "birden çok aday çözülürse hepsi agg'lanır (sıra korunur)",
			candidates: []string{"a", "b"},
			caps: map[string]traceFieldCap{
				"a":         cap1("keyword"),
				"b.keyword": cap1("keyword"),
			},
			want: []string{"a", "b.keyword"},
		},
		{
			// İki aday aynı fiziksel alana çözülürse ikinci düşer:
			// aksi hâlde her doküman iki kez sayılırdı.
			name:       "aynı fiziksel alana çözülen ikinci aday elenir",
			candidates: []string{"ns", "ns.keyword"},
			caps: map[string]traceFieldCap{
				"ns":         {Types: []string{"text"}, Searchable: true},
				"ns.keyword": cap1("keyword"),
			},
			want: []string{"ns.keyword"},
		},
		{
			name:       "hiç aday çözülmezse boş (çağıran _total'a düşer)",
			candidates: esNamespaceFields,
			caps:       map[string]traceFieldCap{},
			want:       []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveGroupAggFields(c.candidates, c.caps)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("resolveGroupAggFields = %v, want %v", got, c.want)
			}
		})
	}
}

func TestGroupAxisCandidates(t *testing.T) {
	// Çok-alanlı eksenler aday taşır; tek-alanlı/olmayanlar taşımaz —
	// Histogram bu nil'e bakarak field_caps probe'unu hiç yapmıyor.
	for _, axis := range []string{"cluster", "namespace"} {
		if len(groupAxisCandidates(axis)) == 0 {
			t.Errorf("%s ekseni aday alan taşımalı", axis)
		}
	}
	for _, axis := range []string{"", "service", "severity", "pod", "Cluster"} {
		if got := groupAxisCandidates(axis); got != nil {
			t.Errorf("groupAxisCandidates(%q) = %v, nil bekleniyordu", axis, got)
		}
	}
	// Cluster aday listesi FİLTRE ile tek kaynak olmalı: kırılımın
	// adlandırdığı cluster, yanındaki select'in bulabildiği cluster
	// olmak zorunda (v0.8.265 sınıfı).
	if !reflect.DeepEqual(groupAxisCandidates("cluster"), esClusterFields) {
		t.Error("cluster adayları filtrenin esClusterFields listesinden gelmeli")
	}
	if !reflect.DeepEqual(groupAxisCandidates("namespace"), esNamespaceFields) {
		t.Error("namespace adayları arama kısayolunun esNamespaceFields listesinden gelmeli")
	}
}

func TestBuildMultiFieldHistogramBody_CostGuards(t *testing.T) {
	fields := []string{"openshift.labels.cluster", "resource_attributes.cluster"}
	body := buildMultiFieldHistogramBody(map[string]any{"match_all": map[string]any{}},
		"@timestamp", fields, 60, "10s")

	if body["size"] != 0 {
		t.Errorf("size:0 yok (agg-only istek): %v", body["size"])
	}
	if body["track_total_hits"] != false {
		t.Errorf("track_total_hits:false yok: %v", body["track_total_hits"])
	}
	if body["timeout"] != "10s" {
		t.Errorf("yumuşak timeout yok: %v", body["timeout"])
	}
	aggs, ok := body["aggs"].(map[string]any)
	if !ok {
		t.Fatalf("aggs map değil: %T", body["aggs"])
	}
	// OTHER sentezi total_buckets'a bağlı (v0.5.396) — o giderse
	// frontend'in "diğer" katlaması sessizce eksik sayar.
	if _, ok := aggs["total_buckets"]; !ok {
		t.Error("total_buckets yok — OTHER sentezi çalışmaz")
	}
	// ADAY ALAN BAŞINA BİR terms agg (mutasyon: tek alana indir → kırmızı).
	if len(aggs) != len(fields)+1 {
		t.Fatalf("agg sayısı %d, aday başına bir terms + total_buckets bekleniyordu (%d)", len(aggs), len(fields)+1)
	}
	for i, want := range fields {
		g, ok := aggs[multiFieldAggKey(i)].(map[string]any)
		if !ok {
			t.Fatalf("%s agg'i yok", multiFieldAggKey(i))
		}
		terms, _ := g["terms"].(map[string]any)
		if terms["field"] != want {
			t.Errorf("%s alanı %v, %q bekleniyordu", multiFieldAggKey(i), terms["field"], want)
		}
		if terms["size"] != groupTermsSize || terms["shard_size"] != groupTermsShardSize {
			t.Errorf("%s size/shard_size kıskacı yok: %v/%v", multiFieldAggKey(i), terms["size"], terms["shard_size"])
		}
		sub, _ := g["aggs"].(map[string]any)
		dh := dateHistOf(t, sub["buckets"])
		if dh["fixed_interval"] != "60s" {
			t.Errorf("fixed_interval %v, 60s bekleniyordu", dh["fixed_interval"])
		}
		// v0.8.3 olayının koruması: seyrek kovalar (CH ile aynı).
		if dh["min_doc_count"] != 1 {
			t.Errorf("min_doc_count:1 yok (v0.8.3 yoğun-ızgara olayı): %v", dh["min_doc_count"])
		}
	}
}

func TestMergeFieldSeries(t *testing.T) {
	const t0, t1 = int64(1_000), int64(2_000)
	s := func(name string, pts ...[2]int64) LogSeries {
		out := LogSeries{Name: name}
		for _, p := range pts {
			out.Points = append(out.Points, LogPoint{T: p[0], V: p[1]})
		}
		return out
	}

	t.Run("aynı ad iki alandan gelirse TOPLANIR", func(t *testing.T) {
		got := mergeFieldSeries([][]LogSeries{
			{s("ocp5", [2]int64{t0, 10}, [2]int64{t1, 4})},
			{s("ocp5", [2]int64{t0, 5})},
		})
		if len(got) != 1 || got[0].Name != "ocp5" {
			t.Fatalf("tek birleşmiş seri bekleniyordu: %+v", got)
		}
		want := []LogPoint{{T: t0, V: 15}, {T: t1, V: 4}}
		if !reflect.DeepEqual(got[0].Points, want) {
			t.Fatalf("noktalar %+v, %+v bekleniyordu", got[0].Points, want)
		}
	})

	t.Run("farklı adlar toplama göre azalan sıralanır", func(t *testing.T) {
		got := mergeFieldSeries([][]LogSeries{
			{s("küçük", [2]int64{t0, 1}), s("büyük", [2]int64{t0, 100})},
			{s("orta", [2]int64{t0, 50})},
		})
		names := []string{got[0].Name, got[1].Name, got[2].Name}
		if !reflect.DeepEqual(names, []string{"büyük", "orta", "küçük"}) {
			t.Fatalf("sıra %v", names)
		}
	})

	t.Run("BELGELİ çift-sayım: bir doküman iki alanda da değer taşırsa", func(t *testing.T) {
		// Tek bir doküman hem `openshift.labels.cluster` hem
		// `resource_attributes.cluster` alanında "ocp5" taşıyorsa iki
		// terms agg'i de onu sayar → birleşmiş bant 1 yerine 2 gösterir.
		// Nadir (yollar farklı pipeline'lara ait) ve sınırlı: şişme adlı
		// bandın içinde kalır, OTHER sentezi (total − toplam, 0'da
		// kelepçeli) aritmetiği yutar. Davranış bilinçli — burada
		// çakılıyor ki sessizce "düzeltilip" alan atlanmasın.
		got := mergeFieldSeries([][]LogSeries{
			{s("ocp5", [2]int64{t0, 1})},
			{s("ocp5", [2]int64{t0, 1})},
		})
		if got[0].Points[0].V != 2 {
			t.Fatalf("belgeli çift-sayım değişti: %+v", got[0].Points)
		}
	})

	t.Run("boş girdi boş çıktı (grafik çizmez, çökmez)", func(t *testing.T) {
		if got := mergeFieldSeries(nil); len(got) != 0 {
			t.Fatalf("boş bekleniyordu: %+v", got)
		}
	})

	t.Run("seyrek kovalar: eksik zaman damgası uydurulmaz", func(t *testing.T) {
		got := mergeFieldSeries([][]LogSeries{
			{s("a", [2]int64{t1, 3})},
			{s("b", [2]int64{t0, 9})},
		})
		for _, sr := range got {
			if len(sr.Points) != 1 {
				t.Fatalf("%s: seyrek seri dolduruldu: %+v", sr.Name, sr.Points)
			}
		}
	})
}
