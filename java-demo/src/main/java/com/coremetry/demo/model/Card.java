package com.coremetry.demo.model;

import jakarta.persistence.*;

/**
 * A payment card bound to an account. Only the last four digits are kept
 * (full PAN never enters the system — full fidelity on everything else).
 */
@Entity
@Table(name = "cards")
public class Card {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;

    @Column(name = "account_no")
    private String accountNo;

    private String last4;

    /** VISA | MASTERCARD | AMEX */
    private String network;

    /** ACTIVE | BLOCKED | EXPIRED */
    private String status;

    public Long getId() { return id; }
    public String getAccountNo() { return accountNo; }
    public String getLast4() { return last4; }
    public String getNetwork() { return network; }
    public String getStatus() { return status; }

    public void setAccountNo(String a) { this.accountNo = a; }
    public void setLast4(String l) { this.last4 = l; }
    public void setNetwork(String n) { this.network = n; }
    public void setStatus(String s) { this.status = s; }
}
