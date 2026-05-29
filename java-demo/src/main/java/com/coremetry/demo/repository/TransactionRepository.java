package com.coremetry.demo.repository;

import com.coremetry.demo.model.Transaction;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.data.domain.Pageable;
import java.util.List;

public interface TransactionRepository extends JpaRepository<Transaction, Long> {
    List<Transaction> findByFromAccountOrToAccountOrderByCreatedAtDesc(
            String fromAccount, String toAccount, Pageable pageable);
}
