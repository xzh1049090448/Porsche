package com.example.app.entity;

import jakarta.persistence.Column;
import jakarta.persistence.Entity;
import jakarta.persistence.Table;

@Entity
@Table(name = "sample")
public class SampleEntity extends BaseEntity {

  @Column(nullable = false, length = 128)
  private String name;

  protected SampleEntity() {}

  public SampleEntity(String name) {
    this.name = name;
  }

  public String getName() {
    return name;
  }
}
