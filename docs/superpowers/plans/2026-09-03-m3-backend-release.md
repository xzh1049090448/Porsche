# M3 后端诊断候选发布与回滚计划

当前状态：用户已明确确认，本计划所指精确镜像已上传并于2026-09-03T05:50:10Z完成后端替换和健康核验。旧容器保留；真实SSE复测仍FAIL。下方准备阶段“待授权/未执行”等内容保留为历史计划，执行结果见末尾及release-result.json。

## 发布对象与内容

| 项目 | 精确值 |
| --- | --- |
| 目标主机 | SSH overseaCloud，172.245.142.21 |
| 服务 | ai-gateway-go，https://aiportcloud.com |
| 代码候选 | 04ed72806f5ca139d219d166452e0473ba5bf1a1 |
| 镜像标签 | porsche-m3-diagnostics:04ed728 |
| 最终镜像 ID | sha256:a69cfdab1cc8e18056286ae3991669d37515994041664b3fed5b6290ac602316 |
| 上传文件 | /private/tmp/porsche-m3-diagnostics-04ed728.image.tar |
| 文件 SHA256 | af8a122d1fa5018a981d4757aff03b0b204ef8048c38f2632eb47e343ad0c580 |
| 当前容器 ID | d2de587030c3055d4c4e913c3d0a1052297294810e6a4047b81b710edfe0b56b |
| 回滚镜像 ID | sha256:31abaeb16799f1bfad6501cf7d765ea443d5682561bbeed3f74255c8d8733b9a |

上传包包含公开 Alpine/CA 运行基础层及私有 server、bootstrap-root 二进制和镜像元数据，不包含源码归档或生产配置。bootstrap-root 仅随镜像携带，不执行引导。原 source+binary tar.gz 是历史本地构建证据，不作为此次上传对象。

镜像仅增加单模型 platform SSE 脱敏诊断；没有新增迁移或公开 API 契约变更。保留现有前端158a00e、数据库和Redis。后端容器替换将有短暂服务中断；成功启动也不等于503已修复。

## 已核验与验证边界

本机离线构建参数为 --network=none --pull=false。公开基础层先在远端单独构建，再下载并校验SHA；私有二进制只在本机加入。镜像导出、重新加载后以最终平台镜像ID核验 amd64/linux、/app、命令["./server"]、源码标签和CA证书。镜像内server及bootstrap-root哈希与精确源码候选的构建清单一致；两个二进制嵌入revision=04ed728全SHA、vcs.modified=false。标签本身不单独作为来源证明。

无网络/无凭据启动得到预期 JIEKOU_API_KEY must be non-empty、exit1，只证明入口可执行及配置拒绝行为。未验证候选真实配置启动、API健康、诊断日志或SSE恢复。完整本地代码验证此前为293全量/46竞态pass事件、0fail/skip、vet/build及独立规格/质量PASS，本轮仅镜像包装与文档，无业务代码变更。

详见 ../reports/validation/m3-backend-diagnostic-candidate.json 及 m3-image-verification.json。历史原始构建包中的imageBuilt=false保留为当时快照；当前以本计划和更新清单为准。

## 授权边界

自动审批此前拒绝向该远端上传含私有源码和后端二进制的构建包，理由为缺少对该目标及该payload的明确上传授权，且目的地受信状态未充分确认。被拒操作未执行，也未改用其他通道传送同一私有内容。远端仅处理公开基础层。

本次待授权的具体动作：将上述精确镜像归档上传至overseaCloud独立私有暂存目录，加载并核对摘要，在下述条件全部成立后替换ai-gateway-go，保留旧容器/镜像，健康检查失败自动回滚。用户确认不取消工具自动审批；若再次拒绝，停止受阻动作并报告。批准前不读取生产秘密或执行切换。

## 发布步骤与中止条件

1. 上传前再次本机校验归档SHA256与精确镜像ID。获授权后在目标主机建立root持有、0700、非符号链接的独立暂存目录；只上传本计划中的image.tar，远端校验相同SHA后加载，核对linux/amd64、来源及两二进制哈希。加载失败不得停止旧服务。
2. 非阻塞按固定顺序获取 /var/lock/porsche-full-stack.deploy.lock、/var/lock/porsche-auth-acceptance.deploy.lock、/var/lock/ai-gateway-go.deploy.lock；全部成功才允许配置快照或切换，任一失败释放已持有锁并退出。锁一直保留至发布/回滚验证结束。
3. 在锁内重新核对当前精确容器ID、镜像、运行状态和配置。当前已观察：network=porsche-app，端口127.0.0.1:8000→8000/tcp，restart=no，Cmd=["./server"]，工作目录/app，无挂载，默认root。执行前还须核对未记录的运行参数；发现新增或无法无损保留的配置即停止，不能用默认值猜测。当前容器或镜像漂移也停止并重新准备计划。
4. 扫描当前容器及相关回滚容器，拒绝任何 ROOT_BOOTSTRAP_ 环境键；列表或inspect失败同样停止。只输出键存在与否和非秘密状态，不打印原始Env或完整inspect。
5. 仅在服务器本地保存必要运行配置和环境快照：root持有0700目录/0600普通非符号链接文件；不回传、不提交、不写日志。以当前容器实际配置为准，不假定/opt/Porsche/.env与运行态相等。转env-file时拒绝换行或不可无损表示值。构建替换命令前比对快照完整性，不通过则旧服务继续运行。
6. 先生成并检查使用精确ID的切换/回滚命令和有限超时健康检查；本计划是执行约束，并非已测试的部署脚本。不得直接调用会reset main或同时发布前端的restart-all/production-deploy/auth-acceptance脚本。诊断候选没有新增schema需求，不执行数据库迁移。
7. 保存旧容器和镜像，停止精确旧ID并改名为唯一ai-gateway-go-acceptance-rollback-<digits>，保持可原样恢复；按已核对配置运行精确候选image ID，绑定返回的新容器ID并复核它的名称、镜像、网络、端口和命令。任何异常进入回滚，不清理未知容器。
8. 源站健康检查使用Host: aiportcloud.com、http://127.0.0.1:8000/health，每次连接超时2秒/总超时3秒，重试整体期限90秒；同时检查容器持续运行及公网https://aiportcloud.com/health预期200。公网检查失败即按本计划回滚，不能宣布发布成功。健康检查不发模型生成请求。
9. 核对公开前端入口仍为index-i7ZWPv9J.js，其SHA256仍为7c133cb1197c701cc6288d2718384c92f8b4b24a15fd13d821f3930d8bf09033。记录精确新/旧容器ID、镜像ID、归档摘要、健康结果与时间。只保留脱敏证据，私有临时快照按执行结果受控清理；保留旧容器及镜像供回滚。

## 回滚

切换后启动、身份核验或健康检查失败：先核对待移除对象确为本次返回的新容器ID且镜像为本计划候选，然后仅停止/移除该新对象；将保留的精确旧ID恢复原名并启动，用相同源站与公网健康检查验证恢复。rename/run等中间步骤失败也按已记录实际状态恢复，不能假定新容器已创建。若对象身份不符，停止破坏性操作并报告；回滚失败不得吞掉错误或报成功。无数据库迁移回滚，无前端回滚，旧镜像不删除。本轮没有在生产执行或演练该回滚。

## 发布后的M3跟进与双方确认

- 先确认真实配置启动和诊断可关联，再使用既定专用账号进行登录/资料等不生成健康检查。
- 有效SSE总预算不重置：gpt-5.4-nano最多3次，每次max_tokens32；已用1次，剩余2次。每次发出前记录预算，不盲目重试。保存公开request ID的安全关联信息和脱敏诊断，确认失败发生阶段；响应恢复还须验证SSE终态、持久化及用量语义。
- M3-11当前FAIL（HTTP503、0帧），go-004仍blocked，web-009仍in_progress；日志候选通过不等于线上根因已修复或完整M3签收。
- 后端project_manager本轮书面确认可在锁定最终镜像ID/归档SHA后提交具体发布授权请求，并确认三锁、运行态私有快照、精确对象切换/回滚的准备要求；该回复不代替用户生产发布授权，也不是本最终归档或真实运行的独立验收。协调者已据此填入最终摘要。
- 责任人：协调者维护候选/预算与用户授权；后端project_manager核对部署后脱敏证据和失败阶段；前端执行角色完成真实浏览器复测，质量角色独立复核实际结果；双方再按明确证据决定子项及整体签收。

## 最终材料书面复核

后端project_manager已只读复核本计划、候选清单与镜像验证JSON，明确回复：“材料足够提交具体用户授权申请，未发现阻塞该申请的材料问题。”确认最终候选SHA、镜像ID及归档SHA256一致；仅审查材料一致性，不独立验证归档内容、不代替用户授权、不签M3。用户明确授权、工具审批、远端摘要复核、完整运行配置快照和实际切换/回滚命令检查仍是执行前置条件。

协调者最终复核：本机归档SHA256与清单一致，前后端清单字节内容一致、JSON有效、状态未误改为部署或M3通过，git diff --check通过。本轮未修改业务代码，沿用此前代码测试证据，不把文档检查算作新增业务回归。

## 2026-09-03：后端诊断已发布，SSE第二次仍失败并定位解析阶段

- 用户明确确认上传及后端替换后完成发布：源码04ed728，镜像sha256:a69cfdab1cc8e18056286ae3991669d37515994041664b3fed5b6290ac602316；新容器9425ea244ad71944ef78474cc405208fbbbe7eb22fdffd2b81e269d328d85b0c于05:50:09Z启动，源站/公网health严格200。旧d2de587容器以ai-gateway-go-acceptance-rollback-1788414605780771454保留，未执行生产回滚；前端158a00e哈希保持。
- 首次预检因OomKillDisable的null/false表示差异安全停止，未停旧服务。公开基础镜像两版本API探针证实创建规范化，限定兼容该默认值后重新预检；true仍拒绝。三锁、私有运行配置JSON快照、候选create后逐字段比对均执行，成功后私有快照删除。脚本已归档，仅适用本次精确对象，不是可直接复用的常规发布入口。
- 真实SSE第2次于05:51:52Z发送：gpt-5.4-nano/max_tokens32，1POST/0refresh，HTTP503、gateway_upstream_unavailable、0帧。请求ID哈希与候选日志匹配，源码revision也匹配；上游返回200，sse_stream failed/malformed_chunk，首帧未发出，auth/catalog/前置quota及消息写入均success，assistant/usage/final_write未运行。尚不确定具体哪个字段或数据类型不兼容，不能宣称修复或归责供应商。
- 应用日调用1→2、剩余99→98、token计数0；这不是供应商零费用证明。测试会话删除200、注销204。预算累计2/3，剩1次×32，不重置、不盲目重放。
- M3-11仍FAIL、go-004仍blocked、web-009仍in_progress。后续先核对解析器拒绝条件与脱敏结构证据，再决定是否使用最后一次预算；不放宽白名单投影、不透传上游正文、不把health通过当M3签收。

执行细节：环境配置采用服务器本机Docker Unix API的JSON复制而非env-file，避免文本转换；快照及身份manifest以0600独占文件、fsync和硬链接原子发布，停旧前完成。先创建不启动的候选并比对，再切换；三锁贯穿。SIGINT/SIGTERM/SIGHUP进入恢复流程，回滚时忽略重复信号；SIGKILL或主机故障不能保证自动恢复，须重新获取三锁、读取服务器私有身份manifest并核对对象后按本计划回滚，不能直接重跑已绑定旧ID的脚本。

Docker规范化依据：[官方资源约束说明](https://docs.docker.com/engine/containers/resource_constraints/)规定默认允许OOM killer；远端公开镜像在API1.47与1.54的无启动probe均将null变false，已按精确ID清理。兼容范围仅old null/new false，true继续拒绝，其他配置不作默认推断。

本轮执行器首次启动自动安装了3个本地Playwright技能依赖并开始下载浏览器；仅终止本次执行器及其下载子进程，未发送业务请求。随后显式复用已有Playwright/Chrome运行正式复测，未更改前后端项目依赖。
