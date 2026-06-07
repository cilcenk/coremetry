package com.coremetry.demo.controller;

import com.coremetry.demo.exception.AccountFrozenException;
import com.coremetry.demo.exception.FraudBlockedException;
import com.coremetry.demo.exception.InsufficientFundsException;
import com.coremetry.demo.gateway.CoreBankingGateway;
import com.coremetry.demo.model.Account;
import com.coremetry.demo.model.Card;
import com.coremetry.demo.model.Transaction;
import com.coremetry.demo.repository.CardRepository;
import com.coremetry.demo.repository.PayeeRepository;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.slf4j.MDC;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.client.RestTemplate;
import org.springframework.web.server.ResponseStatusException;

import java.util.List;
import java.util.Map;
import java.util.UUID;

/**
 * Card and bill payments. Both debit a funding account and post a ledger
 * entry; card payments additionally validate the card state and run a
 * higher-weighted fraud score (card-not-present risk).
 */
@RestController
@RequestMapping("/api/payments")
public class PaymentController {
    private static final Logger log = LoggerFactory.getLogger(PaymentController.class);

    private final CoreBankingGateway core;
    private final CardRepository cards;
    private final PayeeRepository payees;
    private final RestTemplate http;
    private final com.coremetry.demo.service.DemoMetrics metrics;

    @Value("${demo.self-base-url:http://localhost:8080}")
    private String baseUrl;

    public PaymentController(CoreBankingGateway core, CardRepository cards,
                             PayeeRepository payees, RestTemplate http,
                             com.coremetry.demo.service.DemoMetrics metrics) {
        this.core = core;
        this.cards = cards;
        this.payees = payees;
        this.http = http;
        this.metrics = metrics;
    }

    /** Card purchase — POST /api/payments/card. */
    @PostMapping("/card")
    public Transaction card(@RequestBody Map<String, Object> body) {
        String accountNo = String.valueOf(body.get("accountNo"));
        double amount = ((Number) body.getOrDefault("amount", 0)).doubleValue();
        String merchant = String.valueOf(body.getOrDefault("merchant", "UNKNOWN"));
        String ref = "CRD-" + UUID.randomUUID().toString().substring(0, 8).toUpperCase();

        MDC.put("account.id", accountNo);
        MDC.put("txn.id", ref);
        try {
            Account acct = core.fetchAccount(accountNo).orElseThrow(() ->
                    new ResponseStatusException(HttpStatus.NOT_FOUND, "account not found"));

            // card-state guard
            List<Card> bound = cards.findByAccountNo(accountNo);
            Card card = bound.stream().filter(c -> "ACTIVE".equals(c.getStatus()))
                    .findFirst().orElse(null);
            if (card == null) {
                AccountFrozenException ex = new AccountFrozenException(
                        "no active card on account " + accountNo);
                log.warn("card payment rejected ref={} reason=no_active_card", ref);
                metrics.cardDeclined();
                throw ex;
            }
            if (!"ACTIVE".equals(acct.getStatus())) {
                metrics.cardDeclined();
                throw new AccountFrozenException(
                        "account " + accountNo + " is " + acct.getStatus());
            }
            if (acct.getBalance() < amount) {
                InsufficientFundsException ex = new InsufficientFundsException(
                        "insufficient funds on " + accountNo,
                        new IllegalStateException(
                                "available=" + acct.getBalance() + " requested=" + amount));
                log.warn("card payment declined ref={} amount={} balance={}",
                        ref, amount, acct.getBalance());
                metrics.cardDeclined();
                throw ex;
            }

            int fraudScore = callFraud(accountNo, amount, "CARD_PAYMENT");
            MDC.put("fraud.score", Integer.toString(fraudScore));
            if (fraudScore >= 80) {
                FraudBlockedException ex = new FraudBlockedException(
                        "card payment blocked by fraud engine (score=" + fraudScore + ")");
                log.warn("card payment blocked ref={} card=****{} score={}",
                        ref, card.getLast4(), fraudScore);
                metrics.cardDeclined();
                metrics.fraudBlock("CARD_PAYMENT");
                throw ex;
            }

            core.debit(acct, amount);
            Transaction txn = new Transaction();
            txn.setReference(ref);
            txn.setFromAccount(accountNo);
            txn.setToAccount(merchant);
            txn.setAmount(amount);
            txn.setCurrency(acct.getCurrency());
            txn.setType("CARD_PAYMENT");
            txn.setStatus("POSTED");
            txn.setReason("OK");
            Transaction saved = core.postLedger(txn);
            metrics.cardApproved();
            log.info("card payment posted ref={} card=****{} {} merchant={} amount={}",
                    ref, card.getLast4(), card.getNetwork(), merchant, amount);
            return saved;
        } finally {
            MDC.remove("account.id");
            MDC.remove("txn.id");
            MDC.remove("fraud.score");
        }
    }

    /** Bill payment to a registered payee — POST /api/payments/bill. */
    @PostMapping("/bill")
    public Transaction bill(@RequestBody Map<String, Object> body) {
        String accountNo = String.valueOf(body.get("accountNo"));
        double amount = ((Number) body.getOrDefault("amount", 0)).doubleValue();
        String payeeName = String.valueOf(body.getOrDefault("payee", "BILLER"));
        String ref = "BIL-" + UUID.randomUUID().toString().substring(0, 8).toUpperCase();

        MDC.put("account.id", accountNo);
        MDC.put("txn.id", ref);
        try {
            Account acct = core.fetchAccount(accountNo).orElseThrow(() ->
                    new ResponseStatusException(HttpStatus.NOT_FOUND, "account not found"));
            if (!"ACTIVE".equals(acct.getStatus())) {
                throw new AccountFrozenException(
                        "account " + accountNo + " is " + acct.getStatus());
            }
            if (acct.getBalance() < amount) {
                throw new InsufficientFundsException(
                        "insufficient funds on " + accountNo,
                        new IllegalStateException(
                                "available=" + acct.getBalance() + " requested=" + amount));
            }

            int fraudScore = callFraud(accountNo, amount, "BILL_PAYMENT");
            MDC.put("fraud.score", Integer.toString(fraudScore));
            if (fraudScore >= 80) {
                metrics.fraudBlock("BILL_PAYMENT");
                throw new FraudBlockedException(
                        "bill payment blocked by fraud engine (score=" + fraudScore + ")");
            }

            core.debit(acct, amount);
            Transaction txn = new Transaction();
            txn.setReference(ref);
            txn.setFromAccount(accountNo);
            txn.setToAccount(payeeName);
            txn.setAmount(amount);
            txn.setCurrency(acct.getCurrency());
            txn.setType("BILL_PAYMENT");
            txn.setStatus("POSTED");
            txn.setReason("OK");
            Transaction saved = core.postLedger(txn);
            metrics.billPaid();
            log.info("bill payment posted ref={} account={} payee={} amount={}",
                    ref, accountNo, payeeName, amount);
            return saved;
        } finally {
            MDC.remove("account.id");
            MDC.remove("txn.id");
            MDC.remove("fraud.score");
        }
    }

    private int callFraud(String accountNo, double amount, String channel) {
        @SuppressWarnings("unchecked")
        Map<String, Object> resp = http.postForObject(
                baseUrl + "/api/fraud/score",
                Map.of("fromAccount", accountNo, "amount", amount, "channel", channel),
                Map.class);
        if (resp == null || resp.get("score") == null) return 0;
        return ((Number) resp.get("score")).intValue();
    }
}
