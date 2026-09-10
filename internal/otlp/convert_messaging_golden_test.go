package otlp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/cilcenk/coremetry/internal/chstore"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// v0.10.553 — messaging.* golden testi (docs/audit/messaging-kafka-metrics
// -2026-09-08.md Faz 4, /otlp-converter §2.6/§5). TAM-struct karşılaştırma:
// yeni bir Span alanı eklendiğinde bu test KIRILMAK ZORUNDA.
//
// Sözleşme (bugünkü dönüşüm, bilerek):
//   - yalnız messaging.system tipli kolona iner (msg_system);
//   - messaging.destination(.name), messaging.operation(.type|.name),
//     messaging.consumer.group.name / messaging.kafka.consumer.group,
//     partition/offset/key, bootstrap.servers: HİÇBİRİ düşmez — attr dizisine
//     VERBATIM ve gelen sırayla girer (§3 sınıf A: TAŞI). Okuma tarafı coalesce
//     eder (messaging_summary_5m destination; Top-ops operation, v0.10.553).
//   - kind PRODUCER→producer, CONSUMER→consumer; status UNSET→"unset",
//     ERROR→"error" + mesaj; Time = start; Duration = end-start (ns).
func TestConvertSpanMessagingGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "span_messaging_kafka.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req tracecollpb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(raw, &req); err != nil {
		t.Fatalf("fixture protojson: %v", err)
	}
	rows, links := ConvertTraces(&req)
	if len(links) != 0 {
		t.Fatalf("link beklenmiyordu: %d", len(links))
	}
	if len(rows) != 2 {
		t.Fatalf("2 span bekleniyordu, %d", len(rows))
	}
	res := []string{"service.name", "host.name", "deployment.environment"}
	resV := []string{"payments", "payments-7c9-abc", "prod"}
	want := []chstore.Span{
		{
			TraceID: "00000000000000000000000000000001", SpanID: "0000000000000001", ParentID: "0000000000000002",
			Name: "orders publish", OpGroup: rows[0].OpGroup, Kind: "producer",
			ServiceName: "payments", HostName: "payments-7c9-abc", DeployEnv: "prod",
			StatusCode: "unset", StatusMsg: "",
			Time: time.Unix(0, 1_700_000_000_000_000_000).UTC(), Duration: 12_000_000,
			PeerService: "kafka", MsgSystem: "kafka",
			AttrKeys: []string{"messaging.system", "messaging.destination.name", "messaging.destination",
				"messaging.operation.type", "messaging.operation.name", "messaging.operation",
				"messaging.destination.partition.id", "messaging.kafka.message.key",
				"messaging.kafka.bootstrap.servers", "server.address", "peer.service"},
			AttrValues: []string{"kafka", "orders", "orders-legacy", "publish", "send", "publish", "3", "k1", "broker:9092", "broker", "kafka"},
			ResKeys:    res, ResValues: resV, Events: "", ScopeName: "io.opentelemetry.kafka-clients-2.6",
		},
		{
			TraceID: "00000000000000000000000000000001", SpanID: "0000000000000003", ParentID: "",
			Name: "orders process", OpGroup: rows[1].OpGroup, Kind: "consumer",
			ServiceName: "payments", HostName: "payments-7c9-abc", DeployEnv: "prod",
			StatusCode: "error", StatusMsg: "deserialize failed",
			Time: time.Unix(0, 1_700_000_000_020_000_000).UTC(), Duration: 5_000_000,
			MsgSystem: "kafka",
			AttrKeys: []string{"messaging.system", "messaging.destination.name", "messaging.operation.type",
				"messaging.consumer.group.name", "messaging.kafka.consumer.group",
				"messaging.kafka.message.offset", "messaging.destination.partition.id"},
			AttrValues: []string{"kafka", "orders", "process", "orders-workers", "orders-workers-legacy", "4711", "3"},
			ResKeys:    res, ResValues: resV, Events: "", ScopeName: "io.opentelemetry.kafka-clients-2.6",
		},
	}
	for i := range want {
		got := *rows[i]
		got.Time = got.Time.UTC()
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("span %d (-want +got):\n want %+v\n  got %+v", i, want[i], got)
		}
	}
	// OpGroup türetmesi bu testin konusu değil ama boş olmamalı (grup kimliği).
	for i, r := range rows {
		if r.OpGroup == "" {
			t.Logf("span %d OpGroup boş (bilgi)", i)
		}
	}
}
