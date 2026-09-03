# M3 细分诊断候选发布单

状态：本地候选与验证完成；尚未上传或部署。此前已确认的发布对象04ed728保持运行，新候选为6e70784。

| 项目 | 精确对象 |
| --- | --- |
| 目标 | overseaCloud / 172.245.142.21，ai-gateway-go |
| 新源码 | 6e70784e182a4bae1e5b8399053b45429e8e4400 |
| 新镜像 | sha256:cb42daed3581c51a5e6f5ac5a237bec4b4e0c3540afb61d36acb27592dc2947f |
| 本机镜像包 | /private/tmp/porsche-m3-chunk-6e70784.image.tar |
| 归档SHA256 | 195a6f3870e64d79d6aa1f9dbe994baea230ae49c7ac5b92ec8711ec14b8fc2f |
| 回滚容器 | 9425ea244ad71944ef78474cc405208fbbbe7eb22fdffd2b81e269d328d85b0c |
| 回滚镜像 | sha256:a69cfdab1cc8e18056286ae3991669d37515994041664b3fed5b6290ac602316 |
| 计划私有暂存目录 | /var/tmp/porsche-m3-chunk-release-6e70784-20260903 |

上传内容仅公开运行基础层与私有server/bootstrap-root二进制，附源码版本标签；无源码归档或生产配置。helper不执行Root引导。代码只增加固定malformed_chunk_detail.reason/field，不改变公开503、大类malformed_chunk、接受/拒绝规则、数据库或计费。镜像无网络启动因缺JIEKOU_API_KEY退出1符合配置门禁，不代表真实服务健康。

执行沿用已审查且实测成功的上一轮切换机制，准备脚本../reports/validation/m3-chunk-release-prepared.py仅绑定本单精确对象：

1. 再验本机SHA，明确授权后在服务器独占创建0700非符号链接暂存目录（已存在即停止），上传本单image.tar及已复核脚本；远端验SHA、平台、源码与二进制哈希后加载。
2. 非阻塞按原顺序取得full-stack、auth-acceptance、ai-gateway-go三把/var/lock发布锁；任一失败退出。当前容器ID/镜像/标签及运行参数漂移则停止；ROOT_BOOTSTRAP_扫描失败或存在该键则停止。
3. 必要配置仅在服务器本地内存及0600原子快照中保留，通过Unix Docker API JSON复制；不输出Env、密码、完整inspect或连接串。创建不启动的候选，逐字段核对Config/HostConfig；只有已实测的OomKillDisable null→false默认规范化允许等价，true仍拒绝。
4. 在停旧前原子落盘身份manifest。停止精确旧ID、改名保留，候选接管原名后启动；每次源站与公网health均须严格200，源站Host为aiportcloud.com，每次连接2秒/总3秒、总体90秒。前端入口和JS哈希仍须匹配158a00e。
5. 切换失败或健康失败按精确身份独立尝试候选清理和旧容器恢复，保留所有失败结果；未知对象不删除。旧镜像及更早d2de587回滚对象不删除。不运行数据库迁移、前端发布或main-reset脚本。
6. 成功后清理私有快照，记录新旧ID和健康证据。信号恢复同上一版；SIGKILL/主机故障仍需持锁核验manifest后恢复，不能把本地mock当生产回滚演练。

本地门禁：全量345 pass/race113 pass、0测试fail/skip、vet/build与独立规格/质量PASS；独立22组新旧输出一致；本脚本目标替换后的7项mock行为测试记录另见验证报告。实际线上read-only核对04ed728容器仍running，loopback8000/porsche-app/restart=no/无mount。

复测：新候选运行来源和诊断字段确认后，才执行前端validation/m3-sse-diagnostic-attempt3.cjs。该脚本要求预算恰好有2次历史记录，发出前登记第3次，仅一次gpt-5.4-nano/max_tokens32、无自动重放。新日志读取白名单须包含malformed_chunk_detail，两个子字段再限定固定枚举；不得输出原始上游帧。结果无论成功失败都记录usage、清理测试会话、注销并核对request ID哈希。最后预算用尽不得继续生成，M3只按实际结果双方签收。
