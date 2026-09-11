package dto

import "github.com/porsche/ai-gateway-go/internal/service"

type Notification = service.RootAlertView
type NotificationListRequest = service.RootAlertListQuery
type NotificationListResponse = service.RootAlertList
type UnreadCountResponse = service.RootAlertUnreadCount
