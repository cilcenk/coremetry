package com.coremetry.demo.exception;

import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.ResponseStatus;

/**
 * Thrown when a debit would push an account below its available balance.
 * Maps to HTTP 422 (Unprocessable Entity) — the request was well-formed
 * but the business rule rejects it. Carries a cause chain so the
 * stacktrace recorded on the span shows the underlying ledger error.
 */
@ResponseStatus(HttpStatus.UNPROCESSABLE_ENTITY)
public class InsufficientFundsException extends RuntimeException {
    public InsufficientFundsException(String message) {
        super(message);
    }
    public InsufficientFundsException(String message, Throwable cause) {
        super(message, cause);
    }
}
