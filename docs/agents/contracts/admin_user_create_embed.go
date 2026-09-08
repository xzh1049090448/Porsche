package contracts

import _ "embed"

// AdminUserCreateV1 is the canonical A03 contract, embedded at test-build time.
//
//go:embed admin-user-create-v1.json
var AdminUserCreateV1 []byte
