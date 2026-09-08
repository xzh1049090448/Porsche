package router_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/router"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func TestRouteInventoryRemainsPreB1E(t *testing.T) {
	engine := router.New(&app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}})
	got := make([]routeContract, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		got = append(got, routeContract{Method: route.Method, Path: route.Path})
	}
	sortRouteContracts(got)

	want := append([]routeContract(nil), preB1ERouteInventory...)
	sortRouteContracts(want)
	if !slices.Equal(got, want) {
		t.Fatalf("route inventory mismatch\n got (%d): %#v\nwant (%d): %#v", len(got), got, len(want), want)
	}
	for _, route := range got {
		if route.Path == "/admin/v2/action-verifications" || route.Path == "/admin/v2/operations" {
			t.Fatalf("inactive admin action route was registered: %s %s", route.Method, route.Path)
		}
	}
}

func completeRouterUserManagementActions() *service.UserManagementActions {
	return &service.UserManagementActions{
		Verifications: &service.ActionVerificationService{}, Operations: &service.ActionOperationService{},
		DeleteOutbox: &service.AdminActionOutboxWriter{}, CreateOutbox: &service.CreateAccountOutboxWriter{},
		ResetOutbox:        &service.ResetPasswordOutboxWriter{},
		NewDeleteExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) { return nil, nil },
		NewCreateExecution: func(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
			return nil, nil
		},
		NewResetExecution: func(actionsecurity.ResetPasswordIntent, []byte, service.ResetPasswordRequestMetadata) (*service.ResetPasswordExecution, error) {
			return nil, nil
		},
	}
}

func TestUserManagementActionRouteInventoryIsExactAndBundleGated(t *testing.T) {
	complete := completeRouterUserManagementActions()
	state := &app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}, UserManagementActions: complete}
	engine := router.New(state)
	want := []routeContract{
		{http.MethodPost, "/admin/v2/action-verifications"},
		{http.MethodPost, "/admin/v2/users"},
		{http.MethodPost, "/admin/v2/users/:guid/actions"},
		{http.MethodGet, "/admin/v2/operations"},
	}
	for _, expected := range want {
		if !slices.ContainsFunc(engine.Routes(), func(route gin.RouteInfo) bool { return route.Method == expected.Method && route.Path == expected.Path }) {
			t.Fatalf("missing action route %s %s", expected.Method, expected.Path)
		}
	}
	for _, forbidden := range []string{"/admin/v2/actions", "/admin/v2/actions/:action", "/admin/v2/users/:guid/actions/:action"} {
		if slices.ContainsFunc(engine.Routes(), func(route gin.RouteInfo) bool { return route.Path == forbidden }) {
			t.Fatalf("generic action route registered: %s", forbidden)
		}
	}

	partials := []*service.UserManagementActions{
		{},
		{Verifications: complete.Verifications, Operations: complete.Operations, DeleteOutbox: complete.DeleteOutbox, CreateOutbox: complete.CreateOutbox, NewDeleteExecution: complete.NewDeleteExecution},
		{Verifications: complete.Verifications, Operations: complete.Operations, DeleteOutbox: complete.DeleteOutbox, CreateOutbox: complete.CreateOutbox, NewCreateExecution: complete.NewCreateExecution},
	}
	for index, partial := range partials {
		partialEngine := router.New(&app.State{Settings: state.Settings, UserManagementActions: partial})
		for _, expected := range want {
			if slices.ContainsFunc(partialEngine.Routes(), func(route gin.RouteInfo) bool { return route.Method == expected.Method && route.Path == expected.Path }) {
				t.Fatalf("partial bundle %d registered %s %s", index, expected.Method, expected.Path)
			}
		}
	}
}

func TestAdminUserCreateRouteInventoryUsesOneCompleteBundleWithoutDuplicateOwnership(t *testing.T) {
	complete := completeRouterUserManagementActions()
	settings := &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}
	engine := router.New(&app.State{Settings: settings, UserManagementActions: complete})
	want := []routeContract{
		{http.MethodPost, "/admin/v2/action-verifications"},
		{http.MethodPost, "/admin/v2/users"},
		{http.MethodPost, "/admin/v2/users/:guid/actions"},
		{http.MethodGet, "/admin/v2/operations"},
	}
	for _, expected := range want {
		count := 0
		for _, route := range engine.Routes() {
			if route.Method == expected.Method && route.Path == expected.Path {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("route %s %s count=%d want=1", expected.Method, expected.Path, count)
		}
	}
	getCount, legacyCount := 0, 0
	for _, route := range engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/admin/v2/users" {
			getCount++
		}
		if route.Path == "/admin/users" {
			legacyCount++
		}
	}
	if getCount != 1 || legacyCount != 1 {
		t.Fatalf("existing GET/legacy collection routes changed: get=%d legacy=%d", getCount, legacyCount)
	}

	partials := []*service.UserManagementActions{{}, {
		Verifications: complete.Verifications, Operations: complete.Operations, DeleteOutbox: complete.DeleteOutbox,
		CreateOutbox: complete.CreateOutbox, NewDeleteExecution: complete.NewDeleteExecution,
	}}
	for index, partial := range partials {
		partialEngine := router.New(&app.State{Settings: settings, UserManagementActions: partial})
		for _, expected := range want {
			if slices.ContainsFunc(partialEngine.Routes(), func(route gin.RouteInfo) bool { return route.Method == expected.Method && route.Path == expected.Path }) {
				t.Fatalf("partial bundle %d registered %s %s", index, expected.Method, expected.Path)
			}
		}
	}
}

func TestUserDeleteActionRoutesKeepAuthenticationAndSecurityHeaders(t *testing.T) {
	state := &app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}, UserManagementActions: completeRouterUserManagementActions()}
	engine := router.New(state)
	for caseIndex, route := range []routeContract{{http.MethodPost, "/admin/v2/action-verifications"}, {http.MethodPost, "/admin/v2/users"}, {http.MethodPost, "/admin/v2/users/123/actions"}, {http.MethodGet, "/admin/v2/operations?scope=users.delete"}} {
		req := httptest.NewRequest(route.Method, route.Path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("authenticated route case=%d status=%d cache_match=%t request_id_present=%t body_length=%d", caseIndex, rec.Code, rec.Header().Get("Cache-Control") == "no-store", rec.Header().Get("X-Request-ID") != "", rec.Body.Len())
		}
	}
}

func TestCompleteUserManagementBundleLeavesGenericAndOtherActionPaths404(t *testing.T) {
	state := &app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}, UserManagementActions: completeRouterUserManagementActions()}
	engine := router.New(state)
	for caseIndex, route := range []routeContract{
		{http.MethodPost, "/admin/v2/actions"},
		{http.MethodPost, "/admin/v2/actions/users.delete"},
		{http.MethodPost, "/admin/v2/users/123/actions/promote"},
		{http.MethodPost, "/admin/v2/public-content/announcements/publish"},
		{http.MethodPost, "/admin/v2/public-content/announcements/rollback"},
	} {
		req := httptest.NewRequest(route.Method, route.Path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer syntactically-valid-test-token")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("generic route case=%d status=%d body_length=%d", caseIndex, rec.Code, rec.Body.Len())
		}
	}
}

func TestAdminActionInactiveRoutesReturn404BeforeAuthentication(t *testing.T) {
	engine := router.New(&app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}})
	tests := []routeContract{
		{http.MethodPost, "/admin/v2/action-verifications"},
		{http.MethodGet, "/admin/v2/operations?scope=users.delete"},
		{http.MethodPost, "/admin/v2/users"},
		{http.MethodPost, "/admin/v2/users/123/actions"},
		{http.MethodPost, "/admin/v2/public-content/announcements/publish"},
		{http.MethodPost, "/admin/v2/public-content/announcements/rollback"},
	}
	for caseIndex, test := range tests {
		for authIndex, authorization := range []string{"", "Bearer syntactically-valid-test-token"} {
			t.Run(fmt.Sprintf("case-%d-auth-%d", caseIndex, authIndex), func(t *testing.T) {
				req := httptest.NewRequest(test.Method, test.Path, strings.NewReader(`{}`))
				if authorization != "" {
					req.Header.Set("Authorization", authorization)
				}
				rec := httptest.NewRecorder()
				engine.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("unregistered route status=%d want_status=%d body_length=%d", rec.Code, http.StatusNotFound, rec.Body.Len())
				}
			})
		}
	}
}

func sortRouteContracts(routes []routeContract) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})
}
