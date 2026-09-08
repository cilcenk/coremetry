package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/mcptools"
)

// v0.10.559 — Operatör-raporlu (prod, 2026-09-08): "Takımımın servisleri nasıl?"
// takım sorusu yalnız SY (SRE) takımlarını listeliyor, UG (uygulama, ownerTeam)
// takımları çıkmıyor. Kök neden: liste servis sayısına göre sıralı katalogdan
// ilk 8; SRE takımları çok servise sahip olduğundan tavanı tek başına
// dolduruyor. Sözleşme: seçenekler TÜRE göre iki gruptan (owner = uygulama,
// sre) dönüşümlü seçilir, her tür temsil edilir, aynı takım bir kez, toplam
// tavan korunur; metin iki grubu ve dışarıda kalan sayıyı SÖYLER.
func TestTeamAskOptionsMixesKinds(t *testing.T) {
	entries := []mcptools.TeamCatalogueEntry{
		{Team: "SY-A", Services: 40, SRE: 40}, {Team: "SY-B", Services: 35, SRE: 35}, {Team: "SY-C", Services: 30, SRE: 30},
		{Team: "SY-D", Services: 25, SRE: 25}, {Team: "SY-E", Services: 20, SRE: 20}, {Team: "SY-F", Services: 18, SRE: 18},
		{Team: "SY-G", Services: 15, SRE: 15}, {Team: "SY-H", Services: 12, SRE: 12}, {Team: "SY-I", Services: 11, SRE: 11},
		{Team: "UG-1", Services: 4, Owner: 4}, {Team: "UG-2", Services: 3, Owner: 3}, {Team: "UG-3", Services: 2, Owner: 2},
		{Team: "Both", Services: 5, Owner: 3, SRE: 2},
	}
	opts, owner, sre, rest := teamAskOptions(entries, 8)
	if len(opts) != 8 {
		t.Fatalf("tavan 8: %v", opts)
	}
	joined := strings.Join(opts, ",")
	for _, ug := range []string{"UG-1", "UG-2", "UG-3", "Both"} {
		if !strings.Contains(joined, ug) {
			t.Errorf("uygulama takımı %s listede olmalı: %v", ug, opts)
		}
	}
	if !strings.Contains(joined, "SY-A") {
		t.Errorf("en büyük SRE takımı da listede olmalı: %v", opts)
	}
	if opts[0] != "Both" || strings.Count(joined, "Both") != 1 {
		t.Errorf("owner grubu önce, en büyük owner takımı ilk ve tek: %v", opts)
	}
	if len(owner) != 4 || len(sre) != 9 || rest != len(entries)-8 {
		t.Fatalf("gruplar: owner=%d sre=%d rest=%d", len(owner), len(sre), rest)
	}
	if o, _, _, r := teamAskOptions(nil, 8); len(o) != 0 || r != 0 {
		t.Fatal("boş katalog")
	}
}

func TestTeamAskEvidenceNamesBothGroups(t *testing.T) {
	entries := []mcptools.TeamCatalogueEntry{{Team: "UG-1", Services: 2, Owner: 2}, {Team: "SY-A", Services: 9, SRE: 9}, {Team: "SY-B", Services: 8, SRE: 8}}
	txt := renderTeamAskEvidenceTR(entries, 2)
	for _, w := range []string{"Uygulama takımları", "UG-1", "SRE takımları", "SY-A", "1 takım daha"} {
		if !strings.Contains(txt, w) {
			t.Errorf("metin %q taşımalı:\n%s", w, txt)
		}
	}
	if strings.Contains(txt, "en büyük takımlar") {
		t.Fatal("eski 'en büyük takımlar' cümlesi kalmamalı — SRE tekeli mesajı")
	}
}
