package com.coremetry.demo.controller;

import com.coremetry.demo.exception.AccountFrozenException;
import com.coremetry.demo.exception.FraudBlockedException;
import com.coremetry.demo.exception.InsufficientFundsException;
import com.coremetry.demo.gateway.CoreBankingGateway;
import com.coremetry.demo.model.Account;
import com.coremetry.demo.model.Transaction;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.slf4j.MDC;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.client.RestTemplate;
import org.springframework.web.server.ResponseStatusException;

import java.util.Map;
import java.util.UUID;

/**
 * Money movement between two internal accounts. The flow, end to end:
 *
 *   1. load both accounts (Oracle CLIENT spans via the gateway)
 *   2. guard account state (FROZEN -> 409) and balance (-> 422)
 *   3. internal HTTP POST to /api/fraud/score (traced CLIENT->SERVER)
 *   4. on BLOCK -> 403; otherwise debit + credit + post the ledger entry
 *
 * Each business exception carries a cause chain so the recorded
 * stacktrace tells the full story; ORA-xxxxx infra faults bubble up from
 * the gateway as the root cause.
 */
@RestController
@RequestMapping("/api/transfers")
public class TransferController {
    private static final Logger log = LoggerFactory.getLogger(TransferController.class);

    private final CoreBankingGateway core;
    private final RestTemplate http;

    @Value("${demo.self-base-url:http://localhost:8080}")
    private String baseUrl;

    public TransferController(CoreBankingGateway core, RestTemplate http) {
        this.core = core;
        this.http = http;
    }

    @PostMapping
    public Transaction transfer(@RequestBody Map<String, Object> body) {
        String fromNo = String.valueOf(body.get("fromAccount"));
        String toNo = String.valueOf(body.get("toAccount"));
        double amount = ((Number) body.getOrDefault("amount", 0)).doubleValue();
        String ref = "TRF-" + UUID.randomUUID().toString().substring(0, 8).toUpperCase();

        MDC.put("account.id", fromNo);
        MDC.put("txn.id", ref);
        try {
            if (amount <= 0) {
                throw new ResponseStatusException(HttpStatus.BAD_REQUEST, "amount must be positive");
            }

            Account from = core.fetchAccount(fromNo).orElseThrow(() ->
                    new ResponseStatusException(HttpStatus.NOT_FOUND, "source account not found"));
            Account to = core.fetchAccount(toNo).orElseThrow(() ->
                    new ResponseStatusException(HttpStatus.NOT_FOUND, "destination account not found"));

            // 2a. account-state guard
            if (!"ACTIVE".equals(from.getStatus())) {
                AccountFrozenException ex = new AccountFrozenException(
                        "source account " + fromNo + " is " + from.getStatus());
                log.warn("transfer rejected ref={} reason=frozen status={}", ref, from.getStatus());
                recordDeclined(ref, fromNo, toNo, amount, from.getCurrency(),
                        "ACCOUNT_" + from.getStatus());
                throw ex;
            }

            // 2b. balance guard — cause chain shows the ledger shortfall
            if (from.getBalance() < amount) {
                IllegalStateException ledger = new IllegalStateException(
                        "ledger available=" + from.getBalance() + " requested=" + amount);
                InsufficientFundsException ex = new InsufficientFundsException(
                        "insufficient funds on " + fromNo, ledger);
                log.warn("transfer declined ref={} from={} amount={} balance={}",
                        ref, fromNo, amount, from.getBalance());
                recordDeclined(ref, fromNo, toNo, amount, from.getCurrency(), "INSUFFICIENT_FUNDS");
                throw ex;
            }

            // 3. internal fraud scoring (server-to-server HTTP)
            int fraudScore = callFraud(fromNo, amount, "TRANSFER");
            MDC.put("fraud.score", Integer.toString(fraudScore));
            if (fraudScore >= 80) {
                FraudBlockedException ex = new FraudBlockedException(
                        "transfer blocked by fraud engine (score=" + fraudScore + ")");
                log.warn("transfer blocked ref={} from={} score={}", ref, fromNo, fraudScore);
                recordDeclined(ref, fromNo, toNo, amount, from.getCurrency(),
                        "FRAUD_BLOCK_" + fraudScore);
                throw ex;
            }

            // 4. post the movement
            core.debit(from, amount);
            core.credit(to, amount);
            Transaction txn = new Transaction();
            txn.setReference(ref);
            txn.setFromAccount(fromNo);
            txn.setToAccount(toNo);
            txn.setAmount(amount);
            txn.setCurrency(from.getCurrency());
            txn.setType("TRANSFER");
            txn.setStatus("POSTED");
            txn.setReason("OK");
            Transaction saved = core.postLedger(txn);
            log.info("transfer posted ref={} from={} to={} amount={} {}",
                    ref, fromNo, toNo, amount, from.getCurrency());
            return saved;
        } finally {
            MDC.remove("account.id");
            MDC.remove("txn.id");
            MDC.remove("fraud.score");
        }
    }

    private int callFraud(String fromAccount, double amount, String channel) {
        @SuppressWarnings("unchecked")
        Map<String, Object> resp = http.postForObject(
                baseUrl + "/api/fraud/score",
                Map.of("fromAccount", fromAccount, "amount", amount, "channel", channel),
                Map.class);
        if (resp == null || resp.get("score") == null) return 0;
        return ((Number) resp.get("score")).intValue();
    }

    /** Persist a DECLINED ledger row for audit/statement visibility. */
    private void recordDeclined(String ref, String from, String to, double amount,
                                String ccy, String reason) {
        try {
            Transaction t = new Transaction();
            t.setReference(ref);
            t.setFromAccount(from);
            t.setToAccount(to);
            t.setAmount(amount);
            t.setCurrency(ccy);
            t.setType("TRANSFER");
            t.setStatus("DECLINED");
            t.setReason(reason);
            core.postLedger(t);
        } catch (RuntimeException e) {
            // Don't mask the original business exception with a ledger write failure.
            log.error("failed to record declined transfer ref={}", ref, e);
        }
    }
}
