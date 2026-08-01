package mocksvc

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrTicketInvalid = errors.New("ticket invalid")
	ErrTicketExpired = errors.New("ticket expired")
	ErrTicketUsed    = errors.New("ticket already used")
)

type ApplicationTicket struct {
	TaskID    string    `json:"task_id"`
	SubjectID string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Application struct {
	ID        string
	TaskID    string
	SubjectID string
	Name      string
	Email     string
	Position  string
	CreatedAt time.Time
}

type ticketEntry struct {
	ApplicationTicket
}

const (
	ticketStateCapacity      = 1024
	applicationStateCapacity = 1024
	applicationRetention     = 30 * 24 * time.Hour
)

type TicketService struct {
	clock Clock
	ttl   time.Duration

	mu           sync.Mutex
	entries      map[string]ticketEntry
	used         map[string]time.Time
	applications map[string]Application
	randomID     func() (string, error)
}

func NewTicketService(clock Clock, ttl time.Duration) *TicketService {
	if clock == nil {
		clock = time.Now
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &TicketService{
		clock:        clock,
		ttl:          ttl,
		entries:      make(map[string]ticketEntry),
		used:         make(map[string]time.Time),
		applications: make(map[string]Application),
		randomID:     randomID,
	}
}

func (s *TicketService) Issue(taskID, subjectID string) (string, error) {
	if s == nil {
		return "", ErrTicketInvalid
	}
	return s.issue(taskID, subjectID, s.clock().Add(s.ttl))
}

func (s *TicketService) IssueForScenario(taskID, subjectID, scenario string) (string, error) {
	if s == nil {
		return "", ErrTicketInvalid
	}
	if scenario == "ticket_error" {
		return s.issue(taskID, subjectID, s.clock())
	}
	switch scenario {
	case "success", "slow", "upload_error", "analysis_error", "otp_error":
		return s.Issue(taskID, subjectID)
	default:
		return "", ErrUnknownScenario
	}
}

func (s *TicketService) Validate(value string) (ApplicationTicket, error) {
	if s == nil || value == "" {
		return ApplicationTicket{}, ErrTicketInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookupLocked(value, s.clock())
}

func (s *TicketService) Consume(value string) (ApplicationTicket, error) {
	if s == nil || value == "" {
		return ApplicationTicket{}, ErrTicketInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, err := s.lookupLocked(value, s.clock())
	if err != nil {
		return ApplicationTicket{}, err
	}
	delete(s.entries, value)
	s.used[value] = ticket.ExpiresAt
	return ticket, nil
}

func (s *TicketService) Submit(value, name, email, position string) (Application, error) {
	if s == nil || value == "" {
		return Application{}, ErrTicketInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, err := s.lookupLocked(value, s.clock())
	if err != nil {
		return Application{}, err
	}
	if len(s.applications) >= applicationStateCapacity {
		return Application{}, ErrCapacityExceeded
	}
	applicationID, err := s.randomID()
	if err != nil {
		return Application{}, err
	}
	application := Application{
		ID: applicationID, TaskID: ticket.TaskID, SubjectID: ticket.SubjectID,
		Name: name, Email: email, Position: position, CreatedAt: s.clock(),
	}
	delete(s.entries, value)
	s.used[value] = ticket.ExpiresAt
	s.applications[application.ID] = application
	return application, nil
}

func (s *TicketService) GetApplication(applicationID string) (Application, bool) {
	if s == nil || applicationID == "" {
		return Application{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.clock())
	application, ok := s.applications[applicationID]
	return application, ok
}

func (s *TicketService) Revoke(value string) {
	if s == nil || value == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, value)
	s.mu.Unlock()
}

func (s *TicketService) issue(taskID, subjectID string, expiresAt time.Time) (string, error) {
	if s == nil || taskID == "" || subjectID == "" {
		return "", ErrTicketInvalid
	}
	value, err := randomID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.clock())
	if len(s.entries)+len(s.used) >= ticketStateCapacity {
		return "", ErrCapacityExceeded
	}
	s.entries[value] = ticketEntry{
		ApplicationTicket: ApplicationTicket{
			TaskID:    taskID,
			SubjectID: subjectID,
			ExpiresAt: expiresAt,
		},
	}
	return value, nil
}

func (s *TicketService) lookupLocked(value string, now time.Time) (ApplicationTicket, error) {
	entry, exists := s.entries[value]
	usedUntil, wasUsed := s.used[value]
	s.cleanupLocked(now)
	if exists {
		if !now.Before(entry.ExpiresAt) {
			return ApplicationTicket{}, ErrTicketExpired
		}
		return entry.ApplicationTicket, nil
	}
	if wasUsed && now.Before(usedUntil) {
		return ApplicationTicket{}, ErrTicketUsed
	}
	return ApplicationTicket{}, ErrTicketInvalid
}

func (s *TicketService) cleanupLocked(now time.Time) {
	for value, entry := range s.entries {
		if !now.Before(entry.ExpiresAt) {
			delete(s.entries, value)
		}
	}
	for value, expiresAt := range s.used {
		if !now.Before(expiresAt) {
			delete(s.used, value)
		}
	}
	for applicationID, application := range s.applications {
		if !now.Before(application.CreatedAt.Add(applicationRetention)) {
			delete(s.applications, applicationID)
		}
	}
}
