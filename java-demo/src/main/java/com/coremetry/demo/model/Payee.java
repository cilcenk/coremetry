package com.coremetry.demo.model;

import jakarta.persistence.*;

/**
 * A registered bill / transfer payee for an account (utility company,
 * landlord, another bank's account, etc.).
 */
@Entity
@Table(name = "payees")
public class Payee {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;

    @Column(name = "owner_account")
    private String ownerAccount;

    private String name;

    /** the destination account / biller reference */
    @Column(name = "target_account")
    private String targetAccount;

    /** UTILITY | TELECOM | RENT | TRANSFER */
    private String category;

    public Long getId() { return id; }
    public String getOwnerAccount() { return ownerAccount; }
    public String getName() { return name; }
    public String getTargetAccount() { return targetAccount; }
    public String getCategory() { return category; }

    public void setOwnerAccount(String o) { this.ownerAccount = o; }
    public void setName(String n) { this.name = n; }
    public void setTargetAccount(String t) { this.targetAccount = t; }
    public void setCategory(String c) { this.category = c; }
}
