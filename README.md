# Porsche 工作区

本仓库包含**两个相互独立**的子项目，可分别在各自目录内开发、构建与部署。

| 目录 | 说明 |
|------|------|
| [`spring-app/`](spring-app/) | Java 17 + Spring Boot + Maven（博客 API 等后端） |
| [`ai-gateway/`](ai-gateway/) | Python + FastAPI AI 网关（OpenAI 兼容中转） |

两者无代码依赖；若你希望拆成两个 Git 仓库，可将对应目录各自 `git init` 或使用子树/子模块策略迁移。

## 快速入口

- **Spring Boot**：进入 `spring-app/`，见该目录下的 [`README.md`](spring-app/README.md)。
- **AI Gateway**：进入 `ai-gateway/`，见 [`ai-gateway/README.md`](ai-gateway/README.md)（含合规说明与 Docker）。
