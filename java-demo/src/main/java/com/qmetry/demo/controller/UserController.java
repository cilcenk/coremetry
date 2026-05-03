package com.qmetry.demo.controller;

import com.qmetry.demo.model.User;
import com.qmetry.demo.repository.UserRepository;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;
import org.springframework.http.HttpStatus;

import java.util.concurrent.ThreadLocalRandom;

@RestController
@RequestMapping("/api/users")
public class UserController {
    private static final Logger log = LoggerFactory.getLogger(UserController.class);
    private final UserRepository repo;

    public UserController(UserRepository repo) { this.repo = repo; }

    @GetMapping("/{id}")
    public User byId(@PathVariable Long id) throws InterruptedException {
        Thread.sleep(ThreadLocalRandom.current().nextInt(15, 60));
        // Simulate auth-failure path 5% of the time
        if (ThreadLocalRandom.current().nextInt(100) < 5) {
            log.error("Auth failed for user id={}", id);
            throw new ResponseStatusException(HttpStatus.UNAUTHORIZED, "auth failed");
        }
        return repo.findById(id).orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND));
    }
}
