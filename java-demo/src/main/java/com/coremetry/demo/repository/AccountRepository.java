package com.coremetry.demo.repository;

import com.coremetry.demo.model.Account;
import org.springframework.data.jpa.repository.JpaRepository;
import java.util.Optional;

public interface AccountRepository extends JpaRepository<Account, Long> {
    Optional<Account> findByAccountNo(String accountNo);
}
