"""Seed default datasets and platform client on startup."""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.repository.models import Dataset, DatasetCategory, DatasetStatus, VectorStatus
from app.service.rag_engine import RagEngine


DEFAULT_DATASETS = [
  {
    "name": "商品知识库",
    "slug": "product-knowledge",
    "category": DatasetCategory.PRODUCT_KNOWLEDGE,
    "description": "包含亚马逊/跨境商品标题、参数、属性等标准化数据",
    "access_plans": ["free", "professional", "enterprise"],
    "sample_docs": [
      "亚马逊商品标题规范：标题应包含品牌名、产品类型、关键属性，长度不超过200字符，禁止使用全大写和特殊符号堆砌。",
      "跨境商品参数描述：尺寸、重量、材质、颜色等属性需使用标准单位，支持多语言本地化。",
      "FBA商品Listing优化：主图需白底，副图展示使用场景，Bullet Points突出核心卖点。",
    ],
  },
  {
    "name": "客服问答语料",
    "slug": "customer-service",
    "category": DatasetCategory.CUSTOMER_SERVICE,
    "description": "包含跨境物流、售后、退换货等标准化客服话术",
    "access_plans": ["free", "professional", "enterprise"],
    "sample_docs": [
      "跨境物流时效说明：标准海运15-30个工作日，空运5-10个工作日，具体以物流商追踪信息为准。",
      "退换货政策：收到商品7天内，未使用且包装完好可申请退货，质量问题由卖家承担退货运费。",
      "关税说明：跨境商品可能产生进口关税，具体税率因目的国和商品类别而异，建议买家提前了解。",
    ],
  },
  {
    "name": "平台规则库",
    "slug": "platform-rules",
    "category": DatasetCategory.PLATFORM_RULES,
    "description": "包含亚马逊、Shopee等平台最新合规规则",
    "access_plans": ["professional", "enterprise"],
    "sample_docs": [
      "亚马逊禁售品类：武器、危险品、侵权商品、仿冒品等严禁在平台销售。",
      "Shopee商品发布规则：禁止虚假宣传、价格欺诈，商品描述需与实际一致。",
      "亚马逊Review政策：禁止刷单、索评、操纵评价，违规将导致账号封禁。",
    ],
  },
  {
    "name": "评论舆情库",
    "slug": "review-sentiment",
    "category": DatasetCategory.REVIEW_SENTIMENT,
    "description": "包含跨境商品评论、用户反馈、情感标签数据",
    "access_plans": ["professional", "enterprise"],
    "sample_docs": [
      "正面评论特征：物流快、质量好、与描述一致、客服响应及时。",
      "负面评论处理：产品质量问题需48小时内响应，提供退款或换货方案。",
      "情感分析标签：positive/neutral/negative，用于监控商品口碑趋势。",
    ],
  },
]


async def seed_default_datasets(db: AsyncSession, rag: RagEngine) -> None:
  for item in DEFAULT_DATASETS:
    existing = await db.scalar(select(Dataset).where(Dataset.slug == item["slug"]))
    if existing:
      continue
    ds = Dataset(
      name=item["name"],
      slug=item["slug"],
      category=item["category"],
      description=item["description"],
      status=DatasetStatus.ACTIVE,
      current_version="1.0.0",
      vector_status=VectorStatus.READY,
      access_plans=item["access_plans"],
      asset_id=f"CBEC-{item['slug'].upper()}",
      compliance_report={"status": "passed", "source": "seed"},
    )
    db.add(ds)
    await db.flush()
    rag.index_documents(ds.id, item["sample_docs"])
