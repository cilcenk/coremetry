package com.coremetry.jboss;

import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import jakarta.ejb.Singleton;
import jakarta.ejb.Startup;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.Random;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.logging.Level;
import java.util.logging.Logger;

// Self-driving traffic: an @Startup singleton fires HTTP calls
// against the app's own JAX-RS surface so the demo emits a
// steady trace + metric stream without an external generator.
// Each call is a fresh HTTP server trace (root span) — perfect
// for the Coremetry waterfall + service summary. The JDK
// HttpClient also lights up the agent's outbound-call
// instrumentation so the trace has a child span chain.
//
// JBOSS_DEMO_RPS env var controls the target rate; default is
// modest (2 req/s) so the demo doesn't drown a laptop. Set to
// 0 to disable the driver and only emit traces on external
// requests.
@Singleton
@Startup
public class LoadDriver {

    private static final Logger LOG = Logger.getLogger(LoadDriver.class.getName());
    private static final Random RNG = new Random();
    private static final String[] PATHS = { "/api/orders", "/api/payments", "/api/burn", "/api/health" };

    private ScheduledExecutorService scheduler;
    private HttpClient client;
    private String baseUrl;

    @PostConstruct
    public void start() {
        int rps = parseEnvInt("JBOSS_DEMO_RPS", 2);
        if (rps <= 0) {
            LOG.info("LoadDriver disabled (JBOSS_DEMO_RPS=0)");
            return;
        }
        // Resolve own URL — WildFly binds to 0.0.0.0 inside the
        // container; loopback works for the self-call.
        baseUrl = "http://127.0.0.1:8080/jboss-demo";
        client = HttpClient.newBuilder()
            .connectTimeout(Duration.ofSeconds(2))
            .build();

        long intervalMs = Math.max(1, 1000 / rps);
        scheduler = Executors.newScheduledThreadPool(2);
        scheduler.scheduleAtFixedRate(this::tick, 5_000, intervalMs, TimeUnit.MILLISECONDS);
        LOG.info("LoadDriver active: rps=" + rps + " baseUrl=" + baseUrl);
    }

    @PreDestroy
    public void stop() {
        if (scheduler != null) scheduler.shutdownNow();
    }

    private void tick() {
        String path = PATHS[RNG.nextInt(PATHS.length)];
        try {
            HttpRequest req = HttpRequest.newBuilder(URI.create(baseUrl + path))
                .timeout(Duration.ofSeconds(3))
                .GET().build();
            HttpResponse<String> resp = client.send(req, HttpResponse.BodyHandlers.ofString());
            if (resp.statusCode() >= 500) {
                LOG.fine("self-call " + path + " -> " + resp.statusCode());
            }
        } catch (Exception e) {
            LOG.log(Level.FINE, "self-call failed: " + e.getMessage());
        }
    }

    private static int parseEnvInt(String name, int dflt) {
        String v = System.getenv(name);
        if (v == null || v.isEmpty()) return dflt;
        try { return Integer.parseInt(v.trim()); } catch (NumberFormatException e) { return dflt; }
    }
}
