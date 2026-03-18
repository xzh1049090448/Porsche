package com.example.app.controller;

import com.example.app.common.ApiResponse;
import com.example.app.service.HelloService;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api")
public class HelloController {

  private final HelloService helloService;

  public HelloController(HelloService helloService) {
    this.helloService = helloService;
  }

  @GetMapping("/hello")
  public ApiResponse<String> hello(@RequestParam(defaultValue = "world") String name) {
    return ApiResponse.ok(helloService.sayHello(name));
  }
}
