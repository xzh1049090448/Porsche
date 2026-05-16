package com.example.app.repository;

import com.example.app.entity.BlogPost;
import java.util.List;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.data.jpa.repository.Query;
import org.springframework.data.repository.query.Param;

public interface BlogPostRepository extends JpaRepository<BlogPost, Long> {

  @Query("select b from BlogPost b " +
         "where (:keyword is null or lower(b.title) like lower(concat('%', :keyword, '%')) " +
         "   or lower(b.content) like lower(concat('%', :keyword, '%')))")
  List<BlogPost> searchByKeyword(@Param("keyword") String keyword);
}

