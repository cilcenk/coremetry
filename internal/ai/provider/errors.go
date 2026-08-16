package provider

import "fmt"

// errors.go — taşımanın hata sözleşmesi.
//
// İki tüketicisi var ve ikisi de METNE bakıyordu:
//
//  1. copilot.isQuotaErr — mesajda " 429" / "quota" / "rate limit"
//     arayıp kota devre-kesicisini kuruyor (v0.9.200). Yeni bir cümle
//     kurmak kesiciyi sessizce silahsız bırakırdı.
//  2. JSON merdiveni — "response_format'ı anlamadım" ailesini
//     (400, 422, 501) statüden ayırt ediyor.
//
// (1) metinle çalışmaya devam ediyor: string sözleşmesi eskiden beri
// pinli. (2) artık metinden statü ayıklamıyor — HTTPError tipli hata
// statüyü TAŞIYOR, çağıran errors.As ile okuyor. Bu, "gövde metninde
// üç haneli sayı ara" sınıfını tamamen kapatır.
type HTTPError struct {
	// Provider — hata metnindeki önek. Sağlayıcı adının kendisi değil,
	// eski kodun BASTIĞI etiket: "openai-compat", "anthropic",
	// "github copilot".
	Provider string
	Status   int
	// Body — kırpılmış yanıt gövdesi (okuma tavanı maxRespBytes).
	Body string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %d: %s", e.Provider, e.Status, e.Body)
}

// Sağlayıcı etiketleri. Hata metinlerinin ve log satırlarının tek
// kaynağı — "openai-compat 429: …" biçimi v0.8.x'ten beri aynı.
const (
	labelOpenAI    = "openai-compat"
	labelAnthropic = "anthropic"
	labelGitHub    = "github copilot"
	// labelEmbedding — rag'ın eski cümlesi ("embedding endpoint %d: …",
	// v0.8.438). Operatör upload hatasında bu metni görüyor; Faz 1.4
	// gövdeyi taşıdı, metni DEĞİL.
	labelEmbedding = "embedding endpoint"
)
