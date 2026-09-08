# M3 object诊断候选发布单

状态：本地候选ad3f5b4已构建并完成限定验证，尚未上传或部署。用户已增加1次调用额度；新增候选的具体部署授权仍待取得。本单不消耗调用预算。

| 项目 | 精确对象 |
| --- | --- |
| 目标 | overseaCloud / 172.245.142.21 / ai-gateway-go |
| 新源码 | ad3f5b4416854353ebb2b3647ae4b2a809b4a05c |
| 新镜像 | sha256:2bc6b866911f439545bd3128bf8af24e640b3922706c024651d3a7d2d15096d0 |
| 本机镜像包 | /private/tmp/porsche-m3-object-ad3f5b4.image.tar |
| 归档SHA256 | 8154e46b93466f13018b8d310a4caf361bc40989cfd5e603e74ffe9ca94eb280 |
| 当前及回滚容器 | 13ada4aa4f1e4460265d778f3447957ea854b234b542e509ffcf6668ef7b3031 |
| 当前及回滚镜像 | sha256:cb42daed3581c51a5e6f5ac5a237bec4b4e0c3540afb61d36acb27592dc2947f |
| 当前源码 | 6e70784e182a4bae1e5b8399053b45429e8e4400 |
| 计划私有暂存目录 | /var/tmp/porsche-m3-object-release-ad3f5b4-20260903 |

07:34:15Z只读核对当前13ada4aa/cb42运行、两个更早9425ea/d2de587回滚容器停止保留、源站与公网health200、当前标签6e70784/m3-chunk-diagnostic-candidate准确。此为读取时快照；真正执行仍重新核对，漂移则停止。

本地隔离clone构建amd64，源码revision和vcs.modified=false、二进制哈希及CA已核验。server哈希c0fb79a40874a2d3067d2733e6b9e17b0d5e3884c50db7ebbf0de296e7af71c1；bootstrap-root哈希3fdf6868151c180cb412c10580c466fe51a14a027941ff4d4b2bdc30107ed15e。helper不执行Root引导；运行镜像不包含源码归档或生产配置。无网络无凭据启动因缺JIEKOU_API_KEY退出1符合门禁，不代表生产运行健康。

## 已准备的执行步骤

执行脚本：../reports/validation/m3-object-release-prepared.py；SHA256 f98fdd92e10fca06e600e50283e2c19eca73cfd4850d22f17dc65d41c5b0c0e1。先复核本机归档SHA及脚本SHA；取得本单准确新候选授权后，服务器独占创建0700目录并上传image.tar/release.py，收紧0600并再次验SHA。

脚本固定绑定新旧身份：取得full-stack、auth-acceptance、ai-gateway-go三锁；验证旧容器、镜像、标签、ROOT_BOOTSTRAP_不存在、loopback8000/porsche-app/restart=no/无挂载、基线健康和前端158a00e哈希。私密Config/HostConfig仅在服务器0600快照及内存JSON中使用，不输出Env或完整inspect；创建未启动候选并逐字段比较，唯一允许的既有规范化仍为OomKillDisable null→false。

停止前写身份manifest；旧13ada4aa按精确ID停止改名保留，新候选接管ai-gateway-go。检查源站/public严格200、前端入口index-i7ZWPv9J.js和SHA7c133cb1197c701cc6288d2718384c92f8b4b24a15fd13d821f3930d8bf09033未变、候选配置一致。失败独立尝试候选清理/旧容器恢复，未知身份不删除，所有回滚失败必须报告。两更早回滚容器、全部旧镜像、DB/Redis/前端/Nginx均不动。

成功后删除私密快照并归档release-result.json，确认旧容器停止保留。此为发布准备，7项mock行为通过不能声称演练生产回滚；SIGKILL/主机故障仍须持锁核实manifest人工恢复。不得原样运行旧chunk发布脚本或main重置/全栈发布入口。

## 第4次请求准入

新候选部署健康、来源及字段提取器均确认后，将真实release-result.json安全保存到/private/tmp/porsche-m3-object-release-result.json。前端validation/m3-sse-diagnostic-attempt4.cjs已绑定完整源码ad3f5b4416854353ebb2b3647ae4b2a809b4a05c，要求预算maximum4且已有3条记录；独占创建结果文件，避免同脚本重复执行或覆盖旧证据。只允许1POST、gpt-5.4-nano/max_tokens32/stream=true；发送前登记第4次。不得自动重放或换模型。

错误处理仅固定状态/类别，不记录Playwright原始调用错误文本、上游detail或原始帧；结果包括usage、事件顺序、清理测试对话和注销。日志提取器m3-object-log-extract.py接受精确容器/请求哈希/源码/镜像，必须唯一匹配，object_detail三字段再次白名单限制。

模型调用预算已授权，部署具体候选尚待授权；执行后无论请求成败预算归零，不再自行追加。若decoded_kind=other或重复键仍不足以决定兼容方案，如实记录，不承诺单次必能修复。
