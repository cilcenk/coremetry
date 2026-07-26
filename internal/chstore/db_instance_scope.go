package chstore

// Instance scoping for DB receiver metrics — v0.9.279.
//
// Every engine panel narrows its own reads to one instance, and every one of
// them does it with a two-key OR because the receivers disagree about where the
// instance identity lives. The drill modal that opens FROM those panels did not
// narrow at all, so a deployment with two Oracle instances rendered both blended
// into one line — a chart that is not wrong-looking, just wrong.
//
// The obvious fix — pass the instance as an ordinary equality filter — does not
// work, and the reason is worth recording. Measured on live data:
//
//	service_name        attr[instance]        res[service.name]
//	oracledb-receiver   corebank-scan.prod    oracledb-receiver
//	oracledb-receiver   corebank-dg.prod      oracledb-receiver
//
// service_name is IDENTICAL for both instances (it names the receiver, not the
// database), so the existing `service` parameter cannot separate them. The
// discriminating field is attr[instance] — but only in setups that tag at
// attribute level. Older Oracle receivers tag at RESOURCE level instead, and
// there attr[instance] is absent: a flat equality filter would match nothing
// and the modal would draw an empty chart, which is a worse failure than the
// blended one it replaced.
//
// Hence the OR, and hence this single source of truth: the panel and the modal
// must resolve an instance the same way or they disagree about what they are
// showing. dbInstanceScopeKeys is pinned against the four panel helpers by
// TestDBInstanceScopeMatchesPanels.

// dbInstanceScopeKeys maps an engine to the (attribute, fallback) pair its
// receiver family uses. The second element of each pair is looked up in
// res_keys for oracle and in attr_keys for the rest — mirroring the panels.
var dbInstanceScopeKeys = map[string]struct {
	attrKey  string
	altKey   string
	altIsRes bool
}{
	"oracle":     {attrKey: "instance", altKey: "service.name", altIsRes: true},
	"postgresql": {attrKey: "postgresql.instance.name", altKey: "server.address"},
	"mysql":      {attrKey: "mysql.instance.name", altKey: "server.address"},
	"redis":      {attrKey: "redis.instance.name", altKey: "server.address"},
}

// dbInstanceScopeClause returns a WHERE predicate (without a leading AND —
// whereClause joins) and the number of bind args it expects, which is always 2:
// the instance value goes in twice, once per branch of the OR.
//
// An unknown engine, or an empty/"unknown" instance, returns "" — the caller
// then does not narrow, which is exactly what the panels do via their
// hasInstanceFilter switch. Deployments that do not tag per instance keep
// working; they simply see the blended reading they already saw, rather than an
// empty one.
func dbInstanceScopeClause(engine, instance string) (string, int) {
	if instance == "" || instance == "unknown" {
		return "", 0
	}
	k, ok := dbInstanceScopeKeys[engine]
	if !ok {
		return "", 0
	}
	alt := "attr_values[indexOf(attr_keys, '" + k.altKey + "')] = ?"
	if k.altIsRes {
		alt = "res_values[indexOf(res_keys, '" + k.altKey + "')] = ?"
	}
	return "(attr_values[indexOf(attr_keys, '" + k.attrKey + "')] = ? OR " + alt + ")", 2
}
