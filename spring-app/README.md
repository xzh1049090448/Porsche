## Porsche Spring Boot 应用

This is Dog Dan's debut work.

本目录为**独立的 Java/Maven 项目根**（与仓库根目录下的 `ai-gateway` 无耦合）。在此目录执行构建与运行命令。

### 运行

Windows（PowerShell）：

```powershell
cd spring-app
.\mvnw.cmd spring-boot:run
```

macOS / Linux：

```bash
cd spring-app
./mvnw spring-boot:run
```

### 主要模块

- `src/main/java/com/example/app/controller`：接口层
- `src/main/java/com/example/app/api/controller`：对外 API（如博客）
- `src/main/java/com/example/app/service`：业务层
- `src/main/java/com/example/app/repository`：数据访问层（JPA）
- `src/main/java/com/example/app/entity`：实体
- `src/main/java/com/example/app/exception`：全局异常处理
- `src/main/java/com/example/app/common`：统一返回体等通用类
