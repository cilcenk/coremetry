package com.qmetry.demo.service;

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
 * Drives synthetic traffic against our own HTTP endpoints so that the
 * OpenTelemetry javaagent has something to instrument continuously.
 * No telemetry code lives here — every span/log/metric is auto-emitted.
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
            if (pick < 35)        listProducts();
            else if (pick < 60)   searchProducts();
            else if (pick < 80)   fetchUser();
            else                  placeOrder();
            total.incrementAndGet();
        } catch (RestClientException e) {
            // these are normal — controllers throw 401/502 for some scenarios
        }
    }

    @Scheduled(fixedDelay = 30_000L)
    public void heartbeat() {
        log.info("LoadGenerator heartbeat: {} scenarios driven", total.get());
    }

    private void listProducts() {
        String cat = pick("electronics", "books", "kitchen", null);
        String url = baseUrl + "/api/products" + (cat != null ? "?category=" + cat : "");
        http.getForObject(url, Object.class);
    }
    private void searchProducts() {
        String q = pick("laptop", "headphones", "monitor", "mouse", "keyboard");
        http.getForObject(baseUrl + "/api/products/search?q=" + q, Object.class);
    }
    private void fetchUser() {
        long uid = ThreadLocalRandom.current().nextLong(1, 11);
        try {
            http.getForObject(baseUrl + "/api/users/" + uid, Object.class);
        } catch (RestClientException ignored) {}
    }
    private void placeOrder() {
        try {
            http.postForObject(baseUrl + "/api/orders",
                Map.of("userId", ThreadLocalRandom.current().nextLong(1, 11),
                       "productId", ThreadLocalRandom.current().nextLong(1, 21),
                       "quantity", ThreadLocalRandom.current().nextInt(1, 4)),
                Object.class);
        } catch (RestClientException ignored) {}
    }
    private static String pick(String... opts) {
        return opts[ThreadLocalRandom.current().nextInt(opts.length)];
    }
}
