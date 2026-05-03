package com.qmetry.demo.controller;

import com.qmetry.demo.model.Product;
import com.qmetry.demo.repository.ProductRepository;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;
import org.springframework.http.HttpStatus;

import java.util.List;
import java.util.concurrent.ThreadLocalRandom;

@RestController
@RequestMapping("/api/products")
public class ProductController {
    private static final Logger log = LoggerFactory.getLogger(ProductController.class);
    private final ProductRepository repo;

    public ProductController(ProductRepository repo) { this.repo = repo; }

    @GetMapping
    public List<Product> list(@RequestParam(required = false) String category) throws InterruptedException {
        Thread.sleep(ThreadLocalRandom.current().nextInt(20, 80));
        log.info("Listing products category={}", category);
        return category == null ? repo.findAll() : repo.findByCategory(category);
    }

    @GetMapping("/{id}")
    public Product byId(@PathVariable Long id) throws InterruptedException {
        Thread.sleep(ThreadLocalRandom.current().nextInt(10, 50));
        return repo.findById(id).orElseThrow(() -> {
            log.warn("Product not found: {}", id);
            return new ResponseStatusException(HttpStatus.NOT_FOUND);
        });
    }

    @GetMapping("/search")
    public List<Product> search(@RequestParam String q) throws InterruptedException {
        Thread.sleep(ThreadLocalRandom.current().nextInt(40, 120));
        log.info("Search products q='{}'", q);
        return repo.search(q);
    }
}
