package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const a08GatewayIsolationDriverName = "porsche_a08_gateway_isolation"

var (
	a08GatewayIsolationDriverOnce sync.Once
	a08GatewayIsolationScripts    sync.Map
)

type a08GatewayIsolationDriver struct{}
type a08GatewayIsolationConn struct{ options chan driver.TxOptions }

func (a08GatewayIsolationDriver) Open(name string) (driver.Conn, error) {
	value, ok := a08GatewayIsolationScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown A08 Gateway isolation script")
	}
	return &a08GatewayIsolationConn{options: value.(chan driver.TxOptions)}, nil
}
func (*a08GatewayIsolationConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (*a08GatewayIsolationConn) Close() error { return nil }
func (*a08GatewayIsolationConn) Begin() (driver.Tx, error) {
	return nil, errors.New("legacy begin must not be used")
}
func (c *a08GatewayIsolationConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.options <- options
	return nil, errors.New("stop after observing isolation")
}

func TestA08RuntimeGatewayAuthenticationBeginsReadCommitted(t *testing.T) {
	a08GatewayIsolationDriverOnce.Do(func() { sql.Register(a08GatewayIsolationDriverName, a08GatewayIsolationDriver{}) })
	dsn := fmt.Sprintf("gateway-isolation-%d", testSnowflake.Next())
	options := make(chan driver.TxOptions, 1)
	a08GatewayIsolationScripts.Store(dsn, options)
	t.Cleanup(func() { a08GatewayIsolationScripts.Delete(dsn) })
	root, err := sql.Open(a08GatewayIsolationDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: root, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if principal, err := NewGatewayTokenService(db).AuthenticatePrincipal("sk-gw-isolation-probe", "", "", time.Now()); principal != nil || !IsGatewayTokenError(err, GatewayTokenUnavailable) {
		t.Fatalf("probe principal/error = %#v/%v", principal, err)
	}
	observed := <-options
	if observed.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		t.Fatalf("Gateway authentication isolation=%v, want READ COMMITTED", observed.Isolation)
	}
}

type a08RuntimeCredentials struct {
	access  string
	refresh string
	key     models.GatewayAPIToken
	secret  string
	session models.Session
}

func mintA08RuntimeCredentials(t *testing.T, f *realActionFixture, target models.User, sequence int) a08RuntimeCredentials {
	t.Helper()
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	refreshSecret, err := security.NewRefreshSecret()
	if err != nil {
		t.Fatal(err)
	}
	settings := testSessionSettings()
	session := models.Session{AuditFields: a14Audit(f.clock.NowMillis()), SID: sid, UserID: target.ID, LoginMethod: models.LoginMethodPassword,
		SessionVersion: sequence, RefreshHMAC: security.RefreshHMAC(refreshSecret, settings.AuthHMACKey), LastActiveAt: f.clock.NowMillis(), ExpiresAt: f.clock.NowMillis() + 86_400_000}
	if err := f.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	settings.JWTSecretKey = "a08-runtime-jwt-secret"
	settings.SessionAccessMinutes = 5
	access, err := security.CreateAccessToken(fmt.Sprint(target.Guid), settings.JWTSecretKey, settings.SessionAccessMinutes,
		map[string]interface{}{"sid": session.SID, "sv": session.SessionVersion, "av": target.AuthVersion, "role": int(target.Role)})
	if err != nil {
		t.Fatal(err)
	}
	key, keySecret, err := NewGatewayTokenService(f.db).Create(&target, GatewayTokenCreateInput{Name: fmt.Sprintf("a08-runtime-%d", sequence)})
	if err != nil {
		t.Fatal(err)
	}
	return a08RuntimeCredentials{access: access, refresh: sid + "." + refreshSecret, key: *key, secret: keySecret, session: session}
}

func assertA08RuntimeSessionInvalidated(t *testing.T, f *realActionFixture, target models.User, credentials a08RuntimeCredentials) {
	t.Helper()
	settings := testSessionSettings()
	settings.JWTSecretKey = "a08-runtime-jwt-secret"
	claims, err := security.DecodeAccessToken(credentials.access, settings.JWTSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionService(f.db, f.authRedis, settings)
	if _, err := sessions.Validate(context.Background(), claims["sid"].(string), target.ID, int(claims["sv"].(float64)), int(claims["av"].(float64))); err == nil {
		t.Fatal("pre-transition Access remained valid")
	}
	if _, err := sessions.Refresh(context.Background(), credentials.refresh); err == nil {
		t.Fatal("pre-transition Refresh remained valid")
	}
	var stored models.GatewayAPIToken
	if err := f.db.First(&stored, credentials.key.ID).Error; err != nil || stored.Status != models.GatewayTokenActive || stored.TokenHash != credentials.key.TokenHash {
		t.Fatalf("Gateway Key was changed by A08 transition: %#v/%v", stored, err)
	}
}

func TestA08RuntimeCredentialsAndGatewayOwnerPolicyChangeImmediately(t *testing.T) {
	f := openRealActionFixture(t, 1_921_000_000_000)
	beforePromote := mintA08RuntimeCredentials(t, f, f.targetRow, 1)
	principal, err := NewGatewayTokenService(f.db).AuthenticatePrincipal(beforePromote.secret, "127.0.0.1", "", time.Now())
	if err != nil || principal.OwnerRole() != models.UserRoleUser || principal.OwnerAuthVersion() != f.targetRow.AuthVersion || principal.OwnerPolicyVersion() != 0 || principal.AllowsCapability("users.read") {
		t.Fatalf("ordinary no-policy-head Gateway principal = %#v/%v", principal, err)
	}
	promote := actionsecurity.PromoteIntent{TargetGUID: f.targetRow.Guid, ExpectedAuthVersion: f.targetRow.AuthVersion, ExpectedPermissionsVersion: 0,
		CatalogVersion: models.PermissionCatalogVersion, Reason: "runtime promote"}
	if outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersPromote, promote); err != nil || outcome.Failure != nil {
		t.Fatalf("promote = %#v/%v", outcome, err)
	}
	assertA08RuntimeSessionInvalidated(t, f, f.targetRow, beforePromote)
	principal, err = NewGatewayTokenService(f.db).AuthenticatePrincipal(beforePromote.secret, "127.0.0.1", "", time.Now())
	if err != nil || principal.OwnerRole() != models.UserRoleAdmin || principal.OwnerAuthVersion() != f.targetRow.AuthVersion+1 || principal.OwnerPolicyVersion() != 1 || !principal.AllowsCapability("users.read") {
		t.Fatalf("fresh promoted Gateway principal = %#v/%v", principal, err)
	}

	var promoted models.User
	if err := f.db.First(&promoted, f.targetRow.ID).Error; err != nil {
		t.Fatal(err)
	}
	beforeDeny := mintA08RuntimeCredentials(t, f, promoted, 2)
	deny := actionsecurity.PermissionsWriteIntent{TargetGUID: promoted.Guid, ExpectedAuthVersion: promoted.AuthVersion, ExpectedPermissionsVersion: 1,
		CatalogVersion: models.PermissionCatalogVersion, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}, Reason: "runtime deny"}
	if outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersPermissionsWrite, deny); err != nil || outcome.Failure != nil {
		t.Fatalf("permissions write = %#v/%v", outcome, err)
	}
	assertA08RuntimeSessionInvalidated(t, f, promoted, beforeDeny)
	principal, err = NewGatewayTokenService(f.db).AuthenticatePrincipal(beforePromote.secret, "127.0.0.1", "", time.Now())
	if err != nil || principal.OwnerAuthVersion() != promoted.AuthVersion+1 || principal.OwnerPolicyVersion() != 2 || principal.AllowsCapability("users.read") {
		t.Fatalf("fresh denied Gateway principal = %#v/%v", principal, err)
	}

	var denied models.User
	if err := f.db.First(&denied, f.targetRow.ID).Error; err != nil {
		t.Fatal(err)
	}
	beforeDemote := mintA08RuntimeCredentials(t, f, denied, 3)
	demote := actionsecurity.DemoteIntent{TargetGUID: denied.Guid, ExpectedAuthVersion: denied.AuthVersion, ExpectedPermissionsVersion: 2,
		CatalogVersion: models.PermissionCatalogVersion, Reason: "runtime demote"}
	if outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersDemote, demote); err != nil || outcome.Failure != nil {
		t.Fatalf("demote = %#v/%v", outcome, err)
	}
	assertA08RuntimeSessionInvalidated(t, f, denied, beforeDemote)
	principal, err = NewGatewayTokenService(f.db).AuthenticatePrincipal(beforePromote.secret, "127.0.0.1", "", time.Now())
	if err != nil || principal.OwnerRole() != models.UserRoleUser || principal.OwnerPolicyVersion() != 3 || principal.AllowsCapability("users.read") {
		t.Fatalf("fresh demoted Gateway principal = %#v/%v", principal, err)
	}

	var demoted models.User
	if err := f.db.First(&demoted, f.targetRow.ID).Error; err != nil {
		t.Fatal(err)
	}
	beforeRepromote := mintA08RuntimeCredentials(t, f, demoted, 4)
	repromote := actionsecurity.PromoteIntent{TargetGUID: demoted.Guid, ExpectedAuthVersion: demoted.AuthVersion, ExpectedPermissionsVersion: 3,
		CatalogVersion: models.PermissionCatalogVersion, Reason: "runtime baseline repromote"}
	if outcome, err := executeA08RealTransition(f, actionsecurity.ActionUsersPromote, repromote); err != nil || outcome.Failure != nil {
		t.Fatalf("baseline repromote = %#v/%v", outcome, err)
	}
	assertA08RuntimeSessionInvalidated(t, f, demoted, beforeRepromote)
	principal, err = NewGatewayTokenService(f.db).AuthenticatePrincipal(beforePromote.secret, "127.0.0.1", "", time.Now())
	if err != nil || principal.OwnerRole() != models.UserRoleAdmin || principal.OwnerPolicyVersion() != 4 || !principal.AllowsCapability("users.read") {
		t.Fatalf("historical deny revived after baseline repromote: %#v/%v", principal, err)
	}
}

func TestA08RuntimeGatewayPolicyCorruptionFailsClosed(t *testing.T) {
	f := openRealActionFixture(t, 1_921_010_000_000)
	credentials := mintA08RuntimeCredentials(t, f, f.targetRow, 1)
	capability, ok := models.PermissionCapabilityCode("users.read")
	if !ok {
		t.Fatal("users.read missing from catalog")
	}
	orphan := models.PermissionOverride{AuditFields: a14Audit(f.clock.NowMillis()), UserID: f.targetRow.ID, PolicyVersion: 1, Capability: capability, Effect: 3}
	if err := f.db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if principal, err := NewGatewayTokenService(f.db).AuthenticatePrincipal(credentials.secret, "127.0.0.1", "", time.Now()); principal != nil || !IsGatewayTokenError(err, GatewayTokenUnavailable) {
		t.Fatalf("corrupt owner policy did not fail closed: %#v/%v", principal, err)
	}
}
