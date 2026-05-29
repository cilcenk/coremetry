package com.coremetry.demo.exception;

import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.ResponseStatus;

/**
 * Thrown when an account is FROZEN / CLOSED and cannot be debited or
 * credited. Maps to HTTP 409 (Conflict) — the account state conflicts
 * with the requested operation.
 */
@ResponseStatus(HttpStatus.CONFLICT)
public class AccountFrozenException extends RuntimeException {
    public AccountFrozenException(String message) {
        super(message);
    }
    public AccountFrozenException(String message, Throwable cause) {
        super(message, cause);
    }
}
