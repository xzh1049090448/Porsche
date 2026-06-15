package service

import (
	"errors"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/rag"
	"gorm.io/gorm"
)

var defaultDatasets = []struct {
	Name        string
	Slug        string
	Category    models.DatasetCategory
	Description string
	AccessPlans []string
	SampleDocs  []string
}{
	{
		Name: "商品知识库", Slug: "product-knowledge", Category: models.CategoryProductKnowledge,
		Description: "包含亚马逊/跨境商品标题、参数、属性等标准化数据",
		AccessPlans: []string{"free", "professional", "enterprise"},
		SampleDocs: []string{
			"亚马逊商品标题规范：标题应包含品牌名、产品类型、关键属性，长度不超过200字符，禁止使用全大写和特殊符号堆砌。",
			"跨境商品参数描述：尺寸、重量、材质、颜色等属性需使用标准单位，支持多语言本地化。",
		},
	},
	{
		Name: "客服问答语料", Slug: "customer-service", Category: models.CategoryCustomerService,
		Description: "包含跨境物流、售后、退换货等标准化客服话术",
		AccessPlans: []string{"free", "professional", "enterprise"},
		SampleDocs: []string{
			"跨境物流时效说明：标准海运15-30个工作日，空运5-10个工作日，具体以物流商追踪信息为准。",
			"退换货政策：收到商品7天内，未使用且包装完好可申请退货，质量问题由卖家承担退货运费。",
		},
	},
	{
		Name: "平台规则库", Slug: "platform-rules", Category: models.CategoryPlatformRules,
		Description: "包含亚马逊、Shopee等平台最新合规规则",
		AccessPlans: []string{"professional", "enterprise"},
		SampleDocs: []string{
			"亚马逊禁售品类：武器、危险品、侵权商品、仿冒品等严禁在平台销售。",
		},
	},
	{
		Name: "评论舆情库", Slug: "review-sentiment", Category: models.CategoryReviewSentiment,
		Description: "包含跨境商品评论、用户反馈、情感标签数据",
		AccessPlans: []string{"professional", "enterprise"},
		SampleDocs: []string{
			"正面评论特征：物流快、质量好、与描述一致、客服响应及时。",
		},
	},
}

func SeedDefaultDatasets(db *gorm.DB, engine *rag.Engine) error {
	for _, item := range defaultDatasets {
		var existing models.Dataset
		err := db.Where("slug = ?", item.Slug).First(&existing).Error
		if err == nil {
			continue
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		desc := item.Description
		asset := "CBEC-" + item.Slug
		ds := models.Dataset{
			Name:             item.Name,
			Slug:             item.Slug,
			Category:         item.Category,
			Description:      &desc,
			Status:           models.DatasetActive,
			CurrentVersion:   "1.0.0",
			VectorStatus:     models.VectorReady,
			AccessPlans:      item.AccessPlans,
			AssetID:          &asset,
			ComplianceReport: models.JSONMap{"status": "passed", "source": "seed"},
		}
		if err := db.Create(&ds).Error; err != nil {
			return err
		}
		engine.IndexDocuments(int(ds.ID), item.SampleDocs)
	}
	return nil
}
