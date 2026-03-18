package com.example.app.controller;

import com.example.app.common.ApiResponse;
import com.example.app.entity.SampleEntity;
import com.example.app.service.SampleService;
import jakarta.validation.constraints.NotBlank;
import java.util.List;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/samples")
public class SampleController {

  private final SampleService sampleService;

  public SampleController(SampleService sampleService) {
    this.sampleService = sampleService;
  }

  @GetMapping
  public ApiResponse<List<SampleEntity>> list() {
    return ApiResponse.ok(sampleService.list());
  }

  public record CreateSampleRequest(@NotBlank String name) {}

  @PostMapping
  public ApiResponse<SampleEntity> create(@RequestBody CreateSampleRequest req) {
    return ApiResponse.ok(sampleService.create(req.name()));
  }
}
