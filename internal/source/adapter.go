package source

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
)

// Adapter discovers and opens sessions belonging to one product.
type Adapter interface {
	Product() string
	Capabilities() []Capability
	Discover(context.Context) ([]Session, error)
	Open(context.Context, Session) (io.ReadCloser, error)
}

type SourceState string

const (
	SourceReady               SourceState = "ready"
	SourceNotFound            SourceState = "not_found"
	SourceExportRequired      SourceState = "export_required"
	SourceFormatUnsupported   SourceState = "format_unsupported"
	SourceReadError           SourceState = "read_error"
	SourceDetectedUnsupported SourceState = "detected_unsupported"

	// SourceFailed is retained for source compatibility. New callers should
	// distinguish SourceReadError from the other source states.
	SourceFailed = SourceReadError
)

// SourceStatus reports discovery health without exposing adapter error text.
type SourceStatus struct {
	State SourceState `json:"state"`
	Code  string      `json:"code,omitempty"`
	// Error temporarily mirrors Code for callers migrating from the old status
	// model. It is never serialized and never contains adapter error text.
	Error string `json:"-"`
}

// DiscoveryError lets an adapter declare one of the two expected discovery
// limitations without exposing its underlying error to callers.
type DiscoveryError struct {
	state SourceState
	err   error
}

func NewDiscoveryError(state SourceState, err error) error {
	return &DiscoveryError{state: state, err: err}
}

func (e *DiscoveryError) Error() string {
	if e == nil || e.err == nil {
		return "source discovery failed"
	}
	return e.err.Error()
}

func (e *DiscoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type ScanResult struct {
	Sessions []Session               `json:"sessions"`
	Sources  map[string]SourceStatus `json:"sources"`
}

type Registry struct {
	adapters []Adapter
	products []string
}

func NewRegistry(adapters ...Adapter) *Registry {
	filtered := make([]Adapter, 0, len(adapters))
	for _, adapter := range adapters {
		if isNilAdapter(adapter) {
			continue
		}
		filtered = append(filtered, adapter)
	}
	products := make([]string, len(filtered))
	for index, adapter := range filtered {
		products[index] = strings.TrimSpace(adapter.Product())
	}
	return &Registry{adapters: filtered, products: products}
}

func isNilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type adapterSnapshot struct {
	adapter Adapter
	product string
	valid   bool
}

// Scan isolates source failures and returns stable, de-duplicated session data.
func (r *Registry) Scan(ctx context.Context) (ScanResult, error) {
	if err := ctx.Err(); err != nil {
		return ScanResult{}, err
	}

	result := ScanResult{Sources: make(map[string]SourceStatus, len(r.adapters))}
	snapshots := make([]adapterSnapshot, 0, len(r.adapters))
	productCounts := make(map[string]int, len(r.adapters))
	for index, adapter := range r.adapters {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, err
		}
		product := r.products[index]
		valid := validProduct(product)
		snapshots = append(snapshots, adapterSnapshot{adapter: adapter, product: product, valid: valid})
		if !valid {
			result.Sources[product] = sourceErrorStatus(SourceReadError, "invalid_product")
			continue
		}
		productCounts[product]++
	}
	for product, count := range productCounts {
		if count > 1 {
			result.Sources[product] = sourceErrorStatus(SourceReadError, "duplicate_product")
		}
	}

	for _, snapshot := range snapshots {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, err
		}
		if !snapshot.valid {
			continue
		}
		product := snapshot.product
		if productCounts[product] > 1 {
			continue
		}
		discovered, err := snapshot.adapter.Discover(ctx)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ScanResult{}, ctxErr
		}
		if err != nil {
			result.Sources[product] = classifyDiscoveryError(err)
			continue
		}

		productSessions := make([]Session, 0, len(discovered))
		seen := make(map[string]Session, len(discovered))
		invalid := false
		adapterCapabilities := append([]Capability(nil), snapshot.adapter.Capabilities()...)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ScanResult{}, ctxErr
		}
		for _, session := range discovered {
			session.Product = product
			if session.Capabilities == nil {
				session.Capabilities = append([]Capability(nil), adapterCapabilities...)
			} else {
				session.Capabilities = append([]Capability(nil), session.Capabilities...)
			}
			if session.ID == "" {
				invalid = true
				break
			}
			previous, exists := seen[session.ID]
			if exists {
				if !reflect.DeepEqual(previous, session) {
					invalid = true
					break
				}
				continue
			}
			seen[session.ID] = session
			productSessions = append(productSessions, session)
		}
		if invalid {
			result.Sources[product] = sourceErrorStatus(SourceReadError, "invalid_session")
			continue
		}
		state := SourceReady
		if len(productSessions) == 0 {
			state = SourceNotFound
		}
		result.Sources[product] = SourceStatus{State: state}
		result.Sessions = append(result.Sessions, productSessions...)
	}

	sort.Slice(result.Sessions, func(i, j int) bool {
		if result.Sessions[i].Product != result.Sessions[j].Product {
			return result.Sessions[i].Product < result.Sessions[j].Product
		}
		return result.Sessions[i].ID < result.Sessions[j].ID
	})
	if err := ctx.Err(); err != nil {
		return ScanResult{}, err
	}
	return result, nil
}

func classifyDiscoveryError(err error) SourceStatus {
	var discoveryError *DiscoveryError
	if errors.As(err, &discoveryError) && discoveryError != nil {
		switch discoveryError.state {
		case SourceFormatUnsupported, SourceExportRequired:
			return sourceErrorStatus(discoveryError.state, string(discoveryError.state))
		}
	}
	return sourceErrorStatus(SourceReadError, "read_failed")
}

func sourceErrorStatus(state SourceState, code string) SourceStatus {
	return SourceStatus{State: state, Code: code, Error: code}
}

// Open routes a previously discovered session to its exact product adapter.
// Registry metadata is immutable after construction, so Scan and Open are safe
// to call concurrently when the adapters themselves honor their interface
// contract.
func (r *Registry) Open(ctx context.Context, session Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || !validProduct(session.Product) {
		return nil, errors.New("source: session product unavailable")
	}
	match := -1
	for index, product := range r.products {
		if product != session.Product {
			continue
		}
		if match >= 0 {
			return nil, errors.New("source: session product unavailable")
		}
		match = index
	}
	if match < 0 {
		return nil, errors.New("source: session product unavailable")
	}
	reader, err := r.adapters[match].Open(ctx, cloneSession(session))
	if ctxErr := ctx.Err(); ctxErr != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, ctxErr
	}
	if err != nil || reader == nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, errors.New("source: session open failed")
	}
	return reader, nil
}

func validProduct(product string) bool {
	if product == "" || !lowerLetterOrDigit(product[0]) {
		return false
	}
	for index := 1; index < len(product); index++ {
		if product[index] != '-' && !lowerLetterOrDigit(product[index]) {
			return false
		}
	}
	return true
}

func lowerLetterOrDigit(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}
