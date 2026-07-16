package chstore

import (
	"database/sql"
	"errors"
)

// isNoRows — "the query legitimately matched nothing", as opposed to a
// real read failure (timeout, network, bad SQL). clickhouse-go surfaces
// the standard sql.ErrNoRows for an empty QueryRow, but user.go's
// precedent matches by message because wrapping isn't guaranteed across
// driver versions — this helper checks both so call sites never have to
// choose. v0.8.564: extracted while fixing the exemplar readers that
// swallowed EVERY scan error with a bare `_ =`, making a prod timeout
// indistinguishable from "no exemplar in window".
func isNoRows(err error) bool {
	return err != nil &&
		(errors.Is(err, sql.ErrNoRows) || err.Error() == "sql: no rows in result set")
}
