package router_test

import (
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

func TestUserDeleteActionRouteInventoryIsExactAndBundleGated(t *testing.T) {
	complete := &service.UserDeleteActions{
		Verifications: &service.ActionVerificationService{},
		Operations:    &service.ActionOperationService{},
		Outbox:        &service.AdminActionOutboxWriter{},
		NewExecution: func(serviceIntent actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
			return nil, nil
		},
	}
	state := &app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}, UserDeleteActions: complete}
	engine := router.New(state)
	want := []routeContract{
		{http.MethodPost, "/admin/v2/action-verifications"},
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

	partials := []*service.UserDeleteActions{
		{},
		{Verifications: complete.Verifications, Operations: complete.Operations, Outbox: complete.Outbox},
		{Verifications: complete.Verifications, Operations: complete.Operations, NewExecution: complete.NewExecution},
	}
	for index, partial := range partials {
		partialEngine := router.New(&app.State{Settings: state.Settings, UserDeleteActions: partial})
		for _, expected := range want {
			if slices.ContainsFunc(partialEngine.Routes(), func(route gin.RouteInfo) bool { return route.Method == expected.Method && route.Path == expected.Path }) {
				t.Fatalf("partial bundle %d registered %s %s", index, expected.Method, expected.Path)
			}
		}
	}
}

func TestUserDeleteActionRoutesKeepAuthenticationAndSecurityHeaders(t *testing.T) {
	state := &app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}, UserDeleteActions: &service.UserDeleteActions{
		Verifications: &service.ActionVerificationService{}, Operations: &service.ActionOperationService{}, Outbox: &service.AdminActionOutboxWriter{},
		NewExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) { return nil, nil },
	}}
	engine := router.New(state)
	for _, route := range []routeContract{{http.MethodPost, "/admin/v2/action-verifications"}, {http.MethodPost, "/admin/v2/users/123/actions"}, {http.MethodGet, "/admin/v2/operations?scope=users.delete"}} {
		req := httptest.NewRequest(route.Method, route.Path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
			t.Fatalf("%s %s status/headers=%d %v", route.Method, route.Path, rec.Code, rec.Header())
		}
	}
}

func TestCompleteUserDeleteBundleLeavesGenericAndOtherActionPaths404(t *testing.T) {
	state := &app.State{Settings: &config.Settings{AppEnv: "test", AllowedHosts: "example.com"}, UserDeleteActions: &service.UserDeleteActions{
		Verifications: &service.ActionVerificationService{}, Operations: &service.ActionOperationService{}, Outbox: &service.AdminActionOutboxWriter{},
		NewExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) { return nil, nil },
	}}
	engine := router.New(state)
	for _, route := range []routeContract{
		{http.MethodPost, "/admin/v2/actions"},
		{http.MethodPost, "/admin/v2/actions/users.delete"},
		{http.MethodPost, "/admin/v2/users"},
		{http.MethodPost, "/admin/v2/users/123/actions/promote"},
		{http.MethodPost, "/admin/v2/public-content/announcements/publish"},
		{http.MethodPost, "/admin/v2/public-content/announcements/rollback"},
	} {
		req := httptest.NewRequest(route.Method, route.Path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer syntactically-valid-test-token")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d body=%s", route.Method, route.Path, rec.Code, rec.Body.String())
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
	for _, test := range tests {
		for _, authorization := range []string{"", "Bearer syntactically-valid-test-token"} {
			name := test.Method + " " + test.Path
			if authorization != "" {
				name += " with Authorization"
			}
			t.Run(name, func(t *testing.T) {
				req := httptest.NewRequest(test.Method, test.Path, strings.NewReader(`{}`))
				if authorization != "" {
					req.Header.Set("Authorization", authorization)
				}
				rec := httptest.NewRecorder()
				engine.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status=%d body=%s, want 404 for unregistered route", rec.Code, rec.Body.String())
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
