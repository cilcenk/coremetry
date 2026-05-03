package com.qmetry.demo.controller;

import com.qmetry.demo.model.Order;
import com.qmetry.demo.model.Product;
import com.qmetry.demo.model.User;
import com.qmetry.demo.repository.OrderRepository;
import com.qmetry.demo.repository.ProductRepository;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.client.RestTemplate;
import org.springframework.web.server.ResponseStatusException;
import org.springframework.http.HttpStatus;

import java.util.List;
import java.util.Map;
import java.util.concurrent.ThreadLocalRandom;

@RestController
@RequestMapping("/api/orders")
public class OrderController {
    private static final Logger log = LoggerFactory.getLogger(OrderController.class);

    private final OrderRepository orders;
    private final ProductRepository products;
    private final RestTemplate http;

    @Value("${demo.self-base-url:http://localhost:8080}")
    private String baseUrl;

    public OrderController(OrderRepository o, ProductRepository p, RestTemplate http) {
        this.orders = o; this.products = p; this.http = http;
    }

    @PostMapping
    public Order create(@RequestBody Map<String, Object> body) throws InterruptedException {
        Long userId = ((Number) body.get("userId")).longValue();
        Long productId = ((Number) body.get("productId")).longValue();
        int qty = ((Number) body.getOrDefault("quantity", 1)).intValue();

        // 1) Validate user via internal HTTP call (instrumented as CLIENT span)
        User user = http.getForObject(baseUrl + "/api/users/" + userId, User.class);

        // 2) DB lookup for the product
        Thread.sleep(ThreadLocalRandom.current().nextInt(10, 40));
        Product product = products.findById(productId)
                .orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND, "product"));

        // 3) Simulated payment call — fails 5% of the time
        Thread.sleep(ThreadLocalRandom.current().nextInt(80, 220));
        if (ThreadLocalRandom.current().nextInt(100) < 5) {
            log.error("Payment failed for user={} product={} amount={}",
                    user.getId(), product.getId(), product.getPrice() * qty);
            throw new ResponseStatusException(HttpStatus.BAD_GATEWAY, "payment gateway timeout");
        }

        // 4) Persist order
        Order o = new Order();
        o.setUserId(userId); o.setProductId(productId); o.setQuantity(qty);
        o.setTotal(product.getPrice() * qty); o.setStatus("CONFIRMED");
        Order saved = orders.save(o);
        log.info("Order created id={} user={} total={}", saved.getId(), userId, saved.getTotal());
        return saved;
    }

    @GetMapping("/by-user/{userId}")
    public List<Order> byUser(@PathVariable Long userId) {
        return orders.findByUserId(userId);
    }
}
