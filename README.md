## Porsche Spring Boot 应用

This is Dog Dan's debut work.

这里是实际的 Spring Boot 项目根目录，使用 Maven 构建。

### 运行

```bash
./mvnw.cmd spring-boot:run
```

### 主要模块

- `src/main/java/com/example/app/controller`：接口层
- `src/main/java/com/example/app/service`：业务层
- `src/main/java/com/example/app/repository`：数据访问层（JPA）
- `src/main/java/com/example/app/entity`：实体
- `src/main/java/com/example/app/exception`：全局异常处理
- `src/main/java/com/example/app/common`：统一返回体等通用类
