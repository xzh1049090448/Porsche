package com.example.app.repository;

import com.example.app.entity.UserProfile;
import java.util.Optional;
import org.springframework.data.jpa.repository.JpaRepository;

public interface UserProfileRepository extends JpaRepository<UserProfile, Long> {

  Optional<UserProfile> findTopByOrderByCreatedAtAsc();
}

