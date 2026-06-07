package com.coremetry.demo.gateway;

import com.coremetry.demo.exception.CoreBankingException;
import com.coremetry.demo.model.Account;
import com.coremetry.demo.model.Transaction;
import com.coremetry.demo.repository.AccountRepository;
import com.coremetry.demo.repository.TransactionRepository;
import com.coremetry.demo.service.DemoLoad;

import io.opentelemetry.api.GlobalOpenTelemetry;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.StatusCode;
import io.opentelemetry.api.trace.Tracer;
import io.opentelemetry.context.Scope;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Component;

import java.util.List;
import java.util.Optional;
import java.util.concurrent.ThreadLocalRandom;
import java.util.function.Supplier;

/**
 * SIMULATED ORACLE CORE-BANKING TELEMETRY.
 *
 * The real persistence is plain H2/JPA underneath. But to an operator
 * staring at the trace waterfall, every call here looks like a round-trip
 * to a production Oracle "COREBANK" instance: each method wraps its work
 * in a MANUAL OpenTelemetry CLIENT span carrying the OTel database
 * semantic-convention attributes —
 *
 *   db.system      = oracle
 *   db.name        = COREBANK
 *   db.statement   = <realistic Oracle SQL / PL-SQL>
 *   db.operation   = SELECT | UPDATE | INSERT | CALL
 *   db.sql.table   = ACCOUNTS | LEDGER_ENTRIES | ...
 *   server.address = corebank-scan.prod.bank.internal:1521
 *
 * We deliberately disable the javaagent's JDBC auto-instrumentation
 * (OTEL_INSTRUMENTATION_JDBC_ENABLED=false, set in scripts/start.sh) so
 * the real H2 statements don't shadow these synthetic Oracle spans.
 *
 * Roughly 2% of calls inject an ORA-00060 deadlock and 1% an ORA-12170
 * TNS timeout — recorded on the span via span.recordException so the
 * operator sees authentic Oracle infra failures in the trace.
 *
 * The OTel API surface used here (Tracer / Span / Attributes) comes from
 * the opentelemetry-api dependency in pom.xml; the actual SDK / exporter
 * implementation is supplied at runtime by the -javaagent, so
 * GlobalOpenTelemetry resolves to the agent's live TracerProvider.
 */
@Component
public class CoreBankingGateway {
    private static final Logger log = LoggerFactory.getLogger(CoreBankingGateway.class);

    private static final String DB_SYSTEM   = "oracle";
    private static final String DB_NAME     = "COREBANK";
    private static final String CB_HOST     = "corebank-scan.prod.bank.internal";
    private static final int    CB_PORT     = 1521;
    private static final String PEER        = "corebank";

    // OTel db.* semantic-convention attribute keys.
    private static final AttributeKey<String> DB_SYSTEM_KEY    = AttributeKey.stringKey("db.system");
    private static final AttributeKey<String> DB_NAME_KEY      = AttributeKey.stringKey("db.name");
    private static final AttributeKey<String> DB_STATEMENT_KEY = AttributeKey.stringKey("db.statement");
    private static final AttributeKey<String> DB_OPERATION_KEY = AttributeKey.stringKey("db.operation");
    private static final AttributeKey<String> DB_TABLE_KEY     = AttributeKey.stringKey("db.sql.table");
    private static final AttributeKey<String> SERVER_ADDR_KEY  = AttributeKey.stringKey("server.address");
    private static final AttributeKey<Long>   SERVER_PORT_KEY  = AttributeKey.longKey("server.port");
    private static final AttributeKey<String> PEER_SERVICE_KEY = AttributeKey.stringKey("peer.service");

    private final AccountRepository accounts;
    private final TransactionRepository transactions;
    private final DemoLoad load;
    private final Tracer tracer;

    public CoreBankingGateway(AccountRepository accounts, TransactionRepository transactions,
                              DemoLoad load) {
        this.accounts = accounts;
        this.transactions = transactions;
        this.load = load;
        // Resolves to the javaagent-installed TracerProvider at runtime.
        this.tracer = GlobalOpenTelemetry.getTracer("com.coremetry.demo.corebank", "1.0.0");
    }

    // ── public "core banking" operations ──────────────────────────────────

    /** SELECT the account row by its external account number. */
    public Optional<Account> fetchAccount(String accountNo) {
        String sql = "SELECT acct_id, acct_no, customer_name, acct_type, balance, "
                + "currency, status FROM ACCOUNTS WHERE acct_no = :1";
        return inOracleSpan("SELECT", "ACCOUNTS", sql,
                () -> accounts.findByAccountNo(accountNo));
    }

    /** Apply a debit to the source account's balance (UPDATE on ACCOUNTS). */
    public void debit(Account account, double amount) {
        String sql = "UPDATE ACCOUNTS SET balance = balance - :1, "
                + "last_movement_ts = SYSTIMESTAMP WHERE acct_no = :2";
        inOracleSpan("UPDATE", "ACCOUNTS", sql, () -> {
            account.setBalance(account.getBalance() - amount);
            accounts.save(account);
            return null;
        });
    }

    /** Apply a credit to the destination account's balance. */
    public void credit(Account account, double amount) {
        String sql = "UPDATE ACCOUNTS SET balance = balance + :1, "
                + "last_movement_ts = SYSTIMESTAMP WHERE acct_no = :2";
        inOracleSpan("UPDATE", "ACCOUNTS", sql, () -> {
            account.setBalance(account.getBalance() + amount);
            accounts.save(account);
            return null;
        });
    }

    /**
     * Post a ledger entry. Modelled as a PL-SQL stored-procedure CALL —
     * the kind of double-entry posting proc a real core-banking platform
     * exposes.
     */
    public Transaction postLedger(Transaction txn) {
        String sql = "BEGIN PKG_LEDGER.POST_ENTRY("
                + "p_ref => :1, p_from => :2, p_to => :3, "
                + "p_amount => :4, p_ccy => :5, p_type => :6); END;";
        return inOracleSpan("CALL", "LEDGER_ENTRIES", sql,
                () -> transactions.save(txn));
    }

    /** Read the recent ledger movements for an account (statement). */
    public List<Transaction> recentEntries(String accountNo, int limit) {
        String sql = "SELECT * FROM ( "
                + "SELECT ref, from_acct, to_acct, amount, currency, txn_type, "
                + "status, reason, created_ts FROM LEDGER_ENTRIES "
                + "WHERE from_acct = :1 OR to_acct = :1 ORDER BY created_ts DESC "
                + ") WHERE ROWNUM <= :2";
        return inOracleSpan("SELECT", "LEDGER_ENTRIES", sql,
                () -> transactions.findByFromAccountOrToAccountOrderByCreatedAtDesc(
                        accountNo, accountNo,
                        org.springframework.data.domain.PageRequest.of(0, limit)));
    }

    // ── span machinery ────────────────────────────────────────────────────

    /**
     * Wraps {@code work} in a CLIENT span tagged with the Oracle db.*
     * semantic-convention attributes. Adds a little latency to look like a
     * network round-trip, and injects rare ORA-xxxxx infra failures that
     * are recorded on the span (and then thrown as a CoreBankingException).
     */
    private <T> T inOracleSpan(String dbOperation, String table,
                               String statement, Supplier<T> work) {
        // Span name follows the OTel db convention: "<operation> <db>.<table>".
        String spanName = dbOperation + " " + DB_NAME + "." + table;
        Span span = tracer.spanBuilder(spanName)
                .setSpanKind(SpanKind.CLIENT)
                .setAttribute(DB_SYSTEM_KEY, DB_SYSTEM)
                .setAttribute(DB_NAME_KEY, DB_NAME)
                .setAttribute(DB_STATEMENT_KEY, statement)
                .setAttribute(DB_OPERATION_KEY, dbOperation)
                .setAttribute(DB_TABLE_KEY, table)
                .setAttribute(SERVER_ADDR_KEY, CB_HOST)
                .setAttribute(SERVER_PORT_KEY, (long) CB_PORT)
                .setAttribute(PEER_SERVICE_KEY, PEER)
                .startSpan();
        try (Scope scope = span.makeCurrent()) {
            simulateRoundTripLatency();
            maybeInjectOracleFault(span, table);
            return work.get();
        } catch (CoreBankingException e) {
            // Already recorded on the span by maybeInjectOracleFault.
            throw e;
        } catch (RuntimeException e) {
            span.recordException(e);
            span.setStatus(StatusCode.ERROR, e.getMessage());
            throw e;
        } finally {
            span.end();
        }
    }

    private void simulateRoundTripLatency() {
        // Right-skewed (log-normal) round-trip — a warm Oracle on the same DC
        // LAN sits around ~6ms with a tail to ~40ms, and the whole curve
        // stretches under load / during a contention incident.
        load.sleepLogNormal(6, 22);
    }

    /**
     * ~2% ORA-00060 (deadlock), ~1% ORA-12170 (TNS connect timeout).
     * Recorded on the span via recordException + ERROR status, then thrown
     * so it lands at the bottom of the caller's cause chain.
     */
    private void maybeInjectOracleFault(Span span, String table) {
        int roll = ThreadLocalRandom.current().nextInt(1000);
        // Base ~2% deadlock / ~1% TNS timeout, lifted during an incident so
        // Oracle faults cluster with the latency rise instead of trickling.
        int bump = (int) (load.errorBump() * 400); // up to ~+100 per-mille
        if (roll < 20 + bump) {
            CoreBankingException ex = new CoreBankingException("ORA-00060",
                    "deadlock detected while waiting for resource on " + table);
            span.recordException(ex);
            span.setStatus(StatusCode.ERROR, "ORA-00060 deadlock");
            log.error("core-banking deadlock table={} host={}", table, CB_HOST, ex);
            throw ex;
        } else if (roll < 30 + bump) {
            CoreBankingException ex = new CoreBankingException("ORA-12170",
                    "TNS:Connect timeout occurred talking to " + CB_HOST + ":" + CB_PORT);
            span.recordException(ex);
            span.setStatus(StatusCode.ERROR, "ORA-12170 TNS timeout");
            log.error("core-banking TNS timeout host={}:{}", CB_HOST, CB_PORT, ex);
            throw ex;
        }
    }
}
