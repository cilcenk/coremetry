package com.coremetry.demo.model;

import jakarta.persistence.*;
import java.time.Instant;

/**
 * A ledger movement. Double-entry is simulated by recording the from/to
 * account numbers; the actual debit/credit is applied to the H2 balances
 * by the controllers (the core-banking gateway "posts" it under the hood).
 */
@Entity
@Table(name = "transactions")
public class Transaction {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;

    @Column(name = "reference", unique = true)
    private String reference;

    @Column(name = "from_account")
    private String fromAccount;

    @Column(name = "to_account")
    private String toAccount;

    private double amount;

    private String currency;

    /** TRANSFER | CARD_PAYMENT | BILL_PAYMENT */
    private String type;

    /** POSTED | DECLINED */
    private String status;

    /** human-readable decline / post reason */
    private String reason;

    private Instant createdAt = Instant.now();

    public Long getId() { return id; }
    public String getReference() { return reference; }
    public String getFromAccount() { return fromAccount; }
    public String getToAccount() { return toAccount; }
    public double getAmount() { return amount; }
    public String getCurrency() { return currency; }
    public String getType() { return type; }
    public String getStatus() { return status; }
    public String getReason() { return reason; }
    public Instant getCreatedAt() { return createdAt; }

    public void setReference(String r) { this.reference = r; }
    public void setFromAccount(String f) { this.fromAccount = f; }
    public void setToAccount(String t) { this.toAccount = t; }
    public void setAmount(double a) { this.amount = a; }
    public void setCurrency(String c) { this.currency = c; }
    public void setType(String t) { this.type = t; }
    public void setStatus(String s) { this.status = s; }
    public void setReason(String r) { this.reason = r; }
}
