package app

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestNewStateDoesNotBootstrapRootFromSettings(t *testing.T) {
	settings := &config.Settings{
		RootBootstrapUsername: "root_admin",
		RootBootstrapPassword: "Aa1@0123456789ab",
	}

	state, err := NewState(settings, nil)
	if err != nil {
		t.Fatalf("NewState() error = %v, want no Root bootstrap attempt", err)
	}
	if state == nil {
		t.Fatal("NewState() returned nil state")
	}
}

func TestNewStateLeavesGenerationStoreNilWithoutRedis(t *testing.T) {
	state, err := NewState(&config.Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.PlatformGenerations != nil {
		t.Fatal("generation store enabled without Redis")
	}
}

func TestNewStateWiresPlatformGenerationPersistenceWithRedis(t *testing.T) {
	authClient := newStateCloseTrackingRedisClient()
	generationClient := newStateCloseTrackingRedisClient()
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		return service.NewAuthRedis(authClient, "state-test-auth-hmac-key")
	}
	constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
		return service.NewPlatformGenerationStore(generationClient)
	}

	state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured"}, nil, constructors)
	if err != nil {
		t.Fatal(err)
	}
	if state.PlatformGenerations == nil || state.PlatformGenerationPersistence == nil {
		t.Fatalf("generation dependencies not wired: %#v", state)
	}
	if state.PlatformGenerationControl != nil || state.PlatformGenerationCancellations != nil || state.PlatformGenerationConverger != nil {
		t.Fatalf("generation control enabled without DB: %#v", state)
	}
	if authClient.closes != 0 || generationClient.closes != 0 {
		t.Fatalf("successful construction closed clients: auth=%d generation=%d", authClient.closes, generationClient.closes)
	}
	if err := state.Close(); err != nil || generationClient.closes != 1 || authClient.closes != 1 {
		t.Fatalf("state cleanup error/closes = %v generation:%d auth:%d, want nil/1/1", err, generationClient.closes, authClient.closes)
	}
}

func TestNewStateWiresAndStartsGenerationControlOnce(t *testing.T) {
	authClient := newStateCloseTrackingRedisClient()
	generationClient := newStateCloseTrackingRedisClient()
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		return service.NewAuthRedis(authClient, "state-test-auth-hmac-key")
	}
	constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
		return service.NewPlatformGenerationStore(generationClient)
	}
	wantControl := &service.PlatformGenerationControl{}
	wantConverger := &service.PlatformGenerationConverger{}
	constructors.newPlatformGenerationControl = func(db *gorm.DB, store *service.PlatformGenerationStore, registry *service.PlatformGenerationCancellationRegistry) (*service.PlatformGenerationControl, error) {
		if db == nil || store == nil || registry == nil {
			t.Fatal("generation control constructor received incomplete dependencies")
		}
		return wantControl, nil
	}
	constructors.newPlatformGenerationConverger = func(control *service.PlatformGenerationControl) (*service.PlatformGenerationConverger, error) {
		if control != wantControl {
			t.Fatalf("converger control = %p, want %p", control, wantControl)
		}
		return wantConverger, nil
	}
	starts := 0
	workerCloses := 0
	constructors.startPlatformGenerationConverger = func(converger *service.PlatformGenerationConverger) {
		if converger != wantConverger {
			t.Fatalf("started converger = %p, want %p", converger, wantConverger)
		}
		starts++
	}
	constructors.closePlatformGenerationConverger = func(context.Context, *service.PlatformGenerationConverger) error {
		workerCloses++
		return nil
	}

	state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured"}, &gorm.DB{Config: &gorm.Config{}}, constructors)
	if err != nil {
		t.Fatal(err)
	}
	if state.PlatformGenerationControl != wantControl || state.PlatformGenerationCancellations == nil || state.PlatformGenerationConverger != wantConverger {
		t.Fatalf("generation control dependencies not coherently wired: %#v", state)
	}
	if starts != 1 {
		t.Fatalf("generation converger starts = %d, want 1", starts)
	}
	if workerCloses != 0 || authClient.closes != 0 || generationClient.closes != 0 {
		t.Fatalf("successful construction cleaned up before ownership transfer: worker=%d generation=%d auth=%d", workerCloses, generationClient.closes, authClient.closes)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if workerCloses != 1 || authClient.closes != 1 || generationClient.closes != 1 {
		t.Fatalf("state ownership cleanup counts = worker:%d generation:%d auth:%d, want 1/1/1", workerCloses, generationClient.closes, authClient.closes)
	}
	if err := state.Close(); err != nil || workerCloses != 1 || authClient.closes != 1 || generationClient.closes != 1 {
		t.Fatalf("second state cleanup error/counts = %v worker:%d generation:%d auth:%d", err, workerCloses, generationClient.closes, authClient.closes)
	}
}

func TestNewStateGenerationConstructorFailureClosesOwnedRedisInOrder(t *testing.T) {
	for _, test := range []struct {
		name           string
		failController bool
		failConverger  bool
	}{
		{name: "controller", failController: true},
		{name: "converger", failConverger: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []string
			authClient := newStateCloseTrackingRedisClientWithClose(func() error {
				mu.Lock()
				events = append(events, "auth")
				mu.Unlock()
				return nil
			})
			generationClient := newStateCloseTrackingRedisClientWithClose(func() error {
				mu.Lock()
				events = append(events, "generation")
				mu.Unlock()
				return nil
			})
			constructors := defaultStateConstructors()
			constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
				return service.NewAuthRedis(authClient, "state-test-auth-hmac-key")
			}
			constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
				return service.NewPlatformGenerationStore(generationClient)
			}
			constructors.newPlatformGenerationControl = func(*gorm.DB, *service.PlatformGenerationStore, *service.PlatformGenerationCancellationRegistry) (*service.PlatformGenerationControl, error) {
				if test.failController {
					return nil, errors.New("redis://user:secret@backend/controller")
				}
				return &service.PlatformGenerationControl{}, nil
			}
			constructors.newPlatformGenerationConverger = func(*service.PlatformGenerationControl) (*service.PlatformGenerationConverger, error) {
				if test.failConverger {
					return nil, errors.New("redis://user:secret@backend/converger")
				}
				return &service.PlatformGenerationConverger{}, nil
			}
			starts := 0
			workerCloses := 0
			constructors.startPlatformGenerationConverger = func(*service.PlatformGenerationConverger) { starts++ }
			constructors.closePlatformGenerationConverger = func(context.Context, *service.PlatformGenerationConverger) error {
				workerCloses++
				return nil
			}

			state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured"}, &gorm.DB{Config: &gorm.Config{}}, constructors)
			if state != nil || !errors.Is(err, service.ErrPlatformGenerationControlUnavailable) || err.Error() != service.ErrPlatformGenerationControlUnavailable.Error() {
				t.Fatalf("state/error = %#v/%v, want nil/fixed unavailable", state, err)
			}
			if starts != 0 {
				t.Fatalf("failed construction started %d workers, want 0", starts)
			}
			if workerCloses != 0 {
				t.Fatalf("failed construction closed an unconstructed worker %d times", workerCloses)
			}
			if got := events; len(got) != 2 || got[0] != "generation" || got[1] != "auth" {
				t.Fatalf("partial cleanup order = %v, want [generation auth]", got)
			}
			if generationClient.closes != 1 || authClient.closes != 1 {
				t.Fatalf("partial cleanup closes = generation:%d auth:%d, want 1/1", generationClient.closes, authClient.closes)
			}
		})
	}
}

func TestNewStateLaterFailureClosesUnstartedGenerationWorkerBeforeRedis(t *testing.T) {
	var events []string
	authClient := newStateCloseTrackingRedisClientWithClose(func() error {
		events = append(events, "auth")
		return nil
	})
	generationClient := newStateCloseTrackingRedisClientWithClose(func() error {
		events = append(events, "generation")
		return nil
	})
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		return service.NewAuthRedis(authClient, "state-test-auth-hmac-key")
	}
	constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
		return service.NewPlatformGenerationStore(generationClient)
	}
	control := &service.PlatformGenerationControl{}
	converger := &service.PlatformGenerationConverger{}
	constructors.newPlatformGenerationControl = func(*gorm.DB, *service.PlatformGenerationStore, *service.PlatformGenerationCancellationRegistry) (*service.PlatformGenerationControl, error) {
		return control, nil
	}
	constructors.newPlatformGenerationConverger = func(*service.PlatformGenerationControl) (*service.PlatformGenerationConverger, error) {
		return converger, nil
	}
	starts := 0
	constructors.startPlatformGenerationConverger = func(*service.PlatformGenerationConverger) { starts++ }
	workerCloses := 0
	constructors.closePlatformGenerationConverger = func(ctx context.Context, got *service.PlatformGenerationConverger) error {
		if got != converger {
			t.Fatalf("closed converger = %p, want %p", got, converger)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("partial worker close received an unbounded context")
		}
		workerCloses++
		events = append(events, "worker")
		return errors.New("redis://user:secret@backend/worker-close")
	}
	constructors.newUserManagementActions = func(*gorm.DB, *service.AuthRedis, *actionsecurity.Crypto) (*service.UserManagementActions, error) {
		return nil, service.ErrActionVerificationUnavailable
	}

	state, err := newState(&config.Settings{
		RedisURL:              "redis://configured",
		AuthHMACKey:           "configured",
		ActionSecurityHMACKey: bytes.Repeat([]byte{0x45}, 32),
	}, &gorm.DB{Config: &gorm.Config{}}, constructors)
	if state != nil || !errors.Is(err, service.ErrActionVerificationUnavailable) || err.Error() != service.ErrActionVerificationUnavailable.Error() {
		t.Fatalf("state/error = %#v/%v, want nil/original fixed construction error", state, err)
	}
	if starts != 0 {
		t.Fatalf("later failed construction started %d workers, want 0", starts)
	}
	if workerCloses != 1 {
		t.Fatalf("later failed construction closed worker %d times, want 1", workerCloses)
	}
	if len(events) != 3 || events[0] != "worker" || events[1] != "generation" || events[2] != "auth" {
		t.Fatalf("partial cleanup order = %v, want [worker generation auth]", events)
	}
	if generationClient.closes != 1 || authClient.closes != 1 {
		t.Fatalf("partial cleanup closes = generation:%d auth:%d, want 1/1", generationClient.closes, authClient.closes)
	}
}

func TestStateCloseStopsWorkerBeforeClientsAndIsConcurrentSafe(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	state := &State{
		PlatformGenerationConverger: &service.PlatformGenerationConverger{},
		PlatformGenerations:         mustStateGenerationStore(t, newStateCloseTrackingRedisClient()),
		AuthRedis:                   mustStateAuthRedis(t, newStateCloseTrackingRedisClient()),
		closeTimeout:                time.Second,
		closePlatformGenerationConverger: func(context.Context, *service.PlatformGenerationConverger) error {
			record("worker")
			return nil
		},
		closePlatformGenerations: func(*service.PlatformGenerationStore) error {
			record("generation")
			return nil
		},
		closeAuthRedis: func(*service.AuthRedis) error {
			record("auth")
			return nil
		},
	}

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- state.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Close() error = %v", err)
		}
	}
	if len(events) != 3 || events[0] != "worker" || events[1] != "generation" || events[2] != "auth" {
		t.Fatalf("close order/count = %v, want [worker generation auth]", events)
	}
	var nilState *State
	if err := nilState.Close(); err != nil {
		t.Fatalf("nil State.Close() error = %v", err)
	}
}

func TestStateCloseSanitizesTimeoutAndContinuesCleanup(t *testing.T) {
	var events []string
	state := &State{
		PlatformGenerationConverger: &service.PlatformGenerationConverger{},
		PlatformGenerations:         mustStateGenerationStore(t, newStateCloseTrackingRedisClient()),
		AuthRedis:                   mustStateAuthRedis(t, newStateCloseTrackingRedisClient()),
		closeTimeout:                time.Nanosecond,
		closePlatformGenerationConverger: func(ctx context.Context, _ *service.PlatformGenerationConverger) error {
			events = append(events, "worker")
			<-ctx.Done()
			return errors.New("redis://user:secret@backend/worker")
		},
		closePlatformGenerations: func(*service.PlatformGenerationStore) error {
			events = append(events, "generation")
			return errors.New("redis://user:secret@backend/generation")
		},
		closeAuthRedis: func(*service.AuthRedis) error {
			events = append(events, "auth")
			return errors.New("redis://user:secret@backend/auth")
		},
	}

	err := state.Close()
	if err == nil || err.Error() != "close platform generation worker\nclose platform generation store\nclose authentication Redis" {
		t.Fatalf("Close() error = %q, want fixed joined categories", err)
	}
	if len(events) != 3 || events[0] != "worker" || events[1] != "generation" || events[2] != "auth" {
		t.Fatalf("close after timeout order = %v", events)
	}
	if err2 := state.Close(); err2 == nil || err2.Error() != err.Error() || len(events) != 3 {
		t.Fatalf("second Close() = %v, events=%v; want same error and no repeated cleanup", err2, events)
	}
}

func TestStateCloseDoesNotCloseExternalDatabase(t *testing.T) {
	connector := &statePingConnector{}
	sqlDB := sql.OpenDB(connector)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := &State{DB: &gorm.DB{Config: &gorm.Config{ConnPool: sqlDB}}}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("external database was closed: %v", err)
	}
	connector.mu.Lock()
	closes := connector.closes
	connector.mu.Unlock()
	if closes != 0 {
		t.Fatalf("external database connections closed = %d, want 0", closes)
	}
}

func TestNewStateLeavesPlatformGenerationPersistenceNilWithoutRedis(t *testing.T) {
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		t.Fatal("auth Redis constructor called without Redis configuration")
		return nil, nil
	}
	constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
		t.Fatal("generation store constructor called without Redis configuration")
		return nil, nil
	}

	state, err := newState(&config.Settings{}, nil, constructors)
	if err != nil {
		t.Fatal(err)
	}
	if state.PlatformGenerations != nil || state.PlatformGenerationPersistence != nil {
		t.Fatalf("generation dependencies enabled without Redis: %#v", state)
	}
}

func TestNewStateFailsClosedForConfiguredInvalidRedis(t *testing.T) {
	state, err := NewState(&config.Settings{RedisURL: "not-a-redis-url", AuthHMACKey: "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, nil)
	if err == nil || state != nil {
		t.Fatalf("NewState() state=%#v error=%v, want fail-closed Redis construction", state, err)
	}
}

func TestNewStateWiresGenerationStoreWithTestRedis(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_REDIS_URL")
	}
	settings := &config.Settings{RedisURL: url, AuthHMACKey: "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ"}
	state, err := NewState(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.AuthRedis == nil || state.PlatformGenerations == nil {
		t.Fatalf("stores not independently initialized: %#v", state)
	}
	if err := state.PlatformGenerations.CheckAvailable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewStateActionSecurityConstructorLifecycle(t *testing.T) {
	withoutKey, err := NewState(&config.Settings{}, nil)
	if err != nil {
		t.Fatalf("NewState(without key) error = %v", err)
	}
	if withoutKey.ActionSecurityCrypto != nil {
		t.Fatal("NewState constructed action-security crypto without a root key")
	}
	if withoutKey.ActionVerifications != nil {
		t.Fatal("NewState constructed action verification service without a root key")
	}
	if withoutKey.UserDeleteActions != nil {
		t.Fatal("NewState constructed user delete actions without a root key")
	}
	if withoutKey.UserManagementActions != nil {
		t.Fatal("NewState constructed user management actions without a root key")
	}

	root := bytes.Repeat([]byte{0x42}, 32)
	if _, err := NewState(&config.Settings{ActionSecurityHMACKey: root}, nil); err == nil {
		t.Fatal("NewState accepted a root key with partial database/Redis dependencies")
	}
	if !bytes.Equal(root, bytes.Repeat([]byte{0x42}, 32)) {
		t.Fatal("NewState mutated configured root key")
	}
}

func TestNewStateAssignsCompleteCreateAccountActionsAndDeleteCompatibilityView(t *testing.T) {
	root := bytes.Repeat([]byte{0x43}, 32)
	want := &service.UserManagementActions{
		Verifications: &service.ActionVerificationService{},
		Operations:    &service.ActionOperationService{},
		DeleteOutbox:  &service.AdminActionOutboxWriter{},
		CreateOutbox:  &service.CreateAccountOutboxWriter{},
		NewDeleteExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
			return nil, service.ErrActionOperationUnavailable
		},
		NewCreateExecution: func(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
			return nil, service.ErrActionOperationUnavailable
		},
	}
	var gotDB *gorm.DB
	var gotAuthRedis *service.AuthRedis
	var gotCrypto *actionsecurity.Crypto
	client := newStateCloseTrackingRedisClient()
	generationClient := newStateCloseTrackingRedisClient()
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		return service.NewAuthRedis(client, "state-test-auth-hmac-key")
	}
	constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
		return service.NewPlatformGenerationStore(generationClient)
	}
	constructors.newUserManagementActions = func(db *gorm.DB, authRedis *service.AuthRedis, crypto *actionsecurity.Crypto) (*service.UserManagementActions, error) {
		gotDB, gotAuthRedis, gotCrypto = db, authRedis, crypto
		return want, nil
	}
	db := &gorm.DB{Config: &gorm.Config{}}
	state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured", ActionSecurityHMACKey: root}, db, constructors)
	if err != nil {
		t.Fatal(err)
	}
	if state.UserManagementActions != want || state.ActionVerifications != want.Verifications || gotDB != db || gotAuthRedis != state.AuthRedis || gotCrypto != state.ActionSecurityCrypto {
		t.Fatalf("incoherent state/bundle wiring: state=%#v", state)
	}
	if state.UserDeleteActions == nil || state.UserDeleteActions.Verifications != want.Verifications || state.UserDeleteActions.Operations != want.Operations ||
		state.UserDeleteActions.Outbox != want.DeleteOutbox || state.UserDeleteActions.NewExecution == nil {
		t.Fatalf("delete compatibility view did not share the complete bundle: %#v", state.UserDeleteActions)
	}
	if client.closes != 0 {
		t.Fatalf("successful state construction closed owned Redis %d times", client.closes)
	}
	if generationClient.closes != 0 {
		t.Fatalf("successful state construction closed owned generation Redis %d times", generationClient.closes)
	}
	if err := state.Close(); err != nil || client.closes != 1 || generationClient.closes != 1 {
		t.Fatalf("state cleanup error/closes = %v auth:%d generation:%d, want nil/1/1", err, client.closes, generationClient.closes)
	}
	if !bytes.Equal(root, bytes.Repeat([]byte{0x43}, 32)) {
		t.Fatal("NewState mutated configured root key")
	}
}

func TestNewStateCreateAccountActionsFailureExposesNoCreateRouteDependency(t *testing.T) {
	complete := func() *service.UserManagementActions {
		return &service.UserManagementActions{
			Verifications: &service.ActionVerificationService{}, Operations: &service.ActionOperationService{},
			DeleteOutbox: &service.AdminActionOutboxWriter{}, CreateOutbox: &service.CreateAccountOutboxWriter{},
			NewDeleteExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) { return nil, nil },
			NewCreateExecution: func(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
				return nil, nil
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*service.UserManagementActions)
		err    error
	}{
		{name: "constructor error", err: service.ErrActionVerificationUnavailable},
		{name: "verification service", mutate: func(bundle *service.UserManagementActions) { bundle.Verifications = nil }},
		{name: "operation service", mutate: func(bundle *service.UserManagementActions) { bundle.Operations = nil }},
		{name: "delete writer", mutate: func(bundle *service.UserManagementActions) { bundle.DeleteOutbox = nil }},
		{name: "create writer", mutate: func(bundle *service.UserManagementActions) { bundle.CreateOutbox = nil }},
		{name: "delete factory", mutate: func(bundle *service.UserManagementActions) { bundle.NewDeleteExecution = nil }},
		{name: "create factory", mutate: func(bundle *service.UserManagementActions) { bundle.NewCreateExecution = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newStateCloseTrackingRedisClient()
			generationClient := newStateCloseTrackingRedisClient()
			constructors := defaultStateConstructors()
			constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
				return service.NewAuthRedis(client, "state-test-auth-hmac-key")
			}
			constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
				return service.NewPlatformGenerationStore(generationClient)
			}
			constructors.newUserManagementActions = func(*gorm.DB, *service.AuthRedis, *actionsecurity.Crypto) (*service.UserManagementActions, error) {
				bundle := complete()
				if test.mutate != nil {
					test.mutate(bundle)
				}
				return bundle, test.err
			}
			state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured", ActionSecurityHMACKey: bytes.Repeat([]byte{0x44}, 32)}, &gorm.DB{Config: &gorm.Config{}}, constructors)
			if state != nil || !errors.Is(err, service.ErrActionVerificationUnavailable) || err.Error() != service.ErrActionVerificationUnavailable.Error() {
				t.Fatalf("state/error = %#v/%v, want nil/fixed sanitized unavailable", state, err)
			}
			if client.closes != 1 {
				t.Fatalf("failed state construction closed owned Redis %d times, want 1", client.closes)
			}
			if generationClient.closes != 1 {
				t.Fatalf("failed state construction closed owned generation Redis %d times, want 1", generationClient.closes)
			}
		})
	}
}

type stateCloseTrackingRedisClient struct {
	*redis.Client
	closes  int
	closeFn func() error
}

func newStateCloseTrackingRedisClient() *stateCloseTrackingRedisClient {
	return &stateCloseTrackingRedisClient{Client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})}
}

func newStateCloseTrackingRedisClientWithClose(closeFn func() error) *stateCloseTrackingRedisClient {
	client := newStateCloseTrackingRedisClient()
	client.closeFn = closeFn
	return client
}

func (client *stateCloseTrackingRedisClient) Close() error {
	client.closes++
	if client.closeFn != nil {
		return client.closeFn()
	}
	return nil
}

func mustStateGenerationStore(t *testing.T, client redis.UniversalClient) *service.PlatformGenerationStore {
	t.Helper()
	store, err := service.NewPlatformGenerationStore(client)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustStateAuthRedis(t *testing.T, client redis.UniversalClient) *service.AuthRedis {
	t.Helper()
	store, err := service.NewAuthRedis(client, "state-test-auth-hmac-key")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type statePingConnector struct {
	mu     sync.Mutex
	closes int
}

func (c *statePingConnector) Connect(context.Context) (driver.Conn, error) {
	return &statePingConn{connector: c}, nil
}

func (*statePingConnector) Driver() driver.Driver { return statePingDriver{} }

type statePingDriver struct{}

func (statePingDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type statePingConn struct {
	connector *statePingConnector
}

func (*statePingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*statePingConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (*statePingConn) Ping(context.Context) error          { return nil }
func (c *statePingConn) Close() error {
	c.connector.mu.Lock()
	c.connector.closes++
	c.connector.mu.Unlock()
	return nil
}
