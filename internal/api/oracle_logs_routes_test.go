package api

// oracle_logs_routes_test.go — v0.10.602 (Oracle Aşama 2 dilim 4).
//
// Saf pinler: CH satırı → LogRow ikizi projeksiyonu (audit §5 hedef
// anahtarları, boş alan attribute üretmez, ekstralar verbatim, serviceName
// kaynak adı, id 53-bit ve kararlı, origin "oracle"), cache anahtarı tüm
// girdileri taşır (trace / limit / pencere), rota kalıbı kayıtlı.

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func sampleOracleRow() chstore.OracleErrorRow {
	return chstore.OracleErrorRow{
		SourceID: "o-11111111", Time: time.Unix(0, 1_757_500_000_000_000_000).UTC(), RowID: 0xFFFF_FFFF_FFFF_FFFF,
		SeverityNum: 17, SeverityText: "ERROR", Body: "ORA-01555", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		HostName: "app-01", InstanceID: "shop-payment-7f9c", OperationCode: "PAY_TRANSFER", ErrorCode: "BSA_020",
		ExternalCode: "", ErrorType: "T", ChannelCode: "MOB", TaskCode: "", RequestID: "req-42",
		AttrKeys: []string{"MCA_ERR_NUM", "MCA_ERR_SQLTEXT"}, AttrValues: []string{"7", "select 1"},
	}
}

func TestOracleLogRowProjection(t *testing.T) {
	row := oracleLogRowFromStore(sampleOracleRow(), "prod-eu")
	if row.Origin != "oracle" || row.ServiceName != "prod-eu" || row.SpanID != "" || row.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("kimlik alanları: %+v", row)
	}
	if row.Timestamp != 1_757_500_000_000_000_000 || row.Severity != 17 || row.SeverityText != "ERROR" || row.Body != "ORA-01555" {
		t.Errorf("zaman/severity/gövde: %+v", row)
	}
	want := map[string]string{
		"operation.code": "PAY_TRANSFER", "error.code": "BSA_020", "error.type": "T", "channel.code": "MOB",
		"request.id": "req-42", "MCA_ERR_NUM": "7", "MCA_ERR_SQLTEXT": "select 1",
	}
	if len(row.Attributes) != len(want) {
		t.Errorf("attribute sayısı %d, want %d: %v", len(row.Attributes), len(want), row.Attributes)
	}
	for k, v := range want {
		if row.Attributes[k] != v {
			t.Errorf("%s = %q, want %q", k, row.Attributes[k], v)
		}
	}
	for _, absent := range []string{"error.external_code", "task.code", "teller.id", "customer.id", "location"} {
		if _, ok := row.Attributes[absent]; ok {
			t.Errorf("boş alan attribute üretmemeli: %s", absent)
		}
	}
	if row.ResourceAttributes["host.name"] != "app-01" || row.ResourceAttributes["oracle.instance_id"] != "shop-payment-7f9c" ||
		row.ResourceAttributes["oracle.source"] != "prod-eu" || row.ResourceAttributes["oracle.source_id"] != "o-11111111" {
		t.Errorf("resource: %v", row.ResourceAttributes)
	}
	if _, ok := row.ResourceAttributes["service.instance.id"]; ok {
		t.Error("O4 açıkken instance_id'ye service.instance.id anlamı yüklenmez")
	}
	// id: 53-bit, pozitif, kararlı.
	if row.ID < 0 || row.ID >= 1<<53 {
		t.Errorf("id JS güvenli aralıkta olmalı: %d", row.ID)
	}
	if again := oracleLogRowFromStore(sampleOracleRow(), "prod-eu"); again.ID != row.ID {
		t.Error("id kararlı olmalı")
	}
	// Ad yoksa id düşer.
	if r := oracleLogRowFromStore(sampleOracleRow(), ""); r.ServiceName != "o-11111111" {
		t.Errorf("adsız kaynak: %q", r.ServiceName)
	}
}

func TestOracleLogsKeyCarriesAllInputs(t *testing.T) {
	from := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	to := from.Add(10 * time.Minute)
	base := oracleLogsKey("4bf92f3577b34da6a3ce929d0e0e4736", from, to, 200)
	variants := []string{
		oracleLogsKey("4bf92f3577b34da6a3ce929d0e0e4737", from, to, 200),
		oracleLogsKey("4bf92f3577b34da6a3ce929d0e0e4736", from, to, 500),
		oracleLogsKey("4bf92f3577b34da6a3ce929d0e0e4736", from.Add(-time.Minute), to, 200),
		oracleLogsKey("4bf92f3577b34da6a3ce929d0e0e4736", from, to.Add(time.Minute), 200),
	}
	for i, v := range variants {
		if v == base {
			t.Errorf("varyant %d anahtarı değiştirmiyor: %s", i, v)
		}
	}
	if oracleLogsKey("4bf92f3577b34da6a3ce929d0e0e4736", from, to, 200) != base {
		t.Error("anahtar kararlı olmalı")
	}
	// Grid: aynı 30 s kovası aynı anahtar (crafted from/to ayrı girdi basmaz).
	if oracleLogsKey("4bf92f3577b34da6a3ce929d0e0e4736", from.Add(5*time.Second), to.Add(5*time.Second), 200) != base {
		t.Error("30 s grid içinde anahtar aynı kalmalı")
	}
	if !strings.HasPrefix(base, "oracle-logs:") {
		t.Errorf("önek: %s", base)
	}
}
