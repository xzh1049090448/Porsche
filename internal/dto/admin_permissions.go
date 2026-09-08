package dto

import "github.com/porsche/ai-gateway-go/internal/service"

// These explicit display DTOs contain no internal account/session IDs, audit fields,
// or evaluator. Service projections whitelist the exact public contract.
type AdminPermissionCatalog = service.AdminPermissionCatalog
type AdminPermissionDetail = service.AdminPermissionDetail
