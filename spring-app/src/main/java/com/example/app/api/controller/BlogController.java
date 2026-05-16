package com.example.app.api.controller;

import com.example.app.common.ApiResponse;
import com.example.app.entity.BlogPost;
import com.example.app.entity.UserProfile;
import com.example.app.service.BlogPostService;
import com.example.app.service.UserProfileService;
import jakarta.validation.constraints.NotBlank;
import java.util.List;
import java.util.Optional;
import org.springframework.validation.annotation.Validated;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/blog")
@Validated
public class BlogController {

  private final BlogPostService blogPostService;
  private final UserProfileService userProfileService;

  public BlogController(BlogPostService blogPostService, UserProfileService userProfileService) {
    this.blogPostService = blogPostService;
    this.userProfileService = userProfileService;
  }

  @GetMapping("/posts")
  public ApiResponse<List<BlogPost>> listPosts(@RequestParam(required = false) String keyword) {
    return ApiResponse.ok(blogPostService.search(keyword));
  }

  public record BlogPostRequest(
      @NotBlank(message = "标题不能为空") String title,
      @NotBlank(message = "内容不能为空") String content,
      String tags) {}

  @PostMapping("/posts")
  public ApiResponse<BlogPost> createPost(@RequestBody BlogPostRequest request) {
    BlogPost post =
        blogPostService.create(request.title(), request.content(), request.tags());
    return ApiResponse.ok(post);
  }

  @PutMapping("/posts/{id}")
  public ApiResponse<BlogPost> updatePost(
      @PathVariable Long id, @RequestBody BlogPostRequest request) {
    BlogPost post =
        blogPostService.update(id, request.title(), request.content(), request.tags());
    return ApiResponse.ok(post);
  }

  @DeleteMapping("/posts/{id}")
  public ApiResponse<Void> deletePost(@PathVariable Long id) {
    blogPostService.delete(id);
    return ApiResponse.ok(null);
  }

  @GetMapping("/profile")
  public ApiResponse<UserProfile> getProfile() {
    Optional<UserProfile> profile = userProfileService.getProfile();
    return profile.map(ApiResponse::ok).orElseGet(() -> ApiResponse.ok(null));
  }

  public record ProfileRequest(
      @NotBlank(message = "昵称不能为空") String nickname, String signature, String avatarUrl) {}

  @PutMapping("/profile")
  public ApiResponse<UserProfile> updateProfile(@RequestBody ProfileRequest request) {
    UserProfile profile =
        userProfileService.upsertProfile(
            request.nickname(), request.signature(), request.avatarUrl());
    return ApiResponse.ok(profile);
  }
}

