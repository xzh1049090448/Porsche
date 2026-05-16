package com.example.app.entity;

import jakarta.persistence.Column;
import jakarta.persistence.Entity;
import jakarta.persistence.Table;

@Entity
@Table(name = "user_profile")
public class UserProfile extends BaseEntity {

  @Column(nullable = false, length = 64)
  private String nickname;

  @Column(length = 255)
  private String signature;

  @Column(length = 255)
  private String avatarUrl;

  protected UserProfile() {}

  public UserProfile(String nickname, String signature, String avatarUrl) {
    this.nickname = nickname;
    this.signature = signature;
    this.avatarUrl = avatarUrl;
  }

  public String getNickname() {
    return nickname;
  }

  public void setNickname(String nickname) {
    this.nickname = nickname;
  }

  public String getSignature() {
    return signature;
  }

  public void setSignature(String signature) {
    this.signature = signature;
  }

  public String getAvatarUrl() {
    return avatarUrl;
  }

  public void setAvatarUrl(String avatarUrl) {
    this.avatarUrl = avatarUrl;
  }
}

