"""管理端数据集管理接口。

前缀: ``/admin/datasets``

需 Admin Token 鉴权；支持上传、向量化、版本回滚与下线。
"""

from __future__ import annotations

import re
from pathlib import Path
from typing import Annotated

from fastapi import APIRouter, Depends, File, HTTPException, UploadFile
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_state
from app.api.routes.admin import verify_admin
from app.config import get_settings
from app.repository.models import (
    Dataset,
    DatasetCategory,
    DatasetStatus,
    DatasetVersion,
    VectorStatus,
)
from app.repository.session import get_db
from app.common.schemas.dataset import (
    DatasetCreateRequest,
    DatasetProcessResponse,
    DatasetResponse,
    DatasetVersionResponse,
)
from app.service.dataset_processor import DatasetProcessor
from app.state import AppState

router = APIRouter(prefix="/admin/datasets", tags=["admin-datasets"], dependencies=[Depends(verify_admin)])

_VERSION_RE = re.compile(r"^[\w][\w.-]{0,31}$")
_ALLOWED_SUFFIXES = frozenset({".jsonl", ".csv", ".parquet"})


@router.get("", response_model=list[DatasetResponse])
async def admin_list_datasets(db: Annotated[AsyncSession, Depends(get_db)]):
    """获取全部数据集（含草稿、处理中、已下线）。"""
    rows = await db.scalars(select(Dataset).order_by(Dataset.id))
    return [DatasetResponse.model_validate(d) for d in rows.all()]


@router.post("", response_model=DatasetResponse)
async def create_dataset(
    body: DatasetCreateRequest,
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """创建数据集元数据（初始状态为 ``draft``，需上传文件后激活）。"""
    try:
        category = DatasetCategory(body.category)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail="无效的数据集分类") from exc
    existing = await db.scalar(select(Dataset).where(Dataset.slug == body.slug))
    if existing:
        raise HTTPException(status_code=409, detail="slug 已存在")
    ds = Dataset(
        name=body.name,
        slug=body.slug,
        category=category,
        description=body.description,
        access_plans=body.access_plans or ["free", "professional", "enterprise"],
        asset_id=body.asset_id,
        status=DatasetStatus.DRAFT,
    )
    db.add(ds)
    await db.flush()
    return ds


@router.post("/{dataset_id}/upload", response_model=DatasetProcessResponse)
async def upload_dataset(
    dataset_id: int,
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
    file: UploadFile = File(...),
    version: str = "1.0.0",
):
    """上传数据集文件并触发处理与向量化。

    - 支持 ``.jsonl`` / ``.csv`` / ``.parquet``
    - 解析、合规检查、写入 Chroma 向量库
    - 成功后数据集状态变为 ``active``
    """
    ds = await db.get(Dataset, dataset_id)
    if not ds:
        raise HTTPException(status_code=404, detail="数据集不存在")
    if not _VERSION_RE.match(version):
        raise HTTPException(status_code=400, detail="版本号格式无效")

    settings = get_settings()
    suffix = Path(file.filename or "data.jsonl").suffix.lower()
    if suffix not in _ALLOWED_SUFFIXES:
        raise HTTPException(status_code=400, detail="仅支持 .jsonl / .csv / .parquet 文件")

    upload_dir = Path(settings.dataset_upload_dir) / str(dataset_id)
    upload_dir.mkdir(parents=True, exist_ok=True)
    file_path = upload_dir / f"v{version}{suffix}"

    max_bytes = settings.dataset_upload_max_bytes
    written = 0
    with open(file_path, "wb") as f:
        while chunk := await file.read(1024 * 1024):
            written += len(chunk)
            if written > max_bytes:
                file_path.unlink(missing_ok=True)
                raise HTTPException(status_code=413, detail="文件超过大小限制")
            f.write(chunk)

    ds.status = DatasetStatus.PROCESSING
    ds.vector_status = VectorStatus.PROCESSING

    try:
        result = DatasetProcessor.process_file(file_path)
        ds_version = DatasetVersion(
            dataset_id=dataset_id,
            version=version,
            file_path=str(file_path),
            token_count=result["token_count"],
            record_count=result["record_count"],
            compliance_report=result["compliance_report"],
            is_active=True,
        )
        db.add(ds_version)
        await db.flush()

        for v in await db.scalars(
            select(DatasetVersion).where(
                DatasetVersion.dataset_id == dataset_id, DatasetVersion.id != ds_version.id
            )
        ):
            v.is_active = False

        chunk_count = state.rag.index_documents(dataset_id, result["documents"])
        ds.token_count = result["token_count"]
        ds.current_version = version
        ds.status = DatasetStatus.ACTIVE
        ds.vector_status = VectorStatus.READY
        ds.compliance_report = result["compliance_report"]
        return DatasetProcessResponse(
            dataset_id=dataset_id,
            status="ready",
            message=f"处理完成：{result['record_count']} 条记录，{chunk_count} 个向量块",
        )
    except Exception as exc:
        ds.vector_status = VectorStatus.FAILED
        raise HTTPException(status_code=400, detail=f"处理失败: {exc}") from exc


@router.post("/{dataset_id}/rollback/{version}", response_model=DatasetResponse)
async def rollback_dataset(
    dataset_id: int,
    version: str,
    state: Annotated[AppState, Depends(get_state)],
    db: Annotated[AsyncSession, Depends(get_db)],
):
    """回滚数据集到指定历史版本（重新索引向量库）。"""
    ds = await db.get(Dataset, dataset_id)
    if not ds:
        raise HTTPException(status_code=404, detail="数据集不存在")
    ds_version = await db.scalar(
        select(DatasetVersion).where(
            DatasetVersion.dataset_id == dataset_id, DatasetVersion.version == version
        )
    )
    if not ds_version or not ds_version.file_path:
        raise HTTPException(status_code=404, detail="版本不存在")

    for v in await db.scalars(select(DatasetVersion).where(DatasetVersion.dataset_id == dataset_id)):
        v.is_active = v.version == version

    result = DatasetProcessor.process_file(Path(ds_version.file_path))
    state.rag.delete_collection(dataset_id)
    state.rag.index_documents(dataset_id, result["documents"])
    ds.current_version = version
    ds.token_count = ds_version.token_count
    return ds


@router.get("/{dataset_id}/versions", response_model=list[DatasetVersionResponse])
async def list_versions(dataset_id: int, db: Annotated[AsyncSession, Depends(get_db)]):
    """获取数据集全部版本记录。"""
    rows = await db.scalars(
        select(DatasetVersion)
        .where(DatasetVersion.dataset_id == dataset_id)
        .order_by(DatasetVersion.created_at.desc())
    )
    return [DatasetVersionResponse.model_validate(v) for v in rows.all()]


@router.put("/{dataset_id}/offline")
async def offline_dataset(dataset_id: int, db: Annotated[AsyncSession, Depends(get_db)]):
    """下线数据集（用户侧不再可见）。"""
    ds = await db.get(Dataset, dataset_id)
    if not ds:
        raise HTTPException(status_code=404, detail="数据集不存在")
    ds.status = DatasetStatus.OFFLINE
    return {"message": "数据集已下线"}
