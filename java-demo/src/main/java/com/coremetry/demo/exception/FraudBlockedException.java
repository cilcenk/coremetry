package com.coremetry.demo.exception;

import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.ResponseStatus;

/**
 * Thrown when the fraud-scoring service blocks a movement. Maps to HTTP
 * 403 (Forbidden) — the operation is understood but disallowed by the
 * fraud engine. Cause chain carries the score / rule that fired.
 */
@ResponseStatus(HttpStatus.FORBIDDEN)
public class FraudBlockedException extends RuntimeException {
    public FraudBlockedException(String message) {
        super(message);
    }
    public FraudBlockedException(String message, Throwable cause) {
        super(message, cause);
    }
}
