package api

// ai_shield.go — v0.10.421 (CoSRE denetimi E6): her Explain sarmalayıcısının
// ortak kalkan SAYACI. Tek tanım (E6/E7): cevapta geçen, prompt'ta
// gösterilmemiş servis-biçimli ad sayısı → ai_calls.shield_hits. Metni
// DEĞİŞTİRMEZ (shieldNarrative'in ⚠ satırı ayrı ve yalnız auto-explain'de);
// operatörün gördüğü cevap aynı, /ai "uydurma oranı"nı ölçer.
//
// v0.10.431 — iki düzeltme (inceleme bulguları):
//  1. Bilinen küme BOŞTU: canlı katalogdaki gerçek bir servis prompt'ta
//     geçmiyorsa "uydurma" sayılıyordu. Şimdi sayaç canlı servis
//     adlarıyla tohumlanır (guidedServiceNames, 60 sn önbellek) — uydurma
//     tanımı "katalogda da prompt'ta da olmayan ad"a daralır.
//  2. Sohbet yolu: v0.10.421 "sohbet döngüsü sayılmaz" derken chat-intent
//     (JSON sınıflandırma, prompt = soru) ve chat-general (prompt = soru)
//     yüzeyleri varsayılan kalkanı alıyordu; orada prompt yalnız kullanıcı
//     metni olduğundan her tireli teknik terim isabet oluyordu. Bu iki
//     yüzeyde varsayılan nil (sayılmaz); chat-guided/chat-drawer kanıt
//     paketi taşıdığından sayılmaya devam eder.

import (
	"context"

	"github.com/cilcenk/coremetry/internal/rca"
)

// aiShield — tohumsuz sayaç (testler + bilinen kümesi olmayan çağıranlar).
func aiShield(prompt, answer string) uint8 {
	return aiShieldWith(nil)(prompt, answer)
}

// aiShieldWith — bilinen adlarla tohumlanmış sayaç. SAF; küme her çağrıda
// taze kurulur (CountUnknownEntities kümeyi mutasyona uğratır).
func aiShieldWith(known []string) func(prompt, answer string) uint8 {
	return func(prompt, answer string) uint8 {
		return rca.CountUnknownEntities(rca.LowerKnownSet(known...), prompt, answer)
	}
}

// shieldCountsSurface — kalkanın anlamlı olduğu yüzeyler: prompt modele
// gösterilen kanıtı taşıyor olmalı. Sohbet sınıflandırıcısı ve genel
// sohbet yalnız kullanıcı sorusunu görür → sayılmaz (nil = 0).
func shieldCountsSurface(surface string) bool {
	switch surface {
	case "chat-intent", "chat-intent-none", "chat-general":
		return false
	}
	return true
}

// aiShieldFor — sarmalayıcıların varsayılan kalkanı: yüzey uygunsa canlı
// katalogla tohumlu sayaç, değilse nil. Katalog erişimi yoksa (bare test
// sunucusu, boot öncesi) tohumsuz sayaç.
func (s *Server) aiShieldFor(ctx context.Context, surface string) func(prompt, answer string) uint8 {
	if !shieldCountsSurface(surface) {
		return nil
	}
	if s == nil || s.store == nil || s.cache == nil {
		return aiShieldWith(nil)
	}
	return aiShieldWith(s.guidedServiceNames(ctx))
}
