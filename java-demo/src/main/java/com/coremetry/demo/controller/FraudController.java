package com.coremetry.demo.controller;

import com.coremetry.demo.exception.CoreBankingException;
import com.coremetry.demo.service.DemoLoad;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.slf4j.MDC;
import org.springframework.web.bind.annotation.*;

import java.util.Map;
import java.util.concurrent.ThreadLocalRandom;

/**
 * Internal fraud-scoring service. Called server-to-server by the transfer
 * and payment flows (the OTel javaagent traces the outgoing RestTemplate
 * call as a CLIENT span and this handler as the linked SERVER span). Not
 * meant to be hit by an external client — hence /api/fraud/score is
 * "internal" by convention.
 *
 * Returns a score in [0,100]. >= 80 means BLOCK. The caller turns a BLOCK
 * into a FraudBlockedException (HTTP 403).
 */
@RestController
@RequestMapping("/api/fraud")
public class FraudController {
    private static final Logger log = LoggerFactory.getLogger(FraudController.class);

    private final DemoLoad load;

    public FraudController(DemoLoad load) {
        this.load = load;
    }

    @PostMapping("/score")
    public Map<String, Object> score(@RequestBody Map<String, Object> body)
            throws InterruptedException {
        String fromAccount = String.valueOf(body.getOrDefault("fromAccount", ""));
        double amount = body.get("amount") == null
                ? 0.0 : ((Number) body.get("amount")).doubleValue();
        String channel = String.valueOf(body.getOrDefault("channel", "TRANSFER"));

        // Right-skewed model-scoring latency (~15ms median), stretched under
        // load so the fraud hop slows with everything else during an incident.
        Thread.sleep(load.sampleMs(15, 45));

        // Heuristic score: large amounts + card channel skew higher.
        int score = baseScore(amount, channel);
        MDC.put("fraud.score", Integer.toString(score));
        try {
            // ~3% the rules engine times out talking to its Oracle feature
            // store — more often during a degradation incident.
            if (load.roll(3)) {
                CoreBankingException ex = new CoreBankingException("ORA-12170",
                        "TNS:Connect timeout reading fraud feature store");
                log.error("fraud feature-store unavailable from={} amount={}",
                        fromAccount, amount, ex);
                // Fail open with an elevated score so the caller can decide.
                score = Math.max(score, 75);
            }
            boolean block = score >= 80;
            log.info("fraud score from={} amount={} channel={} score={} decision={}",
                    fromAccount, amount, channel, score, block ? "BLOCK" : "ALLOW");
            return Map.of(
                    "fromAccount", fromAccount,
                    "amount", amount,
                    "channel", channel,
                    "score", score,
                    "decision", block ? "BLOCK" : "ALLOW");
        } finally {
            MDC.remove("fraud.score");
        }
    }

    private int baseScore(double amount, String channel) {
        int s = 5;
        if (amount > 5_000) s += 40;
        else if (amount > 1_000) s += 20;
        else if (amount > 250) s += 8;
        if ("CARD_PAYMENT".equals(channel)) s += 15;
        // Random jitter so a small slice crosses the 80 BLOCK line.
        s += ThreadLocalRandom.current().nextInt(0, 45);
        return Math.min(s, 100);
    }
}
