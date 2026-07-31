package api

import (
	"strings"
	"testing"
)

// v0.9.482 pinleri — trace explain kanıt paketinin ÇIKARILMASI.
//
// Operatör raporu (prod fotoğrafı, v0.9.479 takibi): "Explain trace"
// trace'i VE ilişkili logları okuyup cevaplıyor; ama "Chat'te devam et"
// sonrası çekmece sohbeti yalnız açıklamanın METNİNİ görüyordu — "logda
// ne yazıyor" gibi takipler kör cevaplanıyordu. Kanıt montajı handler'dan
// buildTraceExplainInput'a taşındı (iki çağıran: explain handler'ı +
// copilotChatDrawer).
//
// ÇIKARMA = davranış değişikliği DEĞİL. Aşağıdaki golden pin, prompt
// ifadesinin v0.9.462'deki hâliyle BAYT-BAYT aynı kaldığını garanti eder;
// bir gün biri fmt ifadesini "iyileştirirse" test düşer.

func TestTraceExplainUserGolden(t *testing.T) {
	cases := []struct {
		name                     string
		id                       string
		analyzed, total          int
		payload, logsBlock, want string
	}{
		{
			// v0.9.462 öncesinden beri değişmeyen taban ifade: kesme yok, log yok.
			name: "tam trace, log yok",
			id:   "abc123", analyzed: 3, total: 3, payload: `[{"name":"GET /x"}]`,
			want: "Trace abc123 with 3 spans:\n```json\n[{\"name\":\"GET /x\"}]\n```",
		},
		{
			// Dürüstlük notu (v0.9.462): "head" değil, hata+yavaşlık öncelikli.
			name: "kesilmiş trace, log yok",
			id:   "abc123", analyzed: 100, total: 5000, payload: `[]`,
			want: "Trace abc123 with 100 spans (trace'in tamamı 5000 span; hatalar + en yavaşlar öncelikli 100 span analiz edildi):\n```json\n[]\n```",
		},
		{
			// Log bloğu SONA eklenir (v0.9.166 log devri) — çekmece budaması
			// da bu sırayı varsayar (clampDrawerEvidence loglara dokunmaz).
			name: "tam trace + log bloğu",
			id:   "t1", analyzed: 1, total: 1, payload: `[{"id":"s1"}]`, logsBlock: "\n\nLOGLAR:\nboom",
			want: "Trace t1 with 1 spans:\n```json\n[{\"id\":\"s1\"}]\n```\n\nLOGLAR:\nboom",
		},
		{
			name: "kesilmiş trace + log bloğu",
			id:   "t2", analyzed: 100, total: 101, payload: `[]`, logsBlock: "\n\nLOGLAR:\nboom",
			want: "Trace t2 with 100 spans (trace'in tamamı 101 span; hatalar + en yavaşlar öncelikli 100 span analiz edildi):\n```json\n[]\n```\n\nLOGLAR:\nboom",
		},
		{
			// analyzed == total sınırı: not YAZILMAZ (yanlış "kısmi analiz"
			// iddiası operatörü yanıltırdı).
			name: "sınır: analyzed == total",
			id:   "t3", analyzed: 100, total: 100, payload: `[]`,
			want: "Trace t3 with 100 spans:\n```json\n[]\n```",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := traceExplainUser(c.id, c.analyzed, c.total, c.payload, c.logsBlock); got != c.want {
				t.Fatalf("prompt ifadesi DEĞİŞTİ (explain çıktısı bozulur)\n got=%q\nwant=%q", got, c.want)
			}
		})
	}
}

func TestTraceEvidenceSpanIDs(t *testing.T) {
	err := func(id string, dur float64) traceLite {
		return traceLite{SpanID: id, DurationMs: dur, Status: "error"}
	}
	ok := func(id string, dur float64) traceLite { return traceLite{SpanID: id, DurationMs: dur} }

	cases := []struct {
		name string
		in   []traceLite
		want []string
	}{
		{name: "boş", in: nil, want: []string{}},
		{
			name: "hatasız trace → yalnız en yavaş",
			in:   []traceLite{ok("a", 10), ok("b", 90), ok("c", 30)},
			want: []string{"b"},
		},
		{
			name: "hatalar + ayrı en yavaş",
			in:   []traceLite{err("e1", 5), ok("s", 900), err("e2", 7)},
			want: []string{"e1", "e2", "s"},
		},
		{
			// En yavaş span ZATEN hata listesindeyse iki kez yazılmaz —
			// UI aynı satırı iki kez kutulardı.
			name: "en yavaş span zaten hata → tekrar yok",
			in:   []traceLite{err("e1", 900), ok("s", 5)},
			want: []string{"e1"},
		},
		{
			// Hata fırtınası: kanıt listesi 5 hatada durur, en yavaş yine eklenir.
			name: "hata cap'i 5",
			in: []traceLite{
				err("e1", 1), err("e2", 1), err("e3", 1), err("e4", 1),
				err("e5", 1), err("e6", 1), ok("slow", 99),
			},
			want: []string{"e1", "e2", "e3", "e4", "e5", "slow"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := traceEvidenceSpanIDs(c.in)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("kanıt=%v want=%v", got, c.want)
			}
		})
	}
}
