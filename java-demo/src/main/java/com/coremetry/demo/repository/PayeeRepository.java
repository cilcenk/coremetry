package com.coremetry.demo.repository;

import com.coremetry.demo.model.Payee;
import org.springframework.data.jpa.repository.JpaRepository;
import java.util.List;

public interface PayeeRepository extends JpaRepository<Payee, Long> {
    List<Payee> findByOwnerAccount(String ownerAccount);
}
