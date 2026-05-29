package com.coremetry.demo.controller;

import com.coremetry.demo.gateway.CoreBankingGateway;
import com.coremetry.demo.model.Account;
import com.coremetry.demo.model.Transaction;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.slf4j.MDC;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.server.ResponseStatusException;

import java.util.List;
import java.util.Map;

/**
 * Account inquiries — the highest-traffic surface in retail banking
 * (customers checking balances). Reads go through the CoreBankingGateway
 * so each one looks like an Oracle round-trip in the trace.
 */
@RestController
@RequestMapping("/api/accounts")
public class AccountController {
    private static final Logger log = LoggerFactory.getLogger(AccountController.class);

    private final CoreBankingGateway core;

    public AccountController(CoreBankingGateway core) {
        this.core = core;
    }

    /** Balance inquiry — GET /api/accounts/{accountNo}. */
    @GetMapping("/{accountNo}")
    public Account get(@PathVariable String accountNo) {
        MDC.put("account.id", accountNo);
        try {
            Account a = core.fetchAccount(accountNo)
                    .orElseThrow(() -> new ResponseStatusException(
                            HttpStatus.NOT_FOUND, "account not found"));
            log.info("balance inquiry account={} balance={} {}",
                    a.getAccountNo(), a.getBalance(), a.getCurrency());
            return a;
        } finally {
            MDC.remove("account.id");
        }
    }

    /** Mini-statement — GET /api/accounts/{accountNo}/statement. */
    @GetMapping("/{accountNo}/statement")
    public Map<String, Object> statement(@PathVariable String accountNo,
                                          @RequestParam(defaultValue = "10") int limit) {
        MDC.put("account.id", accountNo);
        try {
            Account a = core.fetchAccount(accountNo)
                    .orElseThrow(() -> new ResponseStatusException(
                            HttpStatus.NOT_FOUND, "account not found"));
            int capped = Math.min(Math.max(limit, 1), 50);
            List<Transaction> entries = core.recentEntries(accountNo, capped);
            log.info("statement account={} entries={}", accountNo, entries.size());
            return Map.of(
                    "account", a.getAccountNo(),
                    "balance", a.getBalance(),
                    "currency", a.getCurrency(),
                    "entries", entries);
        } finally {
            MDC.remove("account.id");
        }
    }
}
