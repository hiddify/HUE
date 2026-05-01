package auth

import "github.com/google/uuid"

// parseUUID is tolerant: any malformed string yields uuid.Nil. Callers that
// require a parseable owner_id should validate at issuance time.
func parseUUID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}
