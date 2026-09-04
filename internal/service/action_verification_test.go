package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/security"
)

func TestActionVerificationIssueContractExists(t *testing.T) {
	var _ *ActionVerificationService
	var _ *IssuedVerification
}

func TestActionVerificationIssuePersistsOnlyDigestsAndReissueSoftDeletes(t *testing.T) {
	db := openTestMySQL(t)
	now := int64(1_800_000_000_000)
	passwordText := "Task7-Strong-Password!"
	passwordHash := actionIssuePasswordHash(t, passwordText)
	username := fixtureUsername(testSnowflake.Next())
	actor := models.User{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
		Username:    &username, PasswordHash: &passwordHash, Status: models.UserStatusActive,
		Role: models.UserRoleRoot, AuthVersion: 7, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{},
	}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	session := models.Session{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0},
		SID:         sid, UserID: actor.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 3,
		RefreshHMAC: strings.Repeat("a", 64), LastActiveAt: now, ExpiresAt: now + 3_600_000,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	client := &actionIssueRedisClient{actionRateEvalClient: newActionRateEvalClient()}
	random := bytes.NewReader(append(bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)...))
	service := newTestActionVerificationService(t, db, client, &actionIssueClock{now: now}, random, func() int64 { return testSnowflake.Next() })
	issue := func() (*IssuedVerification, []byte) {
		password := []byte(passwordText)
		result, err := service.Issue(context.Background(), VerificationIssue{
			Action: testNoopAction, Actor: ActionActor{UserID: actor.ID, UserGUID: actor.Guid, AuthVersion: 7, SessionSID: sid, SessionVersion: 3},
			Intent: "opaque-intent-value", CurrentPassword: password, TrustedIP: "203.0.113.9",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result, password
	}
	first, firstPassword := issue()
	if first.ExpiresAt != now+300_000 || len(first.Ticket) != 46 || !strings.HasPrefix(first.Ticket, "av_") {
		t.Fatalf("first result = %#v", first)
	}
	if !bytes.Equal(firstPassword, make([]byte, len(firstPassword))) {
		t.Fatal("successful Issue did not clear password")
	}
	second, secondPassword := issue()
	if second.Ticket == first.Ticket || !bytes.Equal(secondPassword, make([]byte, len(secondPassword))) {
		t.Fatal("reissue did not rotate ticket or clear password")
	}

	var rows []models.AdminActionVerification
	if err := db.Unscoped().Where("actor_user_id = ? AND action = ?", actor.ID, int(testNoopAction)).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].IsDeleted != 1 || rows[0].ConsumedAt != nil || rows[1].IsDeleted != 0 {
		t.Fatalf("verification rows = %#v", rows)
	}
	for _, row := range rows {
		if row.ActorUserID != actor.ID || row.ActorAuthVersion != 7 || row.SessionID != session.ID || row.TargetGUID != nil ||
			row.TargetKind != 1 || row.CreatedBy == nil || *row.CreatedBy != actor.ID || row.UpdatedBy == nil || *row.UpdatedBy != actor.ID ||
			len(row.IntentHMAC) != 64 || len(row.TicketHMAC) != 64 {
			t.Fatalf("unsafe or incomplete persisted row: %#v", row)
		}
		if _, err := hex.DecodeString(row.IntentHMAC); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{passwordText, sid, "opaque-intent-value", first.Ticket, second.Ticket} {
			if strings.Contains(row.IntentHMAC+row.TicketHMAC, raw) {
				t.Fatalf("persisted digest leaked raw input %q", raw)
			}
		}
	}
}
