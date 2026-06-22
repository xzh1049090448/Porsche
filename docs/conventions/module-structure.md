# 模块分层规范

通用后端项目模块划分与职责说明。

| 模块       | 说明                                              |
| ---------- | ------------------------------------------------- |
| api        | Controller层，接口定义                            |
| common     | 公共模块：Domain，DTO，Enum，Constant，Req/Resp等 |
| service    | Service层，业务逻辑                               |
| repository | 数据访问层，访问DB（DAO、Mapper）                 |
| task       | 定时任务                                          |
| tool       | 工具类                                            |

## 适用范围

本规范适用于 **Java / Spring Boot** 类后端项目。当前 monorepo 中主要对应 [`spring-app/`](../spring-app/)。

Python（`ai-gateway/`）与 Go（`ai-gateway-go/`）子项目沿用各自语言惯例，可参考本规范的分层思想，但不强制相同目录命名。

### ai-gateway（Python）映射

| 规范模块 | 目录 |
| -------- | ---- |
| api | `app/api/` |
| common | `app/common/`（schemas、constants、errors、security） |
| service | `app/service/` |
| repository | `app/repository/` |
| task | `app/task/` |
| tool | `app/tool/` |

详见 [`ai-gateway/CONTEXT.md`](../ai-gateway/CONTEXT.md)。
