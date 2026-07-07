// Coremetry demo — extended real-bank topology (v0.7.64).
//
// The base generator (main.go) covers the retail hot path. This file
// widens the mesh into the surrounding bank — ~20 more services across
// lending, onboarding/compliance, payment rails, open banking, ATM,
// disputes, rewards and treasury — so the topology graph reads like an
// actual institution rather than a handful of nodes.
//
// Per the bank's real stack, every new service persists to the Oracle
// core (db.system=oracle, peer.service="oracle") and communicates
// asynchronously over Kafka (peer.service="kafka"); only genuinely
// third-party calls (credit bureau, SWIFT network, card scheme, market
// data) leave the mesh as external HTTP. So Oracle + Kafka sit at the
// centre of the graph with heavy fan-in, exactly like production.
//
// Everything self-registers in init() — extra services merge into the
// `services` map and extra scenarios append to the weighted driver
// list, so main.go stays untouched.
package main

import (
	mrand "math/rand/v2"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// ms1 is one millisecond — keeps the dense span-offset arithmetic
// readable (4*ms1 vs 4*time.Millisecond).
const ms1 = time.Millisecond

// ─── Runtime fingerprint presets (mirror main.go's polyglot mesh) ───────────────
var (
	rtGo     = [4]string{"go", "go", "1.22.5", "go version go1.22.5 linux/amd64"}
	rtJava21 = [4]string{"java", "OpenJDK Runtime Environment", "21.0.2+13", "OpenJDK 64-Bit Server VM Temurin-21.0.2+13 (build 21.0.2+13-LTS)"}
	rtJava17 = [4]string{"java", "OpenJDK Runtime Environment", "17.0.10+7", "OpenJDK 64-Bit Server VM Temurin-17.0.10+7 (build 17.0.10+7-LTS)"}
	rtPy     = [4]string{"python", "CPython", "3.12.2", "CPython 3.12.2 (main, Feb  6 2024, 20:19:44) [GCC 12.2.0]"}
	rtNode   = [4]string{"nodejs", "node", "20.11.1", "Node.js v20.11.1"}
	rtDotnet = [4]string{".net", ".NET", "8.0.4", ".NET 8.0.4"}
	rtRust   = [4]string{"rust", "rust", "1.78.0", "rustc 1.78.0 (9b00956e5 2024-04-29)"}
)

func svc(name string, rt [4]string, pods ...string) Service {
	return Service{Name: name, Pods: pods, Lang: rt[0], RuntimeName: rt[1], RuntimeVersion: rt[2], RuntimeDesc: rt[3]}
}

// extraServices is the wider bank mesh. Pod fleets sized roughly by how
// hot the service is so the backtrace (caller × pod) view varies.
var extraServices = []Service{
	// Lending
	svc("loan-service", rtJava21, "loan-prod-1", "loan-prod-2", "loan-prod-3"),
	svc("mortgage-service", rtJava21, "mort-prod-1", "mort-prod-2"),
	svc("underwriting-service", rtPy, "uw-prod-1", "uw-prod-2"),
	svc("pricing-service", rtGo, "price-prod-1", "price-prod-2"),
	svc("credit-bureau-adapter", rtGo, "bureau-prod-1", "bureau-prod-2"),
	// Onboarding / compliance
	svc("onboarding-service", rtNode, "onb-prod-1", "onb-prod-2"),
	svc("kyc-service", rtPy, "kyc-prod-1", "kyc-prod-2", "kyc-prod-3"),
	svc("sanctions-service", rtPy, "sanc-prod-1", "sanc-prod-2"),
	svc("document-service", rtGo, "doc-prod-1", "doc-prod-2"),
	// Payment rails
	svc("payments-orchestrator", rtGo, "payorch-prod-1", "payorch-prod-2", "payorch-prod-3"),
	svc("sepa-service", rtJava21, "sepa-prod-1", "sepa-prod-2"),
	svc("swift-gateway", rtJava17, "swift-prod-1", "swift-prod-2"),
	svc("settlement-service", rtJava21, "settle-prod-1", "settle-prod-2"),
	svc("scheme-adapter", rtDotnet, "scheme-prod-1", "scheme-prod-2"),
	// Channels
	svc("openbanking-gateway", rtGo, "obgw-prod-1", "obgw-prod-2"),
	svc("consent-service", rtGo, "consent-prod-1", "consent-prod-2"),
	svc("atm-service", rtGo, "atm-prod-1", "atm-prod-2"),
	svc("cash-management", rtGo, "cash-prod-1"),
	// Post-transaction
	svc("dispute-service", rtDotnet, "disp-prod-1", "disp-prod-2"),
	svc("chargeback-service", rtDotnet, "chgbk-prod-1"),
	svc("rewards-service", rtGo, "rwd-prod-1", "rwd-prod-2"),
	// Treasury / data
	svc("treasury-service", rtRust, "treas-prod-1"),
	svc("position-service", rtRust, "pos-prod-1", "pos-prod-2"),
	svc("market-data-service", rtRust, "mkt-prod-1", "mkt-prod-2"),
	svc("datawarehouse-etl", rtPy, "dwh-prod-1"),
}

type scenarioReg struct {
	name   string
	weight int
	fn     scenario
}

func init() {
	for _, s := range extraServices {
		services[s.Name] = s
	}
	extra := []scenarioReg{
		{"LoanApplication", 2, scenarioLoanApplication},
		{"Onboarding", 2, scenarioOnboarding},
		{"OpenBankingAISP", 2, scenarioOpenBanking},
		{"SepaPayment", 2, scenarioSepaPayment},
		{"AtmWithdrawal", 2, scenarioAtmWithdrawal},
		{"CardDispute", 1, scenarioCardDispute},
		{"RewardsEvent", 2, scenarioRewardsEvent},
		{"ChargebackEvent", 1, scenarioChargebackEvent},
		{"TreasuryReval", 1, scenarioTreasuryReval},
	}
	for _, e := range extra {
		scenarios = append(scenarios, struct {
			name   string
			weight int
			fn     scenario
		}{e.name, e.weight, e.fn})
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────────

// ext builds an outbound external-HTTP client-span attribute map to a
// genuine third party (credit bureau, SWIFT network, card scheme,
// market-data feed). peer.service names the external system so the
// topology shows the bank reaching outside its own mesh.
func ext(peer, method, url string, status int) map[string]any {
	return kv(
		"http.method", method, "http.url", url, "http.status_code", status,
		"server.address", peer, "peer.service", peer, "network.transport", "tcp",
	)
}

// kafkaPub appends a Kafka producer span on `topic` under `parent` and
// records it in the shared producer ring so later consumer traces can
// link back to it (v0.8.335).
func kafkaPub(t *Trace, service string, parent []byte, topic string, off time.Duration, ref string) {
	sid := t.Add(service, "kafka.publish "+topic, tracepb.Span_SPAN_KIND_PRODUCER, parent, off, dur(8, 25),
		kv("messaging.system", "kafka", "messaging.destination", topic, "messaging.operation", "publish",
			"peer.service", "kafka", "banking.txn_ref", ref), true, "")
	kafkaLinks.record(topic, t.traceID, sid)
	M.RecordBiz("kafka.events_published")
}

// kycHop appends a KYC verification sub-call (caller → kyc-service →
// Oracle) anchored at offsetMs into the parent span.
func kycHop(t *Trace, parent []byte, caller, subject string, offsetMs int) {
	off := time.Duration(offsetMs) * ms1
	t.Add(caller, "kyc-service/Verify", tracepb.Span_SPAN_KIND_CLIENT, parent, off, dur(30, 90),
		kv("rpc.system", "grpc", "rpc.method", "Verify", "peer.service", "kyc-service"), true, "")
	cv := t.Add("kyc-service", "KycService.Verify", tracepb.Span_SPAN_KIND_SERVER, parent, off+2*ms1, dur(26, 84),
		kv("rpc.system", "grpc", "rpc.method", "Verify", "banking.subject", subject), true, "")
	t.Add("kyc-service", "SELECT CUSTOMER_KYC", tracepb.Span_SPAN_KIND_CLIENT, cv, off+6*ms1, dur(4, 18),
		oraDB("COREBANK", "SELECT", "CUSTOMER_KYC",
			"SELECT KYC_STATUS, RISK_RATING, VERIFIED_TS FROM CUSTOMER_KYC WHERE SUBJECT_ID = :1", oracleDG), true, "")
}

// sancHop appends a sanctions / watchlist screen (caller →
// sanctions-service → Oracle) anchored at offsetMs.
func sancHop(t *Trace, parent []byte, caller, subject string, offsetMs int) {
	off := time.Duration(offsetMs) * ms1
	t.Add(caller, "sanctions-service/Screen", tracepb.Span_SPAN_KIND_CLIENT, parent, off, dur(20, 80),
		kv("rpc.system", "grpc", "rpc.method", "Screen", "peer.service", "sanctions-service"), true, "")
	cv := t.Add("sanctions-service", "SanctionsService.Screen", tracepb.Span_SPAN_KIND_SERVER, parent, off+2*ms1, dur(16, 74),
		kv("rpc.system", "grpc", "rpc.method", "Screen", "banking.subject", subject), true, "")
	t.Add("sanctions-service", "SELECT SANCTIONS_WATCHLIST", tracepb.Span_SPAN_KIND_CLIENT, cv, off+5*ms1, dur(3, 16),
		oraDB("COREBANK", "SELECT", "SANCTIONS_WATCHLIST",
			"SELECT LIST_ID, MATCH_SCORE FROM SANCTIONS_WATCHLIST WHERE NORMALISED_NAME = :1", oracleDG), true, "")
}

// ─── Scenarios ──────────────────────────────────────────────────────────────────

// scenarioLoanApplication: lending hot path + compliance fan-out.
// mobile-bff → api-gateway → loan-service, which writes the application
// to Oracle, runs KYC + sanctions (Oracle), pulls an external credit
// bureau, prices + underwrites, then disburses via the core ledger and
// emits a loan.approved Kafka event. ~12% decline.
func scenarioLoanApplication() *Trace {
	t := NewTrace()
	total := dur(600, 1400)
	cust := custID()
	amt := amount(2000, 75000)
	cur := ccy()

	declined := rollFail(12)
	httpStatus := iff(declined, 422, 201)
	M.RecordHTTP("api-gateway", "POST", "/api/v1/loans/apply", httpStatus, ms(total))
	M.RecordDB("loan-service", "oracle", "INSERT", ms(total)*0.2)
	M.RecordBiz("loans.applied")
	if declined {
		M.RecordBiz("loans.declined")
	} else {
		M.RecordBiz("loans.approved")
	}

	fe := t.Add("mobile-bff", "POST /loans/apply", tracepb.Span_SPAN_KIND_CLIENT, nil, 0, total,
		kv("http.method", "POST", "http.url", "/loans/apply", "banking.customer_id", cust,
			"banking.amount", amt, "banking.currency", cur, "peer.service", "api-gateway"),
		!declined, ifErr(declined, "loan declined"))
	api := t.Add("api-gateway", "POST /api/v1/loans/apply", tracepb.Span_SPAN_KIND_SERVER, fe, 4*ms1, total-8*ms1,
		kv("http.method", "POST", "http.route", "/api/v1/loans/apply", "http.status_code", httpStatus,
			"banking.customer_id", cust), !declined, ifErr(declined, "422: loan policy decline"))

	loan := t.Add("loan-service", "LoanService.Apply", tracepb.Span_SPAN_KIND_SERVER, api, 12*ms1, total-24*ms1,
		kv("rpc.system", "grpc", "rpc.method", "Apply", "peer.service", "loan-service",
			"banking.customer_id", cust, "banking.amount", amt), !declined,
		ifErr(declined, "underwriting decision: DECLINE"))
	t.Add("loan-service", "INSERT LOAN_APPLICATIONS", tracepb.Span_SPAN_KIND_CLIENT, loan, 16*ms1, dur(8, 26),
		oraDB("LOANS", "INSERT", "LOAN_APPLICATIONS",
			"INSERT INTO LOAN_APPLICATIONS(CUST_ID, AMOUNT, CCY, STATUS) VALUES(:1,:2,:3,'PENDING')", oracleCore), true, "")

	kycHop(t, loan, "loan-service", cust, 30)
	sancHop(t, loan, "loan-service", cust, 70)

	// External credit-bureau pull (Experian-style) via the adapter.
	bureauScore := 300 + mrand.IntN(550)
	bsv := t.Add("credit-bureau-adapter", "CreditBureau.Pull", tracepb.Span_SPAN_KIND_SERVER, loan, 122*ms1, dur(74, 230),
		kv("rpc.system", "grpc", "rpc.method", "Pull", "banking.customer_id", cust, "credit.score", bureauScore), true, "")
	t.Add("loan-service", "credit-bureau-adapter/Pull", tracepb.Span_SPAN_KIND_CLIENT, loan, 120*ms1, dur(80, 240),
		kv("rpc.system", "grpc", "rpc.method", "Pull", "peer.service", "credit-bureau-adapter"), true, "")
	t.Add("credit-bureau-adapter", "GET experian /score", tracepb.Span_SPAN_KIND_CLIENT, bsv, 126*ms1, dur(60, 200),
		ext("credit-bureau", "GET", "https://api.experian.example/v3/score", 200), true, "")

	// Risk-based pricing.
	t.Add("loan-service", "pricing-service/Quote", tracepb.Span_SPAN_KIND_CLIENT, loan, 360*ms1, dur(20, 70),
		kv("rpc.system", "grpc", "rpc.method", "Quote", "peer.service", "pricing-service"), true, "")
	t.Add("pricing-service", "PricingService.Quote", tracepb.Span_SPAN_KIND_SERVER, loan, 362*ms1, dur(16, 60),
		kv("rpc.system", "grpc", "rpc.method", "Quote", "credit.score", bureauScore, "banking.amount", amt), true, "")

	// Underwriting model decision (Python).
	t.Add("loan-service", "underwriting-service/Decide", tracepb.Span_SPAN_KIND_CLIENT, loan, 420*ms1, dur(60, 180),
		kv("rpc.system", "grpc", "rpc.method", "Decide", "peer.service", "underwriting-service"),
		!declined, ifErr(declined, "DECLINE"))
	uwv := t.Add("underwriting-service", "Underwriting.Decide", tracepb.Span_SPAN_KIND_SERVER, loan, 422*ms1, dur(54, 170),
		kv("rpc.system", "grpc", "rpc.method", "Decide", "ml.model", "credit-risk-xgb-v4",
			"underwriting.decision", iff(declined, "DECLINE", "APPROVE")),
		!declined, ifErr(declined, "PD above cutoff for product"))
	t.Add("underwriting-service", "redis.HGETALL feat:cust", tracepb.Span_SPAN_KIND_CLIENT, uwv, 426*ms1, dur(1, 5),
		kv("db.system", "redis", "db.operation", "HGETALL", "peer.service", "redis"), true, "")

	if declined {
		return t
	}

	// Disburse via the core ledger + emit the approval event.
	lg := t.Add("ledger-service", "LedgerService.Disburse", tracepb.Span_SPAN_KIND_SERVER, loan, 562*ms1, dur(74, 190),
		kv("rpc.system", "grpc", "rpc.method", "Disburse", "banking.amount", amt), true, "")
	t.Add("loan-service", "ledger-service/Disburse", tracepb.Span_SPAN_KIND_CLIENT, loan, 560*ms1, dur(80, 200),
		kv("rpc.system", "grpc", "rpc.method", "Disburse", "peer.service", "ledger-service"), true, "")
	t.Add("ledger-service", "BEGIN PKG_LEDGER.DISBURSE_LOAN", tracepb.Span_SPAN_KIND_CLIENT, lg, 566*ms1, dur(40, 140),
		oraDB("COREBANK", "BEGIN", "GL_POSTINGS", "BEGIN PKG_LEDGER.DISBURSE_LOAN(:1,:2,:3); END;", oracleCore), true, "")
	kafkaPub(t, "loan-service", loan, "loan.approved", 660*ms1, cust)
	return t
}

// scenarioOnboarding: new-customer KYC/AML onboarding.
// onboarding-service → kyc (Oracle) → document-service (S3 + Oracle) →
// sanctions (Oracle) → account create (Oracle) → kafka.
func scenarioOnboarding() *Trace {
	t := NewTrace()
	total := dur(500, 1100)
	cust := custID()
	rejected := rollFail(9)
	httpStatus := iff(rejected, 409, 201)
	M.RecordHTTP("api-gateway", "POST", "/api/v1/onboarding", httpStatus, ms(total))
	M.RecordDB("onboarding-service", "oracle", "INSERT", ms(total)*0.2)
	M.RecordBiz("onboarding.started")
	if rejected {
		M.RecordBiz("onboarding.rejected")
	} else {
		M.RecordBiz("onboarding.completed")
	}

	fe := t.Add("mobile-bff", "POST /onboarding", tracepb.Span_SPAN_KIND_CLIENT, nil, 0, total,
		kv("http.method", "POST", "http.url", "/onboarding", "peer.service", "api-gateway"),
		!rejected, ifErr(rejected, "KYC rejected"))
	api := t.Add("api-gateway", "POST /api/v1/onboarding", tracepb.Span_SPAN_KIND_SERVER, fe, 4*ms1, total-8*ms1,
		kv("http.method", "POST", "http.route", "/api/v1/onboarding", "http.status_code", httpStatus,
			"banking.customer_id", cust), !rejected, ifErr(rejected, "409: KYC failed"))
	onb := t.Add("onboarding-service", "Onboarding.Register", tracepb.Span_SPAN_KIND_SERVER, api, 10*ms1, total-22*ms1,
		kv("rpc.system", "grpc", "rpc.method", "Register", "peer.service", "onboarding-service",
			"banking.customer_id", cust), !rejected, ifErr(rejected, "KYC verification failed"))

	kycHop(t, onb, "onboarding-service", cust, 20)
	// Document capture → S3 + Oracle metadata.
	docv := t.Add("document-service", "DocumentService.Store", tracepb.Span_SPAN_KIND_SERVER, onb, 122*ms1, dur(34, 110),
		kv("rpc.system", "grpc", "rpc.method", "Store", "banking.customer_id", cust), true, "")
	t.Add("onboarding-service", "document-service/Store", tracepb.Span_SPAN_KIND_CLIENT, onb, 120*ms1, dur(40, 120),
		kv("rpc.system", "grpc", "rpc.method", "Store", "peer.service", "document-service"), true, "")
	t.Add("document-service", "s3.PutObject kyc-doc", tracepb.Span_SPAN_KIND_CLIENT, docv, 126*ms1, dur(20, 60),
		kv("http.method", "PUT", "http.url", "https://s3.eu-west-1.amazonaws.com/bank-kyc-docs",
			"http.status_code", 200, "peer.service", "s3"), true, "")
	t.Add("document-service", "INSERT DOC_METADATA", tracepb.Span_SPAN_KIND_CLIENT, docv, 150*ms1, dur(6, 22),
		oraDB("COREBANK", "INSERT", "DOC_METADATA",
			"INSERT INTO DOC_METADATA(CUST_ID, DOC_TYPE, S3_KEY) VALUES(:1,:2,:3)", oracleCore), true, "")

	sancHop(t, onb, "onboarding-service", cust, 220)
	if rejected {
		return t
	}

	// Create the customer in the core.
	accv := t.Add("account-service", "AccountService.CreateCustomer", tracepb.Span_SPAN_KIND_SERVER, onb, 362*ms1, dur(34, 110),
		kv("rpc.system", "grpc", "rpc.method", "CreateCustomer", "banking.customer_id", cust), true, "")
	t.Add("onboarding-service", "account-service/CreateCustomer", tracepb.Span_SPAN_KIND_CLIENT, onb, 360*ms1, dur(40, 120),
		kv("rpc.system", "grpc", "rpc.method", "CreateCustomer", "peer.service", "account-service"), true, "")
	t.Add("account-service", "INSERT CUSTOMERS", tracepb.Span_SPAN_KIND_CLIENT, accv, 366*ms1, dur(10, 40),
		oraDB("COREBANK", "INSERT", "CUSTOMERS",
			"INSERT INTO CUSTOMERS(CUST_ID, FULL_NAME, SEGMENT, KYC_STATUS) VALUES(:1,:2,:3,'VERIFIED')", oracleCore), true, "")
	kafkaPub(t, "onboarding-service", onb, "customer.onboarded", 440*ms1, cust)
	return t
}

// scenarioOpenBanking: third-party (TPP) AISP/PISP through the partner
// gateway. Roots at openbanking-gateway, checks consent (Oracle), reads
// accounts (Oracle), optionally initiates a payment.
func scenarioOpenBanking() *Trace {
	t := NewTrace()
	total := dur(200, 520)
	cust := custID()
	isPISP := mrand.IntN(100) < 40
	revoked := rollFail(6)
	httpStatus := iff(revoked, 403, 200)
	M.RecordHTTP("openbanking-gateway", "GET", "/open-banking/v3.1/accounts", httpStatus, ms(total))
	M.RecordDB("consent-service", "oracle", "SELECT", ms(total)*0.2)
	M.RecordBiz("openbanking.requests")

	gw := t.Add("openbanking-gateway", "GET /open-banking/v3.1/accounts", tracepb.Span_SPAN_KIND_SERVER, nil, 0, total,
		kv("http.method", "GET", "http.route", "/open-banking/v3.1/accounts", "http.status_code", httpStatus,
			"peer.service", "openbanking-gateway", "openbanking.tpp", "ACME-AISP-042"),
		!revoked, ifErr(revoked, "403: consent revoked"))

	csv := t.Add("consent-service", "ConsentService.Verify", tracepb.Span_SPAN_KIND_SERVER, gw, 8*ms1, dur(8, 34),
		kv("rpc.system", "grpc", "rpc.method", "Verify", "banking.customer_id", cust),
		!revoked, ifErr(revoked, "consent status REVOKED"))
	t.Add("openbanking-gateway", "consent-service/Verify", tracepb.Span_SPAN_KIND_CLIENT, gw, 6*ms1, dur(10, 40),
		kv("rpc.system", "grpc", "rpc.method", "Verify", "peer.service", "consent-service"),
		!revoked, ifErr(revoked, "consent revoked"))
	t.Add("consent-service", "SELECT CONSENTS", tracepb.Span_SPAN_KIND_CLIENT, csv, 10*ms1, dur(2, 10),
		oraDB("COREBANK", "SELECT", "CONSENTS",
			"SELECT SCOPE, EXPIRES_TS, STATUS FROM CONSENTS WHERE TPP_ID = :1 AND CUST_ID = :2", oracleDG), true, "")
	if revoked {
		return t
	}

	// Account information.
	accv := t.Add("account-service", "AccountService.ListAccounts", tracepb.Span_SPAN_KIND_SERVER, gw, 52*ms1, dur(26, 100),
		kv("rpc.system", "grpc", "rpc.method", "ListAccounts", "banking.customer_id", cust), true, "")
	t.Add("openbanking-gateway", "account-service/ListAccounts", tracepb.Span_SPAN_KIND_CLIENT, gw, 50*ms1, dur(30, 110),
		kv("rpc.system", "grpc", "rpc.method", "ListAccounts", "peer.service", "account-service"), true, "")
	t.Add("account-service", "SELECT ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT, accv, 56*ms1, dur(8, 30),
		oraDB("COREBANK", "SELECT", "ACCOUNTS",
			"SELECT ACCT_ID, IBAN, BALANCE, CCY FROM ACCOUNTS WHERE CUST_ID = :1", oracleDG), true, "")

	if isPISP {
		amt := amount(5, 2000)
		t.Add("openbanking-gateway", "payments-orchestrator/Initiate", tracepb.Span_SPAN_KIND_CLIENT, gw, 120*ms1, dur(40, 140),
			kv("rpc.system", "grpc", "rpc.method", "Initiate", "peer.service", "payments-orchestrator",
				"banking.amount", amt), true, "")
		orch := t.Add("payments-orchestrator", "PaymentsOrchestrator.Initiate", tracepb.Span_SPAN_KIND_SERVER, gw, 122*ms1, dur(34, 130),
			kv("rpc.system", "grpc", "rpc.method", "Initiate", "banking.amount", amt), true, "")
		kafkaPub(t, "payments-orchestrator", orch, "payment.initiated", 150*ms1, txnRef())
		M.RecordBiz("openbanking.payments_initiated")
	}
	return t
}

// scenarioSepaPayment: outbound SEPA / SWIFT credit transfer.
// api-gateway → payments-orchestrator → sanctions (Oracle) → rail
// (sepa-service | swift-gateway + external SWIFT) → settlement (Oracle)
// → kafka. ~4% sanctions block.
func scenarioSepaPayment() *Trace {
	t := NewTrace()
	total := dur(400, 900)
	ref := txnRef()
	amt := amount(50, 25000)
	crossBorder := mrand.IntN(100) < 45
	blocked := rollFail(4)
	httpStatus := iff(blocked, 451, 201)
	M.RecordHTTP("api-gateway", "POST", "/api/v1/payments/sepa", httpStatus, ms(total))
	M.RecordDB("settlement-service", "oracle", "INSERT", ms(total)*0.2)
	M.RecordBiz("payments.submitted")
	if blocked {
		M.RecordBiz("payments.sanction_blocked")
	} else {
		M.RecordBiz("payments.settled")
	}

	fe := t.Add("mobile-bff", "POST /payments/sepa", tracepb.Span_SPAN_KIND_CLIENT, nil, 0, total,
		kv("http.method", "POST", "http.url", "/payments/sepa", "banking.txn_ref", ref,
			"banking.amount", amt, "peer.service", "api-gateway"),
		!blocked, ifErr(blocked, "payment blocked"))
	api := t.Add("api-gateway", "POST /api/v1/payments/sepa", tracepb.Span_SPAN_KIND_SERVER, fe, 4*ms1, total-8*ms1,
		kv("http.method", "POST", "http.route", "/api/v1/payments/sepa", "http.status_code", httpStatus,
			"banking.txn_ref", ref), !blocked, ifErr(blocked, "451: sanctions hit"))
	orch := t.Add("payments-orchestrator", "PaymentsOrchestrator.Send", tracepb.Span_SPAN_KIND_SERVER, api, 12*ms1, total-24*ms1,
		kv("rpc.system", "grpc", "rpc.method", "Send", "peer.service", "payments-orchestrator",
			"banking.txn_ref", ref, "banking.amount", amt, "payment.scheme", iff(crossBorder, "SWIFT", "SEPA")),
		!blocked, ifErr(blocked, "sanctions screening DECLINE"))

	sancHop(t, orch, "payments-orchestrator", ref, 20)
	if blocked {
		return t
	}

	railStart := 120 * ms1
	if crossBorder {
		swv := t.Add("swift-gateway", "SwiftGateway.SendMT103", tracepb.Span_SPAN_KIND_SERVER, orch, railStart+2*ms1, dur(114, 300),
			kv("rpc.system", "grpc", "rpc.method", "SendMT103", "swift.message", "MT103", "banking.txn_ref", ref), true, "")
		t.Add("payments-orchestrator", "swift-gateway/Send", tracepb.Span_SPAN_KIND_CLIENT, orch, railStart, dur(120, 320),
			kv("rpc.system", "grpc", "rpc.method", "Send", "peer.service", "swift-gateway", "banking.txn_ref", ref), true, "")
		t.Add("swift-gateway", "POST swift-network /fin", tracepb.Span_SPAN_KIND_CLIENT, swv, railStart+8*ms1, dur(90, 260),
			ext("swift-network", "POST", "https://swift.example/fin/mt103", 202), true, "")
	} else {
		t.Add("payments-orchestrator", "sepa-service/Submit", tracepb.Span_SPAN_KIND_CLIENT, orch, railStart, dur(60, 180),
			kv("rpc.system", "grpc", "rpc.method", "Submit", "peer.service", "sepa-service", "banking.txn_ref", ref), true, "")
		t.Add("sepa-service", "SepaService.SubmitSCT", tracepb.Span_SPAN_KIND_SERVER, orch, railStart+2*ms1, dur(54, 170),
			kv("rpc.system", "grpc", "rpc.method", "SubmitSCT", "sepa.scheme", "SCT", "banking.txn_ref", ref), true, "")
	}

	// Settlement booking + core ledger.
	setStart := railStart + dur(140, 260)
	setv := t.Add("settlement-service", "SettlementService.Book", tracepb.Span_SPAN_KIND_SERVER, orch, setStart+2*ms1, dur(34, 110),
		kv("rpc.system", "grpc", "rpc.method", "Book", "banking.txn_ref", ref, "banking.amount", amt), true, "")
	t.Add("payments-orchestrator", "settlement-service/Book", tracepb.Span_SPAN_KIND_CLIENT, orch, setStart, dur(40, 120),
		kv("rpc.system", "grpc", "rpc.method", "Book", "peer.service", "settlement-service", "banking.txn_ref", ref), true, "")
	t.Add("settlement-service", "INSERT SETTLEMENTS", tracepb.Span_SPAN_KIND_CLIENT, setv, setStart+4*ms1, dur(10, 40),
		oraDB("COREBANK", "INSERT", "SETTLEMENTS",
			"INSERT INTO SETTLEMENTS(TXN_REF, AMOUNT, SCHEME, STATUS) VALUES(:1,:2,:3,'BOOKED')", oracleCore), true, "")
	kafkaPub(t, "payments-orchestrator", orch, "payment.settled", setStart+60*ms1, ref)
	return t
}

// scenarioAtmWithdrawal: cash withdrawal from the ATM channel.
// atm-service (root) → api-gateway → account (Oracle) → fraud → ledger
// debit (Oracle) → cash-management dispenses. ~3% dispense fault.
func scenarioAtmWithdrawal() *Trace {
	t := NewTrace()
	total := dur(300, 700)
	acct := acctID()
	amt := float64((mrand.IntN(40) + 1) * 10)
	nsf := rollFail(4)
	dispenseFault := !nsf && rollFail(2)
	failed := nsf || dispenseFault
	M.RecordHTTP("api-gateway", "POST", "/api/v1/atm/withdraw", iff(failed, 422, 200), ms(total))
	M.RecordDB("ledger-service", "oracle", "UPDATE", ms(total)*0.3)
	M.RecordBiz("atm.withdrawals")
	if failed {
		M.RecordBiz("atm.failed")
	}

	atm := t.Add("atm-service", "ATM.Withdraw", tracepb.Span_SPAN_KIND_SERVER, nil, 0, total,
		kv("http.method", "POST", "http.route", "/atm/withdraw", "peer.service", "atm-service",
			"atm.id", "ATM-"+pick("LON", "MAN", "EDI", "BRS")+"-"+pick("0042", "0118", "0291"),
			"banking.amount", amt), !failed, ifErr(dispenseFault, "dispense fault"))
	api := t.Add("api-gateway", "POST /api/v1/atm/withdraw", tracepb.Span_SPAN_KIND_SERVER, atm, 6*ms1, total-12*ms1,
		kv("http.method", "POST", "http.route", "/api/v1/atm/withdraw", "http.status_code", iff(failed, 422, 200),
			"banking.account_id", acct), !failed, ifErr(nsf, "insufficient funds"))

	accv := t.Add("account-service", "AccountService.GetBalance", tracepb.Span_SPAN_KIND_SERVER, api, 14*ms1, dur(12, 34),
		kv("rpc.system", "grpc", "rpc.method", "GetBalance", "banking.account_id", acct), true, "")
	t.Add("api-gateway", "account-service/GetBalance", tracepb.Span_SPAN_KIND_CLIENT, api, 12*ms1, dur(15, 40),
		kv("rpc.system", "grpc", "rpc.method", "GetBalance", "peer.service", "account-service"), true, "")
	t.Add("account-service", "SELECT ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT, accv, 18*ms1, dur(6, 22),
		oraDB("COREBANK", "SELECT", "ACCOUNTS", "SELECT AVAIL_BALANCE FROM ACCOUNTS WHERE ACCT_ID = :1", oracleDG),
		!nsf, ifErr(nsf, "available balance below requested amount"))
	if nsf {
		return t
	}
	t.Add("api-gateway", "fraud-service/ScoreAtm", tracepb.Span_SPAN_KIND_CLIENT, api, 60*ms1, dur(15, 50),
		kv("rpc.system", "grpc", "rpc.method", "ScoreAtm", "peer.service", "fraud-service"), true, "")
	t.Add("fraud-service", "FraudService.ScoreAtm", tracepb.Span_SPAN_KIND_SERVER, api, 62*ms1, dur(12, 44),
		kv("rpc.system", "grpc", "rpc.method", "ScoreAtm", "banking.account_id", acct), true, "")
	lg := t.Add("ledger-service", "LedgerService.Debit", tracepb.Span_SPAN_KIND_SERVER, api, 122*ms1, dur(34, 110),
		kv("rpc.system", "grpc", "rpc.method", "Debit", "banking.amount", amt), true, "")
	t.Add("api-gateway", "ledger-service/Debit", tracepb.Span_SPAN_KIND_CLIENT, api, 120*ms1, dur(40, 120),
		kv("rpc.system", "grpc", "rpc.method", "Debit", "peer.service", "ledger-service"), true, "")
	t.Add("ledger-service", "UPDATE ACCOUNTS", tracepb.Span_SPAN_KIND_CLIENT, lg, 126*ms1, dur(10, 40),
		oraDB("COREBANK", "UPDATE", "ACCOUNTS", "UPDATE ACCOUNTS SET BALANCE = BALANCE - :1 WHERE ACCT_ID = :2", oracleCore), true, "")

	// Cash dispense (may fault).
	t.Add("atm-service", "cash-management/Dispense", tracepb.Span_SPAN_KIND_CLIENT, atm, 220*ms1, dur(30, 90),
		kv("rpc.system", "grpc", "rpc.method", "Dispense", "peer.service", "cash-management", "banking.amount", amt),
		!dispenseFault, ifErr(dispenseFault, "cassette jam"))
	t.Add("cash-management", "CashManagement.Dispense", tracepb.Span_SPAN_KIND_SERVER, atm, 222*ms1, dur(24, 80),
		kv("rpc.system", "grpc", "rpc.method", "Dispense", "atm.cassette", pick("C1", "C2", "C3")),
		!dispenseFault, ifErr(dispenseFault, "dispenser cassette jam — reversal initiated"))
	return t
}

// scenarioCardDispute: customer raises a transaction dispute.
// dispute-service writes the case to Oracle, fetches the card txn
// (Oracle), posts a provisional credit (Oracle), emits dispute.opened.
func scenarioCardDispute() *Trace {
	t := NewTrace()
	total := dur(250, 600)
	ref := txnRef()
	amt := amount(5, 600)
	M.RecordHTTP("api-gateway", "POST", "/api/v1/disputes", 201, ms(total))
	M.RecordDB("dispute-service", "oracle", "INSERT", ms(total)*0.2)
	M.RecordBiz("disputes.opened")

	fe := t.Add("mobile-bff", "POST /disputes", tracepb.Span_SPAN_KIND_CLIENT, nil, 0, total,
		kv("http.method", "POST", "http.url", "/disputes", "banking.txn_ref", ref, "peer.service", "api-gateway"), true, "")
	api := t.Add("api-gateway", "POST /api/v1/disputes", tracepb.Span_SPAN_KIND_SERVER, fe, 4*ms1, total-8*ms1,
		kv("http.method", "POST", "http.route", "/api/v1/disputes", "http.status_code", 201, "banking.txn_ref", ref), true, "")
	dp := t.Add("dispute-service", "DisputeService.Open", tracepb.Span_SPAN_KIND_SERVER, api, 10*ms1, total-22*ms1,
		kv("rpc.system", "grpc", "rpc.method", "Open", "peer.service", "dispute-service", "banking.txn_ref", ref), true, "")
	t.Add("dispute-service", "INSERT DISPUTE_CASES", tracepb.Span_SPAN_KIND_CLIENT, dp, 14*ms1, dur(6, 24),
		oraDB("COREBANK", "INSERT", "DISPUTE_CASES",
			"INSERT INTO DISPUTE_CASES(TXN_REF, AMOUNT, REASON, STATUS) VALUES(:1,:2,:3,'OPEN')", oracleCore), true, "")

	cdv := t.Add("card-service", "CardService.GetTransaction", tracepb.Span_SPAN_KIND_SERVER, dp, 62*ms1, dur(16, 52),
		kv("rpc.system", "grpc", "rpc.method", "GetTransaction", "banking.txn_ref", ref), true, "")
	t.Add("dispute-service", "card-service/GetTransaction", tracepb.Span_SPAN_KIND_CLIENT, dp, 60*ms1, dur(20, 60),
		kv("rpc.system", "grpc", "rpc.method", "GetTransaction", "peer.service", "card-service"), true, "")
	t.Add("card-service", "SELECT CARD_TXNS", tracepb.Span_SPAN_KIND_CLIENT, cdv, 66*ms1, dur(6, 22),
		oraDB("CARDS", "SELECT", "CARD_TXNS", "SELECT * FROM CARD_TXNS WHERE TXN_REF = :1", oracleDG), true, "")

	lg := t.Add("ledger-service", "LedgerService.ProvisionalCredit", tracepb.Span_SPAN_KIND_SERVER, dp, 132*ms1, dur(34, 110),
		kv("rpc.system", "grpc", "rpc.method", "ProvisionalCredit", "banking.amount", amt), true, "")
	t.Add("dispute-service", "ledger-service/ProvisionalCredit", tracepb.Span_SPAN_KIND_CLIENT, dp, 130*ms1, dur(40, 120),
		kv("rpc.system", "grpc", "rpc.method", "ProvisionalCredit", "peer.service", "ledger-service", "banking.amount", amt), true, "")
	t.Add("ledger-service", "BEGIN PKG_LEDGER.PROV_CREDIT", tracepb.Span_SPAN_KIND_CLIENT, lg, 136*ms1, dur(20, 80),
		oraDB("COREBANK", "BEGIN", "GL_POSTINGS", "BEGIN PKG_LEDGER.PROV_CREDIT(:1,:2); END;", oracleCore), true, "")
	kafkaPub(t, "dispute-service", dp, "dispute.opened", 200*ms1, ref)
	return t
}

// scenarioRewardsEvent: event-driven loyalty accrual. Roots at a Kafka
// consumer (card.approved); rewards-service credits points in Oracle +
// (sometimes) posts a cashback credit to the core ledger.
func scenarioRewardsEvent() *Trace {
	t := NewTrace()
	total := dur(80, 220)
	ref := txnRef()
	M.RecordBiz("kafka.events_consumed")
	M.RecordBiz("rewards.accrued")
	M.RecordDB("rewards-service", "oracle", "UPDATE", ms(total)*0.4)

	rw := t.Add("rewards-service", "kafka.consume card.approved", tracepb.Span_SPAN_KIND_CONSUMER, nil, 0, total,
		kv("messaging.system", "kafka", "messaging.destination", "card.approved",
			"messaging.operation", "receive", "peer.service", "kafka", "banking.txn_ref", ref), true, "")
	t.Link(rw, kafkaLinks.maybe("card.approved", t.traceID))
	t.Add("rewards-service", "UPDATE POINTS_LEDGER", tracepb.Span_SPAN_KIND_CLIENT, rw, 8*ms1, dur(6, 24),
		oraDB("REWARDS", "UPDATE", "POINTS_LEDGER",
			"UPDATE POINTS_LEDGER SET BALANCE = BALANCE + :1 WHERE CUST_ID = :2", oracleCore), true, "")
	if mrand.IntN(100) < 30 {
		t.Add("rewards-service", "ledger-service/CreditCashback", tracepb.Span_SPAN_KIND_CLIENT, rw, 40*ms1, dur(30, 90),
			kv("rpc.system", "grpc", "rpc.method", "CreditCashback", "peer.service", "ledger-service"), true, "")
		t.Add("ledger-service", "LedgerService.CreditCashback", tracepb.Span_SPAN_KIND_SERVER, rw, 42*ms1, dur(24, 80),
			kv("rpc.system", "grpc", "rpc.method", "CreditCashback"), true, "")
	}
	return t
}

// scenarioChargebackEvent: the back-office side of a dispute. Roots at a
// Kafka consumer (dispute.opened); chargeback-service raises the
// chargeback with the card scheme (external) + records it in Oracle.
func scenarioChargebackEvent() *Trace {
	t := NewTrace()
	total := dur(200, 520)
	ref := txnRef()
	scheme := pick("visa-network", "mastercard-network")
	M.RecordBiz("kafka.events_consumed")
	M.RecordBiz("chargebacks.raised")
	M.RecordDB("chargeback-service", "oracle", "INSERT", ms(total)*0.3)

	cb := t.Add("chargeback-service", "kafka.consume dispute.opened", tracepb.Span_SPAN_KIND_CONSUMER, nil, 0, total,
		kv("messaging.system", "kafka", "messaging.destination", "dispute.opened",
			"messaging.operation", "receive", "peer.service", "kafka", "banking.txn_ref", ref), true, "")
	t.Link(cb, kafkaLinks.maybe("dispute.opened", t.traceID))
	// Raise the chargeback with the scheme via the adapter.
	sav := t.Add("scheme-adapter", "SchemeAdapter.RaiseChargeback", tracepb.Span_SPAN_KIND_SERVER, cb, 12*ms1, dur(80, 260),
		kv("rpc.system", "grpc", "rpc.method", "RaiseChargeback", "card.scheme", scheme, "banking.txn_ref", ref), true, "")
	t.Add("chargeback-service", "scheme-adapter/RaiseChargeback", tracepb.Span_SPAN_KIND_CLIENT, cb, 10*ms1, dur(90, 280),
		kv("rpc.system", "grpc", "rpc.method", "RaiseChargeback", "peer.service", "scheme-adapter"), true, "")
	t.Add("scheme-adapter", "POST "+scheme+" /chargebacks", tracepb.Span_SPAN_KIND_CLIENT, sav, 16*ms1, dur(60, 220),
		ext(scheme, "POST", "https://api."+scheme+".example/v2/chargebacks", 201), true, "")
	t.Add("chargeback-service", "INSERT CHARGEBACKS", tracepb.Span_SPAN_KIND_CLIENT, cb, 120*ms1, dur(8, 26),
		oraDB("CARDS", "INSERT", "CHARGEBACKS",
			"INSERT INTO CHARGEBACKS(TXN_REF, SCHEME, STATUS) VALUES(:1,:2,'RAISED')", oracleCore), true, "")
	return t
}

// scenarioTreasuryReval: end-of-day FX position revaluation batch.
// treasury-service → position-service (Oracle) → market-data-service
// (external feed) → forex, then loads revalued P&L to the warehouse
// (Oracle) and publishes a risk.eod event.
func scenarioTreasuryReval() *Trace {
	t := NewTrace()
	total := dur(400, 1100)
	M.RecordBiz("treasury.revaluations")
	M.RecordDB("datawarehouse-etl", "oracle", "INSERT", ms(total)*0.3)

	tr := t.Add("treasury-service", "Treasury.RevaluePositions", tracepb.Span_SPAN_KIND_SERVER, nil, 0, total,
		kv("rpc.system", "grpc", "rpc.method", "RevaluePositions", "peer.service", "treasury-service",
			"batch.job", "EOD_FX_REVAL"), true, "")

	psv := t.Add("position-service", "PositionService.ListOpen", tracepb.Span_SPAN_KIND_SERVER, tr, 12*ms1, dur(54, 190),
		kv("rpc.system", "grpc", "rpc.method", "ListOpen"), true, "")
	t.Add("treasury-service", "position-service/ListOpen", tracepb.Span_SPAN_KIND_CLIENT, tr, 10*ms1, dur(60, 200),
		kv("rpc.system", "grpc", "rpc.method", "ListOpen", "peer.service", "position-service"), true, "")
	t.Add("position-service", "SELECT FX_POSITIONS", tracepb.Span_SPAN_KIND_CLIENT, psv, 16*ms1, dur(40, 150),
		oraDB("COREBANK", "SELECT", "FX_POSITIONS",
			"SELECT CCY_PAIR, NOTIONAL, BOOK FROM FX_POSITIONS WHERE STATUS='OPEN'", oracleDG), true, "")

	mdv := t.Add("market-data-service", "MarketData.GetRates", tracepb.Span_SPAN_KIND_SERVER, tr, 242*ms1, dur(34, 130),
		kv("rpc.system", "grpc", "rpc.method", "GetRates"), true, "")
	t.Add("treasury-service", "market-data-service/GetRates", tracepb.Span_SPAN_KIND_CLIENT, tr, 240*ms1, dur(40, 140),
		kv("rpc.system", "grpc", "rpc.method", "GetRates", "peer.service", "market-data-service"), true, "")
	t.Add("market-data-service", "GET refinitiv /rates", tracepb.Span_SPAN_KIND_CLIENT, mdv, 246*ms1, dur(20, 100),
		ext("market-data-feed", "GET", "https://elektron.refinitiv.example/v1/rates", 200), true, "")
	t.Add("treasury-service", "forex-service/GetRates", tracepb.Span_SPAN_KIND_CLIENT, tr, 360*ms1, dur(15, 50),
		kv("rpc.system", "grpc", "rpc.method", "GetRates", "peer.service", "forex-service"), true, "")

	etv := t.Add("datawarehouse-etl", "ETL.LoadPnL", tracepb.Span_SPAN_KIND_SERVER, tr, 442*ms1, dur(74, 250),
		kv("rpc.system", "grpc", "rpc.method", "LoadPnL"), true, "")
	t.Add("treasury-service", "datawarehouse-etl/LoadPnL", tracepb.Span_SPAN_KIND_CLIENT, tr, 440*ms1, dur(80, 260),
		kv("rpc.system", "grpc", "rpc.method", "LoadPnL", "peer.service", "datawarehouse-etl"), true, "")
	t.Add("datawarehouse-etl", "INSERT FX_PNL", tracepb.Span_SPAN_KIND_CLIENT, etv, 446*ms1, dur(60, 220),
		oraDB("DWH", "INSERT", "FX_PNL",
			"INSERT INTO FX_PNL(CCY_PAIR, NOTIONAL, MTM_PNL, AS_OF) SELECT :1,:2,:3,SYSDATE FROM DUAL", oracleCore), true, "")
	kafkaPub(t, "treasury-service", tr, "risk.eod", total-20*ms1, "EOD")
	return t
}
