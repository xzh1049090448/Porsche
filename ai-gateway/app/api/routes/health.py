"""健康检查接口。

前缀: ``/health``（无版本前缀）

用于负载均衡、容器探活；无需鉴权。
"""

from __future__ import annotations

from fastapi import APIRouter, Depends

from app.api.deps import get_state
from app.state import AppState

router = APIRouter(tags=["health"])


@router.get("/health")
async def health(state: AppState = Depends(get_state)) -> dict:
    """服务存活探测。

    - 生产环境仅返回 ``{"status": "ok"}``
    - 非生产环境额外返回已加载模型数、客户端数
    """
    if state.settings.app_env == "production":
        return {"status": "ok"}
    return {
        "status": "ok",
        "models_loaded": len(state.models.routes),
        "clients_loaded": state.clients.client_count,
    }
