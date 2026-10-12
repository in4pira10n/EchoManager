package server

type processIdentity struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	StartToken string `json:"startToken"`
	StartedAt  string `json:"startedAt"`
}

func appendUniqueIdentity(items []processIdentity, identity processIdentity) []processIdentity {
	for _, existing := range items {
		if existing.PID == identity.PID {
			return items
		}
	}
	return append(items, identity)
}
