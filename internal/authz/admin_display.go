package authz

import "github.com/porsche/ai-gateway-go/internal/models"

// AdminDisplayCapability is a display hint; it carries no evaluator or authority.
type AdminDisplayCapability struct {
	Name            string
	Baseline        bool
	Override        Effect
	PolicyEffective bool
	Effective       bool
}

// ProjectAdminPolicy accepts active or disabled Admin targets without promoting
// a disabled account into an authorization actor.
func ProjectAdminPolicy(target Account, rules []Override) ([]AdminDisplayCapability, error) {
	if !validAccount(target) || target.IsDeleted != 0 || target.Role != models.UserRoleAdmin {
		return nil, ErrInvalidActor
	}
	overrides, err := validateOverrides(rules)
	if err != nil {
		return nil, err
	}
	result := make([]AdminDisplayCapability, 0, len(definitions))
	for _, d := range definitions {
		effect := overrides[d.Name]
		if effect == "" {
			effect = Inherit
		}
		policy := d.AdminDefault
		if effect == Deny {
			policy = false
		} else if effect == Allow {
			policy = d.Grantable
		}
		if d.RootOnly || d.Unavailable {
			policy = false
		}
		result = append(result, AdminDisplayCapability{Name: d.Name, Baseline: d.AdminDefault, Override: effect, PolicyEffective: policy, Effective: target.Status == models.UserStatusActive && policy})
	}
	return result, nil
}
