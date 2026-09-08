package authz

import (
	"errors"

	"github.com/porsche/ai-gateway-go/internal/models"
)

// Effect is an immutable override snapshot's requested capability effect.
type Effect string

const (
	// Inherit uses the catalog default.
	Inherit Effect = "inherit"
	// Allow requests an allow override.
	Allow Effect = "allow"
	// Deny requests a deny override.
	Deny Effect = "deny"
)

// Decision is an immutable result for a requested internal authorization action.
type Decision uint8

const (
	// Denied is the zero-value fail-closed decision.
	Denied Decision = iota
	// Allowed permits the requested internal action.
	Allowed
	// Hidden directs a future authenticated adapter to its uniform not-found result.
	Hidden
)

// Account is a trusted, immutable service-layer account snapshot.
//
// Callers must establish account freshness and session validity before creating
// an Evaluator; this type is not authentication evidence.
type Account struct {
	ID        int64
	GUID      int64
	Role      models.UserRole
	Status    models.UserStatus
	IsDeleted int
}

// Override is a requested capability override copied by NewEvaluator.
type Override struct {
	Capability string
	Effect     Effect
}

// Evaluator evaluates copied account and override snapshots without external I/O.
type Evaluator struct {
	actor     Account
	overrides map[string]Effect
}

var (
	// ErrInvalidActor reports an account snapshot that cannot establish an evaluator.
	ErrInvalidActor = errors.New("invalid authorization actor")
	// ErrInvalidOverrides reports a malformed or inapplicable override snapshot.
	ErrInvalidOverrides = errors.New("invalid authorization overrides")
)

// NewEvaluator constructs a fail-closed evaluator from trusted snapshots.
//
// It copies valid overrides. Callers must reject construction errors rather than
// replacing them with an empty policy, and must provide fresh authenticated
// snapshots for every integration boundary.
func NewEvaluator(actor Account, rules []Override) (*Evaluator, error) {
	if !validAccount(actor) || actor.IsDeleted != 0 || actor.Status != models.UserStatusActive {
		return nil, ErrInvalidActor
	}
	if len(rules) != 0 && actor.Role != models.UserRoleAdmin {
		return nil, ErrInvalidOverrides
	}

	copied, err := validateOverrides(rules)
	if err != nil {
		return nil, err
	}
	return &Evaluator{actor: actor, overrides: copied}, nil
}

func validateOverrides(rules []Override) (map[string]Effect, error) {
	copied := make(map[string]Effect, len(rules))
	for _, rule := range rules {
		definition, ok := lookup(rule.Capability)
		if !ok || !definition.Grantable || definition.RootOnly || definition.Unavailable {
			return nil, ErrInvalidOverrides
		}
		if rule.Effect != Inherit && rule.Effect != Allow && rule.Effect != Deny {
			return nil, ErrInvalidOverrides
		}
		if _, exists := copied[rule.Capability]; exists {
			return nil, ErrInvalidOverrides
		}
		copied[rule.Capability] = rule.Effect
	}

	return copied, nil
}

func knownRole(role models.UserRole) bool {
	return role == models.UserRoleUser || role == models.UserRoleAdmin || role == models.UserRoleRoot
}

func validAccount(account Account) bool {
	return account.ID > 0 && account.GUID > 0 && knownRole(account.Role) &&
		(account.Status == models.UserStatusActive || account.Status == models.UserStatusDisabled) &&
		(account.IsDeleted == 0 || account.IsDeleted == 1)
}

func (e *Evaluator) manager() bool {
	return e != nil && validAccount(e.actor) && e.actor.IsDeleted == 0 &&
		e.actor.Status == models.UserStatusActive &&
		(e.actor.Role == models.UserRoleAdmin || e.actor.Role == models.UserRoleRoot)
}

func (e *Evaluator) capability(name string) Decision {
	if !e.manager() {
		return Denied
	}
	definition, ok := lookup(name)
	if !ok || definition.Unavailable {
		return Denied
	}
	if e.actor.Role == models.UserRoleRoot {
		return Allowed
	}
	if definition.RootOnly {
		return Denied
	}
	switch e.overrides[name] {
	case Deny:
		return Denied
	case Allow:
		if definition.Grantable {
			return Allowed
		}
		return Denied
	case Inherit, "":
		if definition.AdminDefault {
			return Allowed
		}
	}
	return Denied
}

// User evaluates a user-scoped capability against one required target snapshot.
// It does not validate target freshness, session state, or perform the action.
func (e *Evaluator) User(name string, target Account) Decision {
	if !e.manager() {
		return Denied
	}
	definition, ok := lookup(name)
	if !ok || definition.scopes&userScope == 0 || definition.Unavailable {
		return Denied
	}
	if !validAccount(target) || target.ID == e.actor.ID || target.GUID == e.actor.GUID ||
		target.Role == models.UserRoleRoot || target.Role >= e.actor.Role {
		return Hidden
	}
	if target.IsDeleted != 0 {
		if e.capability("users.deleted.read") != Allowed {
			return Hidden
		}
		if name != "users.read" && name != "users.deleted.read" && name != "users.audit.read" {
			return Denied
		}
	} else if name == "users.deleted.read" {
		return Hidden
	}
	if name == "users.promote" && target.Role != models.UserRoleUser {
		return Denied
	}
	if (name == "users.demote" || name == "users.permissions.write") && target.Role != models.UserRoleAdmin {
		return Denied
	}
	return e.capability(name)
}

// Create evaluates whether an actor may create the requested role.
// It does not create users, authorize future tickets, or audit the operation.
func (e *Evaluator) Create(role models.UserRole) Decision {
	if !e.manager() || (role != models.UserRoleUser && role != models.UserRoleAdmin) || role >= e.actor.Role {
		return Denied
	}
	return e.capability("users.create")
}

// Collection evaluates a collection-scoped capability and its tombstone request.
// A caller must still impose its SQL visibility and consistency obligations.
func (e *Evaluator) Collection(name string, includeDeleted bool) Decision {
	definition, ok := lookup(name)
	if !ok || definition.scopes&collectionScope == 0 {
		return Denied
	}
	if includeDeleted && e.capability("users.deleted.read") != Allowed {
		return Denied
	}
	return e.capability(name)
}

// Resource evaluates a resource-scoped capability.
// It does not load or modify the referenced resource.
func (e *Evaluator) Resource(name string) Decision {
	definition, ok := lookup(name)
	if !ok || definition.scopes&resourceScope == 0 {
		return Denied
	}
	return e.capability(name)
}

// CapabilityNames returns a copied menu hint, never authorization or freshness proof.
func (e *Evaluator) CapabilityNames() []string {
	out := make([]string, 0)
	for _, definition := range definitions {
		if e.capability(definition.Name) == Allowed {
			out = append(out, definition.Name)
		}
	}
	return out
}
