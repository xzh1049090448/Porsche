package authz

type scope uint8

const (
	userScope scope = 1 << iota
	createScope
	collectionScope
	resourceScope
)

// Definition describes an immutable capability from the fixed internal catalog.
type Definition struct {
	Name         string
	AdminDefault bool
	Grantable    bool
	RootOnly     bool
	Unavailable  bool
	scopes       scope
}

var definitions = [...]Definition{
	{"users.read", true, true, false, false, userScope | collectionScope},
	{"users.create", true, true, false, false, createScope},
	{"users.edit", true, true, false, false, userScope},
	{"users.enable", true, true, false, false, userScope},
	{"users.disable", true, true, false, false, userScope},
	{"users.reset_password", false, true, false, false, userScope},
	{"users.sessions.read", false, true, false, false, userScope},
	{"users.sessions.revoke", false, true, false, false, userScope},
	{"users.plan.change", false, true, false, false, userScope},
	{"users.group.change", false, true, false, false, userScope},
	{"users.quota.adjust", false, false, false, true, userScope},
	{"users.delete", false, true, false, false, userScope},
	{"users.deleted.read", false, true, false, false, userScope | collectionScope},
	{"users.promote", false, false, true, false, userScope},
	{"users.demote", false, false, true, false, userScope},
	{"users.permissions.write", false, false, true, false, userScope},
	{"users.audit.read", true, true, false, false, userScope | collectionScope},
	{"groups.read", true, true, false, false, resourceScope},
	{"groups.write", false, false, true, false, resourceScope},
	{"public_content.read", false, true, false, false, resourceScope},
	{"public_content.edit", false, true, false, false, resourceScope},
	{"public_content.preview", false, true, false, false, resourceScope},
	{"public_content.publish", false, true, false, false, resourceScope},
	{"public_content.rollback", false, true, false, false, resourceScope},
}

// Catalog returns an independent, immutable-to-the-caller capability-catalog snapshot.
func Catalog() []Definition {
	out := make([]Definition, len(definitions))
	copy(out, definitions[:])
	return out
}

func lookup(name string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return Definition{}, false
}
