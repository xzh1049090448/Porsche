package com.example.app.api.controller;

import com.example.app.common.ApiResponse;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/sample")
public class SampleApiController {

  @GetMapping("/ping")
  public ApiResponse<String> ping() {
    return ApiResponse.ok("pong");
  }
}

