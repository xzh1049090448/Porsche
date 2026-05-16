package com.example.app.service;

import com.example.app.entity.SampleEntity;
import com.example.app.repository.SampleRepository;
import java.util.List;
import org.springframework.data.domain.Sort;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

@Service
public class SampleService {

  private final SampleRepository sampleRepository;

  public SampleService(SampleRepository sampleRepository) {
    this.sampleRepository = sampleRepository;
  }

  @Transactional(readOnly = true)
  public List<SampleEntity> list() {
    return sampleRepository.findAll(Sort.by(Sort.Direction.DESC, "id"));
  }

  @Transactional
  public SampleEntity create(String name) {
    if (name == null || name.isBlank()) {
      throw new IllegalArgumentException("name must not be blank");
    }
    return sampleRepository.save(new SampleEntity(name.trim()));
  }
}
