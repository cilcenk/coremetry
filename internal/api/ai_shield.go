package api

// ai_shield.go — v0.10.421 (CoSRE denetimi E6): her Explain sarmalayıcısının
// ortak kalkan SAYACI. Tek tanım (E6/E7): cevapta geçen, prompt'ta
// gösterilmemiş servis-biçimli ad sayısı → ai_calls.shield_hits. Metni
// DEĞİŞTİRMEZ (shieldNarrative'in ⚠ satırı ayrı ve yalnız auto-explain'de);
// operatörün gördüğü cevap aynı, /ai "uydurma oranı"nı ölçer.

import "github.com/cilcenk/coremetry/internal/rca"

func aiShield(prompt, answer string) uint8 {
	return rca.CountUnknownEntities(rca.LowerKnownSet(), prompt, answer)
}
