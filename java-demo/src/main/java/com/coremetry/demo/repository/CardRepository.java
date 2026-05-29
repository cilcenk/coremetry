package com.coremetry.demo.repository;

import com.coremetry.demo.model.Card;
import org.springframework.data.jpa.repository.JpaRepository;
import java.util.List;

public interface CardRepository extends JpaRepository<Card, Long> {
    List<Card> findByAccountNo(String accountNo);
}
