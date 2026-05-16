package com.example.app.service;

import com.example.app.entity.UserProfile;
import com.example.app.repository.UserProfileRepository;
import java.util.Optional;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

@Service
@Transactional
public class UserProfileService {

  private final UserProfileRepository userProfileRepository;

  public UserProfileService(UserProfileRepository userProfileRepository) {
    this.userProfileRepository = userProfileRepository;
  }

  public Optional<UserProfile> getProfile() {
    return userProfileRepository.findTopByOrderByCreatedAtAsc();
  }

  public UserProfile upsertProfile(String nickname, String signature, String avatarUrl) {
    UserProfile profile =
        userProfileRepository
            .findTopByOrderByCreatedAtAsc()
            .orElseGet(() -> new UserProfile(nickname, signature, avatarUrl));
    profile.setNickname(nickname);
    profile.setSignature(signature);
    profile.setAvatarUrl(avatarUrl);
    return userProfileRepository.save(profile);
  }
}

