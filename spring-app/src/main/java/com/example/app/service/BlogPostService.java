package com.example.app.service;

import com.example.app.entity.BlogPost;
import com.example.app.repository.BlogPostRepository;
import java.util.List;
import java.util.Optional;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

@Service
@Transactional
public class BlogPostService {

  private final BlogPostRepository blogPostRepository;

  public BlogPostService(BlogPostRepository blogPostRepository) {
    this.blogPostRepository = blogPostRepository;
  }

  public BlogPost create(String title, String content, String tags) {
    BlogPost post = new BlogPost(title, content, tags);
    return blogPostRepository.save(post);
  }

  public Optional<BlogPost> findById(Long id) {
    return blogPostRepository.findById(id);
  }

  public List<BlogPost> search(String keyword) {
    if (keyword == null || keyword.isBlank()) {
      return blogPostRepository.findAll();
    }
    return blogPostRepository.searchByKeyword(keyword);
  }

  public BlogPost update(Long id, String title, String content, String tags) {
    BlogPost post = blogPostRepository
        .findById(id)
        .orElseThrow(() -> new IllegalArgumentException("Blog post not found: " + id));
    post.setTitle(title);
    post.setContent(content);
    post.setTags(tags);
    return blogPostRepository.save(post);
  }

  public void delete(Long id) {
    blogPostRepository.deleteById(id);
  }
}

