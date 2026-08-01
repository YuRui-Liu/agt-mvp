package source

import "time"

// Capability identifies an operation or data feature exposed by a source.
type Capability string

// ScopeType identifies the namespace used to group related sessions.
type ScopeType string

const (
	ScopeProject           ScopeType = "project"
	ScopeWorkspace         ScopeType = "workspace"
	ScopeConversationGroup ScopeType = "conversation_group"
	ScopeSessionCollection ScopeType = "session_collection"
)

// ScopeRef is the source-private reference used to group a session.
type ScopeRef struct {
	Type  ScopeType
	Root  string
	Label string
}

// Session is source-neutral metadata for a locally discoverable agent session.
type Session struct {
	ID             string
	Product        string
	FormatVersion  string
	AdapterVersion string
	Capabilities   []Capability
	Scope          ScopeRef
	StartedAt      time.Time
	EndedAt        time.Time
	MessageCount   int
	ParentID       string
	Usage          map[string]int64
	// MalformedCount counts non-empty invalid JSON records and recognized
	// message records whose required envelope/content is invalid.
	MalformedCount int
	OpaqueRef      string
	SnapshotID     string
}

// Scope is a privacy-preserving assessment grouping. Sessions remain available
// to server-side callers but are deliberately excluded from browser JSON.
type Scope struct {
	Key          string    `json:"key"`
	Type         ScopeType `json:"type"`
	Label        string    `json:"label"`
	SessionCount int       `json:"sessionCount"`
	Products     []string  `json:"products"`
	Sessions     []Session `json:"-"`
}

func cloneSession(session Session) Session {
	session.Capabilities = append([]Capability(nil), session.Capabilities...)
	if session.Usage != nil {
		original := session.Usage
		session.Usage = map[string]int64{}
		for k, v := range original {
			session.Usage[k] = v
		}
	}
	return session
}
