package upload

import (
	"encoding/json"
	"time"
)

type Package struct {
	SchemaVersion int       `json:"schema_version"`
	Client        Client    `json:"client"`
	Scope         Scope     `json:"scope"`
	Sessions      []Session `json:"sessions"`
	Redaction     Stats     `json:"redaction"`
	CreatedAt     time.Time `json:"created_at"`
	// Project is retained only for decoding and serving legacy prepared v1
	// packages. Canonical v2 serialization rejects it when populated.
	Project Project `json:"project,omitempty"`
}

type Client struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

type Scope struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Label string `json:"label"`
}

type Project struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type Session struct {
	ID     string           `json:"id"`
	Source Source           `json:"source"`
	Events []map[string]any `json:"events"`
	// Agent is the v1 compatibility field.
	Agent string `json:"agent,omitempty"`
}

type Source struct {
	Product        string   `json:"product"`
	FormatVersion  string   `json:"format_version"`
	AdapterVersion string   `json:"adapter_version"`
	Capabilities   []string `json:"capabilities"`
}

type Stats struct {
	Replacements  int `json:"replacements"`
	RemovedFields int `json:"removed_fields"`
	DroppedFields int `json:"dropped_fields"`
	OmittedReads  int `json:"omitted_reads"`
}

func (p Package) MarshalJSON() ([]byte, error) {
	if p.SchemaVersion == 1 {
		return json.Marshal(struct {
			SchemaVersion int       `json:"schema_version"`
			Project       Project   `json:"project"`
			Sessions      []Session `json:"sessions"`
			Redaction     struct {
				Replacements  int `json:"replacements"`
				DroppedFields int `json:"dropped_fields"`
				OmittedReads  int `json:"omitted_reads"`
			} `json:"redaction"`
			CreatedAt time.Time `json:"created_at"`
		}{
			SchemaVersion: p.SchemaVersion,
			Project:       p.Project,
			Sessions:      p.Sessions,
			Redaction: struct {
				Replacements  int `json:"replacements"`
				DroppedFields int `json:"dropped_fields"`
				OmittedReads  int `json:"omitted_reads"`
			}{p.Redaction.Replacements, p.Redaction.DroppedFields, p.Redaction.OmittedReads},
			CreatedAt: p.CreatedAt,
		})
	}
	return json.Marshal(struct {
		SchemaVersion int       `json:"schema_version"`
		Client        Client    `json:"client"`
		Scope         Scope     `json:"scope"`
		Sessions      []Session `json:"sessions"`
		Redaction     struct {
			Replacements  int `json:"replacements"`
			OmittedReads  int `json:"omitted_reads"`
			RemovedFields int `json:"removed_fields"`
		} `json:"redaction"`
		CreatedAt time.Time `json:"created_at"`
	}{
		SchemaVersion: p.SchemaVersion,
		Client:        p.Client,
		Scope:         p.Scope,
		Sessions:      p.Sessions,
		Redaction: struct {
			Replacements  int `json:"replacements"`
			OmittedReads  int `json:"omitted_reads"`
			RemovedFields int `json:"removed_fields"`
		}{p.Redaction.Replacements, p.Redaction.OmittedReads, p.Redaction.RemovedFields},
		CreatedAt: p.CreatedAt,
	})
}

func (s Session) MarshalJSON() ([]byte, error) {
	if s.Agent != "" && s.Source.Product == "" && s.Source.FormatVersion == "" &&
		s.Source.AdapterVersion == "" && len(s.Source.Capabilities) == 0 {
		return json.Marshal(struct {
			ID     string           `json:"id"`
			Agent  string           `json:"agent"`
			Events []map[string]any `json:"events"`
		}{s.ID, s.Agent, s.Events})
	}
	type sessionAlias Session
	return json.Marshal(sessionAlias(s))
}

type Limits struct {
	MaxLineBytes    int64
	MaxSessionBytes int64
	MaxPackageBytes int64
}
