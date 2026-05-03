package com.qmetry.demo.model;

import jakarta.persistence.*;

@Entity
@Table(name = "users")
public class User {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;
    private String name;
    private String email;

    public Long getId() { return id; }
    public String getName() { return name; }
    public String getEmail() { return email; }
    public void setName(String n) { this.name = n; }
    public void setEmail(String e) { this.email = e; }
}
