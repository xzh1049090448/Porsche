package router_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/router"
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
