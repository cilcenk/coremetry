package com.coremetry.demo.service;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;

import java.time.LocalTime;
import java.util.concurrent.ThreadLocalRandom;

/**
 * Shared traffic-shape + saturation model for the Java demo — the Spring
 * counterpart of the Go demo's load model.
 *
 * Real production traffic is not a flat line driven at a fixed delay with
 * fixed per-call error odds and uniform latencies. It breathes with the
 * business day, has organic micro-spikes, and every so often something
 * degrades for a few minutes so that latency AND error rate rise together
 * and then recover. This bean is the single source of that shape; the
 * load generator, the simulated Oracle gateway, the fraud scorer and the
 * custom metrics all read from it so the auto-instrumented traces, the
 * JVM/HTTP metrics, and the business metrics tell one coherent story.
 *
 * Three live factors:
 *   rateFactor()     scales how many scenarios the generator fires;
 *   latencyFactor()  stretches every simulated latency (saturation);
 *   errorBump()      extra failure probability folded into roll().
 */
@Component
public class DemoLoad {
    private static final Logger log = LoggerFactory.getLogger(DemoLoad.class);

    private volatile double rateFactor = 1.0;
    private volatile double latencyFactor = 1.0;
    private volatile double errorBump = 0.0;
    private volatile String incidentLabel = "";

    private long incidentUntilMs = 0L;
    private double incidentLatency = 1.0;
    private double incidentErr = 0.0;

    /** Recompute the diurnal rate and start/stop incidents every few seconds. */
    @Scheduled(fixedDelay = 3_000L)
    public void recompute() {
        ThreadLocalRandom r = ThreadLocalRandom.current();
        double rate = diurnal(LocalTime.now()) * (0.92 + r.nextDouble() * 0.16);
        double lat = 1.0;
        double err = 0.0;

        long now = System.currentTimeMillis();
        if (!incidentLabel.isEmpty() && now > incidentUntilMs) {
            log.info("incident cleared: {}", incidentLabel);
            incidentLabel = "";
        }
        if (incidentLabel.isEmpty() && r.nextDouble() < 0.03) {
            startIncident(now, r);
        }
        if (!incidentLabel.isEmpty()) {
            lat = incidentLatency;
            err = incidentErr;
            rate *= 1.15;
        }
        // Organic micro-spike even without a full incident.
        if (r.nextDouble() < 0.05) {
            rate *= 1.4 + r.nextDouble() * 0.8;
            lat *= 1.2;
        }

        rateFactor = rate;
        latencyFactor = lat;
        errorBump = err;
    }

    private void startIncident(long now, ThreadLocalRandom r) {
        String[] labels = {
            "oracle-row-lock-contention",
            "jvm-gc-pause-storm",
            "downstream-dependency-degraded",
            "noisy-neighbor-cpu-steal"
        };
        double[] lats = {2.4, 3.2, 1.8, 4.0};
        double[] errs = {0.10, 0.04, 0.18, 0.02};
        int k = r.nextInt(labels.length);
        incidentLabel = labels[k];
        incidentLatency = lats[k];
        incidentErr = errs[k];
        incidentUntilMs = now + (60 + r.nextInt(180)) * 1000L;
        log.warn("incident started: {} (latency x{}, +{} err) for ~{}s",
                incidentLabel, incidentLatency, incidentErr, (incidentUntilMs - now) / 1000);
    }

    /**
     * 0.28..1.0 business-day curve: overnight trough, ~10:00 main peak,
     * ~19:00 evening bump. Raised-cosine humps over the minute of day.
     */
    private static double diurnal(LocalTime t) {
        double minute = t.getHour() * 60 + t.getMinute();
        double v = 0.28 + hump(minute, 10 * 60, 6 * 60, 0.72)
                + hump(minute, 19 * 60, 3.5 * 60, 0.35);
        return Math.min(v, 1.0);
    }

    private static double hump(double x, double center, double width, double amp) {
        double d = Math.abs(x - center);
        if (d > width) return 0.0;
        return amp * 0.5 * (1 + Math.cos(Math.PI * d / width));
    }

    // ── hot-path readers ─────────────────────────────────────────────────

    public double rateFactor()    { return rateFactor; }
    public double latencyFactor() { return latencyFactor; }
    public double errorBump()     { return errorBump; }
    public String incidentLabel() { return incidentLabel; }

    /** How many scenarios to fire on this generator tick (Poisson-ish). */
    public int burstCount() {
        double expected = rateFactor;
        int n = (int) Math.floor(expected);
        if (ThreadLocalRandom.current().nextDouble() < (expected - n)) n++;
        return n;
    }

    /** roll returns true with probability basePct% plus the current error bump. */
    public boolean roll(int basePct) {
        double p = basePct / 100.0 + errorBump;
        if (p > 0.95) p = 0.95;
        return ThreadLocalRandom.current().nextDouble() < p;
    }

    /**
     * Sleep a realistic, right-skewed (log-normal) latency derived from
     * [medianMs hint, maxMs], stretched by the live latency factor so a
     * saturation incident slows every simulated round-trip at once.
     */
    public void sleepLogNormal(int medianMs, int maxMs) {
        try {
            Thread.sleep(sampleMs(medianMs, maxMs));
        } catch (InterruptedException ignored) {
            Thread.currentThread().interrupt();
        }
    }

    /** Log-normal latency sample in ms (no sleep) for callers that need the value. */
    public long sampleMs(int medianMs, int maxMs) {
        ThreadLocalRandom r = ThreadLocalRandom.current();
        double u1 = Math.max(r.nextDouble(), 1e-9);
        double u2 = r.nextDouble();
        double z = Math.sqrt(-2 * Math.log(u1)) * Math.cos(2 * Math.PI * u2);
        double base = medianMs * Math.exp(0.55 * z);
        double v = Math.min(base, maxMs * 2.5) * latencyFactor;
        return (long) Math.max(1, v);
    }
}
