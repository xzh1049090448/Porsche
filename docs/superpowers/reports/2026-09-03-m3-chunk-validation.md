# M3 chunk 细分诊断验证记录

当前范围：本地补充诊断分类；线上04ed728仍返回503，最后1次×max_tokens32预算未动。本报告不代表新候选已发布或M3已通过。

## 实现与边界

5个代码/测试文件：internal/diagnostics/trace.go增加可省略的malformed_chunk_detail；chunk.go/chunk_test.go提供封闭reason/field枚举及入口白名单；internal/whitelabel/sse.go与sse_diagnostics_detail_test.go按原拒绝分支返回细分原因。旧helper签名及errMalformedCompletion哨兵、大类malformed_chunk和公开503保持。

固定原因：unknown、json_syntax、json_type、missing_required、invalid_value、negative_value、invalid_shape。字段只限chunk/id/object/created/usage/choices及预定义delta/tool/function路径，不输出动态索引或任意字段名。json.UnmarshalTypeError.Field仅作为固定字典查询输入，错误文本和Value不落日志；未知值二次回落unknown。已有省略或null的接受行为不作额外收紧，无trace调用没有新增日志。

## 执行证据

- 基础init.sh通过：/private/tmp/porsche-m3-chunk-init.txt。未注入TEST_*时DB用例按规则跳过，此结果不当作全量集成验证。
- RED：/private/tmp/porsche-m3-chunk-red.txt，合成拒绝帧诊断detail缺失。GREEN：/private/tmp/porsche-m3-chunk-green.jsonl，52个测试通过事件，0fail/skip。
- 实施者17组正常SSE输出摘要，旧HEAD sse overlay与新代码一致：/private/tmp/porsche-m3-chunk-compat-old.txt、/private/tmp/porsche-m3-chunk-compat-new.txt。覆盖普通内容、created缺省、null/usage-only/empty usage、delta与工具增量、CRLF多行data及DONE。
- 协调者初次全量与实现者新增test import重叠，whitelabel编译报could not import crypto/sha256 (open : no such file or directory)，退出1，原日志/private/tmp/porsche-m3-chunk-full.log保留。未当作业务回归结论；严格冻结后重新执行。
- 冻结版实际命令：python3 /private/tmp/porsche-m3-chunk-run-checks.py full，即显式独占TEST_*环境下go test -json -p 1 ./... -count=1。结果exit0，345个含子测试pass、0fail/skip；日志/private/tmp/porsche-m3-chunk-full-final.log。计数仅含带Test字段的测试事件，无测试文件的包不算测试skip。
- 冻结版race：go test -race -json ./internal/diagnostics ./internal/whitelabel -count=1，113个测试pass、0fail/skip；/private/tmp/porsche-m3-chunk-race-final.log。go vet ./...与go build ./...退出0；/private/tmp/porsche-m3-chunk-vet-final.log、porsche-m3-chunk-build-final.log。git diff --check通过。

数据库环境为本次独占MySQL8.0及Redis7容器，tmpfs存储、仅回环随机端口；测试凭据仅本机0600文件注入，未使用生产.env或数据库。清理结果另行追加。

## 规格审查

后端PM读取实际5文件与设计，git diff --check通过，限定本地规格PASS：原校验短路优先级、旧哨兵、公开响应保持；细分来自实际分支，固定字段映射和日志二次白名单有效。无trace没有新增日志，首个非法chunk立即终止，记录有界。此审查未独立运行DB/线上，不授权新候选部署或签M3。

## 独立质量、镜像及发布材料

独立质量以3a35301为旧基线，同一corpus的22组新旧输出逐字节一致，涵盖正常、usage-only、工具调用、CRLF/分帧/EOF、缺省/null/重复key及多种错误类型。基线和当前分别go overlay执行，结果/private/tmp/porsche-m3-projection-baseline.out、porsche-m3-projection-current.out。独立-race对抗探针验证敏感类型错误只输出固定json_type/id、多错误优先级、未知reason/field并发归一为unknown、无trace包装兼容，实际输出ok github.com/porsche/ai-gateway-go/internal/whitelabel 1.545s，日志/private/tmp/porsche-m3-chunk-adversarial.out。质量限定本地PASS，不替代真实M3。

代码候选6e70784e182a4bae1e5b8399053b45429e8e4400已提交。独立精确Git clone、Go -trimpath -buildvcs=true、CGO_ENABLED=0/GOOS=linux/GOARCH=amd64构建，server与bootstrap-root均嵌入该SHA且vcs.modified=false。离线基础层、--network=none/--pull=false组装amd64镜像，导出重载后的精确镜像ID sha256:cb42daed3581c51a5e6f5ac5a237bec4b4e0c3540afb61d36acb27592dc2947f。镜像包/private/tmp/porsche-m3-chunk-6e70784.image.tar，SHA256 195a6f3870e64d79d6aa1f9dbe994baea230ae49c7ac5b92ec8711ec14b8fc2f。镜像内二进制哈希及CA核验通过；无凭据启动预期exit1缺JIEKOU_API_KEY，不称真实健康通过。详见validation/m3-chunk-diagnostic-candidate.json。

已准备新候选发布脚本和7场景行为回归，另从独立manifest断言OLD/OLDIMAGE/IMAGE/REV/归档SHA/二进制SHA一一对应。草稿字符串替换曾误将OLDIMAGE设为新镜像，人工核对发现并在任何远程使用前纠正；最终旧镜像为a69cfdab，新镜像为cb42daed，身份断言与7行为场景均通过。脚本尚未上传或在服务器执行。

后端PM只读复核最终发布单、manifest与脚本，书面确认材料足够提交针对该精确候选的用户授权，旧容器9425ea及镜像a69和旧标签均正确；不代表已上传、部署或健康/M3通过。发布单docs/superpowers/plans/2026-09-03-m3-chunk-release.md。

本轮临时MySQL/Redis按精确ID与标签核对后停止并自动移除，临时测试凭据文件删除；证据validation/m3-chunk-cleanup.json。没有删除生产卷或原有容器。后续重跑集成测试须重新提供独立fixture，不能使用已清理的test-env.json。
