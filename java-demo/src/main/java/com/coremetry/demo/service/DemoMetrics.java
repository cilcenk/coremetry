package com.coremetry.demo.service;

import io.opentelemetry.api.GlobalOpenTelemetry;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.metrics.LongCounter;
import io.opentelemetry.api.metrics.Meter;

import org.springframework.beans.factory.InitializingBean;
import org.springframework.stereotype.Component;

import java.util.concurrent.ThreadLocalRandom;

/**
 * Custom OpenTelemetry metrics for the Java demo.
 *
 * The javaagent already auto-emits HTTP server + JVM (heap, GC, threads)
 * metrics for free. What it CAN'T know is the bank's domain and the
 * service's saturation state, so this bean adds:
 *
 *   • business counters — transfers / card / bill outcomes, fraud blocks —
 *     incremented from the controllers, so the operator dashboards graph
 *     real banking throughput and decline rates;
 *
 *   • saturation gauges — connection-pool usage/max/utilization, cache
 *     hit-ratio, in-flight worker count, Kafka-style consumer lag — driven
 *     by DemoLoad, so at the morning peak or during an incident they climb
 *     together (cache hit-ratio dips), matching the latency + error-rate
 *     rise the auto-instrumented metrics already show.
 *
 * The Meter resolves to the agent's MeterProvider via GlobalOpenTelemetry,
 * exactly like CoreBankingGateway's Tracer, so these points export over the
 * same OTLP pipeline as everything else.
 */
@Component
public class DemoMetrics implements InitializingBean {

    private final DemoLoad load;

    private LongCounter transfersCompleted;
    private LongCounter transfersFailed;
    private LongCounter cardApproved;
    private LongCounter cardDeclined;
    private LongCounter billsPaid;
    private LongCounter fraudBlocked;

    public DemoMetrics(DemoLoad load) {
        this.load = load;
    }

    @Override
    public void afterPropertiesSet() {
        Meter meter = GlobalOpenTelemetry.getMeter("com.coremetry.demo.metrics");

        transfersCompleted = meter.counterBuilder("demo.transfers.completed")
                .setDescription("Money transfers posted").setUnit("{transfer}").build();
        transfersFailed = meter.counterBuilder("demo.transfers.failed")
                .setDescription("Money transfers declined").setUnit("{transfer}").build();
        cardApproved = meter.counterBuilder("demo.card.approved")
                .setDescription("Card payments approved").setUnit("{payment}").build();
        cardDeclined = meter.counterBuilder("demo.card.declined")
                .setDescription("Card payments declined").setUnit("{payment}").build();
        billsPaid = meter.counterBuilder("demo.bills.paid")
                .setDescription("Bill payments posted").setUnit("{payment}").build();
        fraudBlocked = meter.counterBuilder("demo.fraud.blocked")
                .setDescription("Transactions blocked by the fraud engine").setUnit("{decision}").build();

        // ── saturation gauges driven by the load model ──────────────────────
        // Namespaced under demo.* so they never collide with metrics the
        // javaagent emits under the same OTel semantic-convention names
        // (e.g. HikariCP's db.client.connections.usage).
        final int poolMax = 50;

        meter.gaugeBuilder("demo.db.pool.max").setUnit("{connection}")
                .setDescription("Max connection-pool size")
                .buildWithCallback(m -> m.record(poolMax));

        meter.gaugeBuilder("demo.db.pool.usage").setUnit("{connection}")
                .setDescription("Connections in use from the pool")
                .buildWithCallback(m -> m.record(poolUsage(poolMax)));

        meter.gaugeBuilder("demo.db.pool.utilization").setUnit("1")
                .setDescription("Pool utilization (usage/max)")
                .buildWithCallback(m -> m.record(poolUsage(poolMax) / (double) poolMax));

        meter.gaugeBuilder("demo.cache.hit_ratio").setUnit("1")
                .setDescription("Fraud feature-store cache hit ratio")
                .buildWithCallback(m -> {
                    double lf = load.latencyFactor();
                    double v = 0.97 - 0.12 * (lf - 1) - 0.02 * ThreadLocalRandom.current().nextDouble();
                    m.record(clamp(v, 0.4, 0.999));
                });

        meter.gaugeBuilder("demo.worker.in_flight").setUnit("1")
                .setDescription("In-flight worker threads")
                .buildWithCallback(m -> {
                    double rf = load.rateFactor();
                    m.record(clamp(2 + 34 * rf, 0, 500));
                });

        meter.gaugeBuilder("demo.queue.lag").setUnit("{message}")
                .setDescription("Pending work backlog")
                .buildWithCallback(m -> {
                    double rf = load.rateFactor();
                    double lf = load.latencyFactor();
                    m.record(clamp(20 + 400 * (rf - 0.5) + 1500 * (lf - 1), 0, 50_000));
                });
    }

    private double poolUsage(int poolMax) {
        double rf = load.rateFactor();
        double lf = load.latencyFactor();
        return clamp(6 + 34 * rf * (0.7 + 0.5 * lf), 0, poolMax);
    }

    private static double clamp(double v, double lo, double hi) {
        return v < lo ? lo : (v > hi ? hi : v);
    }

    // ── business counter hooks (called from controllers) ────────────────────

    public void transferCompleted() { transfersCompleted.add(1); }
    public void transferFailed()    { transfersFailed.add(1); }
    public void cardApproved()      { cardApproved.add(1); }
    public void cardDeclined()      { cardDeclined.add(1); }
    public void billPaid()          { billsPaid.add(1); }
    public void fraudBlock(String channel) {
        fraudBlocked.add(1, Attributes.builder().put("channel", channel).build());
    }
}
