package com.coremetry.demo.service;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;
import org.springframework.web.client.RestClientException;
import org.springframework.web.client.RestTemplate;

import java.util.Map;
import java.util.concurrent.ThreadLocalRandom;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Drives synthetic retail-banking traffic against our own HTTP endpoints
 * so the OpenTelemetry javaagent has a continuous stream to instrument.
 * No telemetry code lives here — every span / log / metric is auto-emitted
 * by the agent; the simulated Oracle spans come from CoreBankingGateway.
 *
 * Traffic mix mirrors a real retail bank: balance inquiries dominate,
 * then transfers, then card payments, then statements, then bill pay.
 * A slice of every category deliberately hits an error path (frozen
 * account, overdraft, fraud block, unknown account) so the trace store
 * always carries error exemplars.
 */
@Component
public class LoadGenerator {
    private static final Logger log = LoggerFactory.getLogger(LoadGenerator.class);

    private final RestTemplate http;
    private final AtomicLong total = new AtomicLong();

    @Value("${demo.self-base-url:http://localhost:8080}")
    private String baseUrl;

    public LoadGenerator(RestTemplate http) { this.http = http; }

    @Scheduled(fixedDelayString = "${demo.scenario-delay-ms:300}")
    public void runScenario() {
        int pick = ThreadLocalRandom.current().nextInt(100);
        try {
            if (pick < 50)        balanceInquiry();   // 50% — most common
            else if (pick < 72)   transfer();         // 22%
            else if (pick < 86)   cardPayment();       // 14%
            else if (pick < 95)   statement();         // 9%
            else                  billPayment();       // 5%
            total.incrementAndGet();
        } catch (RestClientException e) {
            // Expected — controllers throw 4xx/5xx on the error paths.
        }
    }

    @Scheduled(fixedDelay = 30_000L)
    public void heartbeat() {
        log.info("LoadGenerator heartbeat: {} banking scenarios driven", total.get());
    }

    // ── scenarios ────────────────────────────────────────────────────────

    private void balanceInquiry() {
        String acct = pickAccount(/*includeBad=*/true);
        try {
            http.getForObject(baseUrl + "/api/accounts/" + acct, Object.class);
        } catch (RestClientException ignored) { /* 404 on unknown account */ }
    }

    private void statement() {
        String acct = pickAccount(false);
        int limit = ThreadLocalRandom.current().nextInt(5, 20);
        try {
            http.getForObject(baseUrl + "/api/accounts/" + acct + "/statement?limit=" + limit,
                    Object.class);
        } catch (RestClientException ignored) {}
    }

    private void transfer() {
        String from = pickAccount(false);
        String to = pickAccount(false);
        // Occasionally aim at the frozen account to exercise the 409 path.
        if (ThreadLocalRandom.current().nextInt(100) < 8) from = "TR330000000000099";
        // Amounts skew small; ~10% are large enough to risk overdraft / fraud.
        double amount = ThreadLocalRandom.current().nextInt(100) < 10
                ? ThreadLocalRandom.current().nextInt(4_000, 12_000)
                : ThreadLocalRandom.current().nextInt(10, 800);
        post("/api/transfers", Map.of(
                "fromAccount", from, "toAccount", to, "amount", amount));
    }

    private void cardPayment() {
        String acct = pickAccount(false);
        double amount = ThreadLocalRandom.current().nextInt(100) < 12
                ? ThreadLocalRandom.current().nextInt(3_000, 9_000)
                : ThreadLocalRandom.current().nextInt(5, 400);
        post("/api/payments/card", Map.of(
                "accountNo", acct,
                "amount", amount,
                "merchant", pick("AMZN-MKTP", "STARBUCKS", "SHELL-FUEL", "STEAM", "UBER")));
    }

    private void billPayment() {
        String acct = pickAccount(false);
        double amount = ThreadLocalRandom.current().nextInt(20, 600);
        post("/api/payments/bill", Map.of(
                "accountNo", acct,
                "amount", amount,
                "payee", pick("CITY-POWER", "ACME-TELECOM", "WATERWORKS", "LANDLORD-LLC")));
    }

    private void post(String path, Map<String, Object> body) {
        try {
            http.postForObject(baseUrl + path, body, Object.class);
        } catch (RestClientException ignored) {
            // 403 fraud / 409 frozen / 422 overdraft / 5xx ORA faults — all expected.
        }
    }

    // ── helpers ──────────────────────────────────────────────────────────

    /** Seeded account numbers are TR33 + a 12-digit zero-padded id (1..20). */
    private String pickAccount(boolean includeBad) {
        if (includeBad && ThreadLocalRandom.current().nextInt(100) < 4) {
            return "TR330000000000999"; // unknown -> 404
        }
        int n = ThreadLocalRandom.current().nextInt(1, 21);
        return String.format("TR33%012d", n);
    }

    private static String pick(String... opts) {
        return opts[ThreadLocalRandom.current().nextInt(opts.length)];
    }
}
