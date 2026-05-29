package com.coremetry.demo.model;

import jakarta.persistence.*;
import java.time.Instant;

/**
 * A retail-banking account. The {@code accountNo} is the operator-visible
 * handle (e.g. customer statement reference); {@code id} stays the JPA PK.
 */
@Entity
@Table(name = "accounts")
public class Account {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;

    @Column(name = "account_no", unique = true)
    private String accountNo;

    private String customer;

    /** CHECKING | SAVINGS | CREDIT */
    private String type;

    private double balance;

    private String currency;

    /** ACTIVE | FROZEN | CLOSED */
    private String status;

    private Instant openedAt = Instant.now();

    public Long getId() { return id; }
    public String getAccountNo() { return accountNo; }
    public String getCustomer() { return customer; }
    public String getType() { return type; }
    public double getBalance() { return balance; }
    public String getCurrency() { return currency; }
    public String getStatus() { return status; }
    public Instant getOpenedAt() { return openedAt; }

    public void setAccountNo(String n) { this.accountNo = n; }
    public void setCustomer(String c) { this.customer = c; }
    public void setType(String t) { this.type = t; }
    public void setBalance(double b) { this.balance = b; }
    public void setCurrency(String c) { this.currency = c; }
    public void setStatus(String s) { this.status = s; }
}
