package chstore

import "testing"

// exception_fingerprint_test.go — v0.8.500 regression (operatör-raporlu:
// "aynı başlıklı problemleri coremetry neden gruplamıyor?"). Mesajına
// RequestId(UUID) + Time(ISO-8601) gömen SDK'larda (Azure
// RequestFailedException şekli) normalizeMessage'ın \b sınırlı rakam
// maskesi "11T12" / "0943159Z" gibi harfe bitişik parçaları
// kaçırıyordu — stack'siz exception event'lerinde her occurrence ayrı
// parmak izi → /problems inbox'ında occurrence başına bir satır.

// azureAuthMsg — ekrandaki mesajın birebir şekli (generic Azure SDK
// metni), yalnız kimlik/damga alanları parametreli.
func azureAuthMsg(reqID, tISO, date string) string {
	return "Server failed to authenticate the request. Make sure the value of Authorization header is formed correctly including the signature.\n" +
		"RequestId:" + reqID + "\n" +
		"Time:" + tISO + "\n" +
		"Status: 403 (Server failed to authenticate the request.)\n" +
		"ErrorCode: AuthenticationFailed\n" +
		"Headers:\nServer: Microsoft-HTTPAPI/2.0\nx-ms-request-id: " + reqID + "\nDate: " + date + "\nContent-Length: 299\n"
}

func TestFingerprintMergesDynamicIDVariants(t *testing.T) {
	cases := []struct {
		name  string
		a, b  string
		merge bool
	}{
		{
			"Azure auth şekli — RequestId+Time+Date farkı birleşir",
			azureAuthMsg("0f267c76-d002-00b9-6532-110742000000", "2026-07-11T12:42:32.0943159Z", "Sat, 11 Jul 2026 12:42:31 GMT"),
			azureAuthMsg("ab12cd34-e567-89f0-1234-567890abcdef", "2026-07-11T15:03:07.1234567Z", "Sat, 11 Jul 2026 15:03:07 GMT"),
			true,
		},
		// v0.9.466 (hacim denetimi #8) — üç yeni maske sınıfı: tırnaklı
		// dinamik değer, e-posta, karışık harf+rakam token. Her sınıf
		// İKİ yönde test edilir (birleşmeli / ayrık kalmalı) —
		// unit-mixing disiplini.
		{
			"tırnaklı dinamik değer birleşir",
			`user 'ali.veli' not found in cache`,
			`user 'ayse.k' not found in cache`,
			true,
		},
		{
			"çift tırnak da birleşir",
			`config key "payment.retry.limit" missing`,
			`config key "fraud.check.timeout" missing`,
			true,
		},
		{
			"e-posta farkı birleşir",
			`notification failed for cil.cenk@example.com: bounce`,
			`notification failed for x.y@bank.com.tr: bounce`,
			true,
		},
		{
			"karışık token (session id) birleşir",
			`session abc123def456gh expired`,
			`session zz99xx88yy77ww expired`,
			true,
		},
		{
			"farklı mesaj ŞEKLİ ayrık kalır (tırnak maskesi her şeyi yutmaz)",
			`connection refused to 'db-primary'`,
			`timeout waiting for 'db-primary'`,
			false,
		},
		{
			"düz kelimeler maskelenmez — ayrık kalır",
			`invalid password for account`,
			`invalid username for account`,
			false,
		},
		{
			"çıplak ISO damga farkı birleşir",
			"deadline exceeded at 2026-07-11T12:42:32Z",
			"deadline exceeded at 2026-07-12T09:01:55Z",
			true,
		},
		{
			"harfe bitişik sayı (conn_id / port) birleşir",
			"failed to read packet from 10.244.5.12:9000 (conn_id=333): read: EOF",
			"failed to read packet from 10.244.5.199:9000 (conn_id=91): read: EOF",
			true,
		},
		{
			"çıplak uzun hex kimlik (trace id) birleşir",
			"span 4bf92f3577b34da6a3ce929d0e0e4736 not sampled",
			"span 00f067aa0ba902b7aa0ba902b74da6a3 not sampled",
			true,
		},
		{
			"metni gerçekten farklı hatalar AYRI kalır",
			"connection refused",
			"certificate has expired",
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fpA := FingerprintException("SomeError", c.a, "svc", "")
			fpB := FingerprintException("SomeError", c.b, "svc", "")
			if (fpA == fpB) != c.merge {
				t.Fatalf("merge=%v bekleniyordu; fpA=%s fpB=%s\nnormA=%q\nnormB=%q",
					c.merge, fpA, fpB, normalizeMessage(c.a), normalizeMessage(c.b))
			}
		})
	}
}

// Aynı hata iki serviste iki ayrı satır kalır (takım-triage sözleşmesi).
func TestFingerprintKeepsServicesDistinct(t *testing.T) {
	m := azureAuthMsg("0f267c76-d002-00b9-6532-110742000000", "2026-07-11T12:42:32.0943159Z", "Sat, 11 Jul 2026 12:42:31 GMT")
	if FingerprintException("AuthenticationFailed", m, "svc-a", "") ==
		FingerprintException("AuthenticationFailed", m, "svc-b", "") {
		t.Fatal("farklı servisler aynı parmak izine indirgenmemeli")
	}
}
