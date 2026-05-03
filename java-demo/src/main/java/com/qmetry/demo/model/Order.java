package com.qmetry.demo.model;

import jakarta.persistence.*;
import java.time.Instant;

@Entity
@Table(name = "orders")
public class Order {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;
    private Long userId;
    private Long productId;
    private int quantity;
    private double total;
    private String status;
    private Instant createdAt = Instant.now();

    public Long getId() { return id; }
    public Long getUserId() { return userId; }
    public Long getProductId() { return productId; }
    public int getQuantity() { return quantity; }
    public double getTotal() { return total; }
    public String getStatus() { return status; }
    public Instant getCreatedAt() { return createdAt; }

    public void setUserId(Long uid) { this.userId = uid; }
    public void setProductId(Long pid) { this.productId = pid; }
    public void setQuantity(int q) { this.quantity = q; }
    public void setTotal(double t) { this.total = t; }
    public void setStatus(String s) { this.status = s; }
}
