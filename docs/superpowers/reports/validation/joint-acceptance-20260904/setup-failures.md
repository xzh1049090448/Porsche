# 联合环境设置失败记录

1. loopback端口探针在默认sandbox内返回`PermissionError: Operation not permitted`；命令在编译/迁移前停止。之后按已授权本地环境提升权限重跑。
2. 首次Root bootstrap返回`UPSTREAM_REGION must be cn or global`；0001–0004迁移已完成，bootstrap未执行。私密env补`UPSTREAM_REGION=cn`后继续。
3. 第二次bootstrap返回`invalid Root credentials file`；检查只输出字段名、长度和空白状态，发现生成脚本多写一个空行。仅删除空行后bootstrap成功，凭据值未输出。
4. Backend首次health连续403，响应固定`Forbidden`；定位为默认`ALLOWED_HOSTS=aiportcloud.com`。停止本地进程，在私密env加入`127.0.0.1,localhost`后以同一二进制重启，health200。
5. 首次列表smoke被zsh拒绝：`no matches found: http://127.0.0.1:15173/admin/v2/users?page_size=20`。请求未发送；仅为URL加引号后列表200。
