// v0.9.604 — dağıtık DDL zamanaşımı boot'u öldürüyordu.
//
// Operator-reported (prod, v0.9.603 rollout'u): api rolündeki bir pod
// boot'ta ölüp crashloop'a girdi.
//
//	create database: code: 159, message: Distributed DDL task … is not
//	finished on 2 of 4 hosts (0 of them are currently executing the
//	task, 0 are inactive). They are going to execute the query in
//	background. Was waiting for 180.93 seconds, which is longer than
//	distributed_ddl_task_timeout
//
// Bu bir BAŞARISIZLIK DEĞİL — mesajın kendisi "arka planda
// çalıştıracaklar" diyor. Ölümcül saymanın bedeli kendini besliyordu:
// pod ölür, yeniden başlar, tüm DDL kümesini zaten tıkalı kuyruğa
// yeniden gönderir. Rollout'ta birden çok pod aynı anda boot ettiği
// için tam o an en kötü hâline geliyordu.
package chstore

import (
	"errors"
	"testing"
)

// prodErr — operatörün log'undaki mesajın birebir şekli.
const prodErr = `create database: code: 159, message: Distributed DDL task ` +
	`/clickhouse/task_queue/ddl/query-0000647099 is not finished on 2 of 4 hosts ` +
	`(0 of them are currently executing the task, 0 are inactive). ` +
	`They are going to execute the query in background. Was waiting for ` +
	`180.932353072 seconds, which is longer than distributed_ddl_task_timeout`

func TestDistributedDDLQueuedRecognisesProdError(t *testing.T) {
	if !isDistributedDDLQueued(errors.New(prodErr)) {
		t.Fatal("prod'da pod'u düşüren hata TANINMADI — crashloop sürer")
	}
}

// TestDistributedDDLQueuedIsNarrow — imza DAR olmalı.
//
// Yalın "code: 159" başka bir zamanaşımı da olabilir (sorgu
// zamanaşımı). Geniş tutmak alakasız bir hatayı sessizce yutardı ve
// boot bozuk bir şemayla devam ederdi — crashloop'tan daha kötüsü.
func TestDistributedDDLQueuedIsNarrow(t *testing.T) {
	cases := map[string]string{
		"çıplak 159 (sorgu zamanaşımı)": "code: 159, message: Timeout exceeded: elapsed 30.1 seconds",
		"host GERÇEKTEN düşmüş":         "code: 159, message: Distributed DDL task is not finished on 2 of 4 hosts (2 are inactive)",
		"alakasız hata":                 "code: 62, message: Syntax error",
		"boş":                           "",
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			if msg != "" {
				err = errors.New(msg)
			}
			if isDistributedDDLQueued(err) {
				t.Errorf("imza fazla geniş — %q yutuldu. Alakasız bir DDL hatasını "+
					"yutmak, boot'un bozuk şemayla devam etmesi demektir; bu "+
					"crashloop'tan KÖTÜDÜR (sessiz).", name)
			}
		})
	}
}

// TestNilIsNotQueued — nil hata "kuyruğa alındı" sayılmamalı.
func TestNilIsNotQueued(t *testing.T) {
	if isDistributedDDLQueued(nil) {
		t.Error("nil hata kuyruk imzası sayıldı")
	}
}
