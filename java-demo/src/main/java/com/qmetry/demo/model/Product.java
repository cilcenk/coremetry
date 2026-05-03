package com.qmetry.demo.model;

import jakarta.persistence.*;

@Entity
@Table(name = "products")
public class Product {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;
    private String name;
    private String category;
    private double price;
    private int stock;

    public Long getId() { return id; }
    public String getName() { return name; }
    public String getCategory() { return category; }
    public double getPrice() { return price; }
    public int getStock() { return stock; }

    public void setName(String n) { this.name = n; }
    public void setCategory(String c) { this.category = c; }
    public void setPrice(double p) { this.price = p; }
    public void setStock(int s) { this.stock = s; }
}
