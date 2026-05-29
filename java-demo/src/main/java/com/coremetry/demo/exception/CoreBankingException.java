package com.coremetry.demo.exception;

/**
 * Simulated Oracle core-banking infrastructure failure. Carries the raw
 * ORA-xxxxx code so the manual CLIENT span on the gateway can record a
 * realistic exception (ORA-00060 deadlock, ORA-12170 TNS timeout). This
 * is the typical "cause" at the bottom of the cause chain when a
 * business exception is triggered by the backend ledger erroring out.
 */
public class CoreBankingException extends RuntimeException {
    private final String oraCode;

    public CoreBankingException(String oraCode, String message) {
        super(oraCode + ": " + message);
        this.oraCode = oraCode;
    }

    public String getOraCode() { return oraCode; }
}
