package api

// pickPodService — pod↔servis eşleşmesinin saf çözümleme yarısı
// (v0.9.11, korelasyon audit'i §2.1): PodServiceMap'in adaylarından
// pod satırına yazılacak TEK servisi seçer.
//
//   - tek aday → o (olağan yol; canlıda pod'ların ~tamamı)
//   - birden çok aday → metadata namespace'i pod'un namespace'iyle
//     eşleşen aday (sidecar/agent belirsizliği)
//   - hâlâ belirsiz → "" (yanlış etiket basmaktansa boş — audit
//     kuralı; UI '—' gösterir)
func pickPodService(candidates []string, podNamespace string, svcNamespace map[string]string) string {
	switch len(candidates) {
	case 0:
		return ""
	case 1:
		return candidates[0]
	}
	if podNamespace != "" {
		match := ""
		for _, c := range candidates {
			if svcNamespace[c] == podNamespace {
				if match != "" {
					return "" // aynı namespace'te iki aday — hâlâ belirsiz
				}
				match = c
			}
		}
		if match != "" {
			return match
		}
	}
	return ""
}
