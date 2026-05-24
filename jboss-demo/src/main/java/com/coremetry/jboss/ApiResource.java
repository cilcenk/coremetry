package com.coremetry.jboss;

import jakarta.ws.rs.GET;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.Produces;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;

import java.util.HashMap;
import java.util.Map;
import java.util.Random;
import java.util.concurrent.atomic.AtomicLong;
import java.util.logging.Level;
import java.util.logging.Logger;

// Tiny JAX-RS surface exercising a few patterns the OTel agent
// auto-instruments: HTTP server spans, JUL log correlation (logs
// get the active span's trace_id stamped), JDK HttpClient (downstream
// span), and a CPU-burner so the JFR + Pyroscope profilers have
// something to sample.
//
// Deliberately small — the goal is a representative trace +
// metric stream, not a real app. LoadDriver beats this surface
// continuously so traffic shows up in Coremetry without any
// external generator.
@Path("/")
public class ApiResource {

    private static final Logger LOG = Logger.getLogger(ApiResource.class.getName());
    private static final Random RNG = new Random();
    private static final AtomicLong COUNTER = new AtomicLong();

    @GET
    @Path("/health")
    @Produces(MediaType.APPLICATION_JSON)
    public Map<String, Object> health() {
        Map<String, Object> r = new HashMap<>();
        r.put("status", "UP");
        r.put("server", System.getProperty("jboss.server.name", "wildfly"));
        return r;
    }

    @GET
    @Path("/orders")
    @Produces(MediaType.APPLICATION_JSON)
    public Map<String, Object> orders() {
        long n = COUNTER.incrementAndGet();
        // A pinch of latency so the histogram is interesting.
        sleepy(20 + RNG.nextInt(40));
        if (RNG.nextInt(100) < 3) {
            LOG.log(Level.WARNING, "transient order lookup hiccup #" + n);
            return Map.of("id", n, "status", "retry");
        }
        LOG.info("served order #" + n);
        return Map.of("id", n, "status", "ok", "items", RNG.nextInt(5) + 1);
    }

    @GET
    @Path("/payments")
    @Produces(MediaType.APPLICATION_JSON)
    public Response payments() {
        // Occasional 5xx so the error-rate panel earns its keep.
        if (RNG.nextInt(100) < 5) {
            LOG.severe("payment gateway timeout");
            return Response.status(502)
                .entity(Map.of("error", "upstream_timeout"))
                .build();
        }
        sleepy(30 + RNG.nextInt(80));
        return Response.ok(Map.of("authorized", true, "amount", RNG.nextInt(10_000) / 100.0)).build();
    }

    @GET
    @Path("/burn")
    @Produces(MediaType.TEXT_PLAIN)
    public String burn() {
        // 50–250 ms CPU burner. Forces JFR's cpu-profile event
        // to actually emit samples; without sustained on-CPU
        // work the profiler returns near-empty windows.
        long deadline = System.nanoTime() + (50_000_000L + RNG.nextInt(200_000_000));
        double x = 0;
        while (System.nanoTime() < deadline) {
            x += Math.sqrt(RNG.nextDouble());
        }
        return "burned " + x;
    }

    private static void sleepy(int ms) {
        try { Thread.sleep(ms); } catch (InterruptedException ignored) {
            Thread.currentThread().interrupt();
        }
    }
}
