package actionsecurity

type Action int

const (
	ActionUsersCreateAdmin      Action = 1
	ActionUsersResetPassword    Action = 2
	ActionUsersPromote          Action = 3
	ActionUsersDemote           Action = 4
	ActionUsersPermissionsWrite Action = 5
	ActionUsersDelete           Action = 6
	ActionPublicContentPublish  Action = 7
	ActionPublicContentRollback Action = 8
)

type TargetKind int

const (
	TargetNone          TargetKind = 1
	TargetUser          TargetKind = 2
	TargetPublicContent TargetKind = 3
)

type Descriptor struct {
	Action         Action
	Name           string
	Capability     string
	RootOnly       bool
	RequiresTicket bool
	Active         bool
	TargetKind     TargetKind
	// Encode returns canonical HMAC input. Callers must clear the returned
	// buffer immediately after computing its digest because password intents
	// intentionally include the raw password bytes.
	Encode func(any) ([]byte, error)
}
