package chstore

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/cilcenk/coremetry/internal/config"
)

// RunQuery connects to the CONFIGURED ClickHouse (the same hosts / auth /
// database the server uses — NO schema migration, unlike New) and writes the
// result of `sql` as tab-separated rows (a header line + one line per row) to w.
//
// It backs the `coremetry ch "<sql>"` debug subcommand so an operator can query
// the (often external) ClickHouse from inside the pod without bundling a 460 MB
// clickhouse-client and without re-entering the host list + credentials — the
// connection is read straight from the running config (config.yaml +
// COREMETRY_CH_* env).
func RunQuery(ctx context.Context, cfg config.CHConfig, sql string, w io.Writer) error {
	opts := &clickhouse.Options{
		Addr:        cfg.Hosts(),
		Auth:        clickhouse.Auth{Database: cfg.Database, Username: cfg.Username, Password: cfg.Password},
		DialTimeout: 10 * time.Second,
	}
	if cfg.Secure {
		opts.TLS = &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Reflectively allocate a destination of each column's ScanType — the driver
	// rejects bare *interface{} for typed columns and we have no schema knowledge
	// (operator-typed SQL). Same pattern as the /admin SQL playground.
	cts := rows.ColumnTypes()
	cols := make([]string, len(cts))
	scanTypes := make([]reflect.Type, len(cts))
	for i, c := range cts {
		cols[i] = c.Name()
		scanTypes[i] = c.ScanType()
		if scanTypes[i] == nil {
			scanTypes[i] = reflect.TypeOf("")
		}
	}
	if len(cols) > 0 {
		fmt.Fprintln(w, strings.Join(cols, "\t"))
	}
	for rows.Next() {
		ptrs := make([]any, len(cts))
		dests := make([]reflect.Value, len(cts))
		for i := range cts {
			v := reflect.New(scanTypes[i])
			ptrs[i] = v.Interface()
			dests[i] = v.Elem()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		cells := make([]string, len(cts))
		for i := range cts {
			cells[i] = fmt.Sprintf("%v", dests[i].Interface())
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	return rows.Err()
}
