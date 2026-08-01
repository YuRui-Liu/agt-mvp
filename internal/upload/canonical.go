package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"
)

const maxCanonicalDepth = 64

// CanonicalBytes returns the deterministic JSON representation used by kuai
// package integrity checks. It is deliberately a small, package-specific
// canonical form and is not an implementation of RFC 8785.
func CanonicalBytes(pkg Package) ([]byte, error) {
	if pkg.SchemaVersion != 2 {
		return nil, errors.New("upload: canonical encoding requires schema version 2")
	}
	if pkg.Project != (Project{}) {
		return nil, errors.New("upload: legacy project is not valid in schema version 2")
	}
	normalized := pkg
	normalized.CreatedAt = normalized.CreatedAt.UTC()
	if normalized.Sessions == nil {
		normalized.Sessions = []Session{}
	}
	for index := range normalized.Sessions {
		session := &normalized.Sessions[index]
		if session.Agent != "" {
			return nil, errors.New("upload: legacy agent is not valid in schema version 2")
		}
		if session.Events == nil {
			session.Events = []map[string]any{}
		}
		if session.Source.Capabilities == nil {
			session.Source.Capabilities = []string{}
		}
		for _, event := range session.Events {
			if err := validateStableJSON(reflect.ValueOf(event), 0); err != nil {
				return nil, err
			}
		}
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	wire := struct {
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
		SchemaVersion: normalized.SchemaVersion,
		Client:        normalized.Client,
		Scope:         normalized.Scope,
		Sessions:      normalized.Sessions,
		CreatedAt:     normalized.CreatedAt,
	}
	wire.Redaction.Replacements = normalized.Redaction.Replacements
	wire.Redaction.OmittedReads = normalized.Redaction.OmittedReads
	wire.Redaction.RemovedFields = normalized.Redaction.RemovedFields
	if err := encoder.Encode(wire); err != nil {
		return nil, errors.New("upload: canonical encoding failed")
	}
	body := bytes.TrimSuffix(output.Bytes(), []byte{'\n'})
	return append([]byte(nil), body...), nil
}

func Digest(pkg Package) (string, int64, error) {
	body, err := CanonicalBytes(pkg)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), int64(len(body)), nil
}

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	timeType          = reflect.TypeOf(time.Time{})
)

func validateStableJSON(value reflect.Value, depth int) error {
	if depth > maxCanonicalDepth {
		return errors.New("upload: canonical value nesting invalid")
	}
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateStableJSON(value.Elem(), depth+1)
	}
	if value.Type() != timeType &&
		(value.Type().Implements(jsonMarshalerType) || value.Type().Implements(textMarshalerType) ||
			reflect.PointerTo(value.Type()).Implements(jsonMarshalerType) ||
			reflect.PointerTo(value.Type()).Implements(textMarshalerType)) {
		return errors.New("upload: custom marshaler is not stable")
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String || value.Type().Key() != reflect.TypeOf("") {
			return errors.New("upload: canonical maps require string keys")
		}
		for _, key := range value.MapKeys() {
			if err := validateStableJSON(value.MapIndex(key), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateStableJSON(value.Index(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("upload: canonical number is not finite")
		}
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Invalid:
		return nil
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return validateStableJSON(value.Elem(), depth+1)
	default:
		return errors.New("upload: canonical value type unsupported")
	}
	return nil
}
