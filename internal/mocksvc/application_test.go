package mocksvc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestApplicationPIINeverEntersIdentityFileStore(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "private", "state.json")
	if _, err := OpenFileStore(statePath); err != nil {
		t.Fatal(err)
	}
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Submit(
		value, "Candidate Private", "candidate-private@example.com", "Secret Position",
	); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, pii := range [][]byte{
		[]byte("Candidate Private"),
		[]byte("candidate-private@example.com"),
		[]byte("Secret Position"),
	} {
		if bytes.Contains(state, pii) {
			t.Fatalf("identity state contains application PII %q", pii)
		}
	}
}

func TestApplicationTicketValidateDoesNotConsume(t *testing.T) {
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := tickets.Validate(value)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tickets.Validate(value)
	if err != nil {
		t.Fatal(err)
	}
	if first.TaskID != "task-1" || first.SubjectID != "sub-1" || second != first {
		t.Fatalf("tickets = %#v %#v", first, second)
	}
	if _, err := tickets.Consume(value); err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(value); !errors.Is(err, ErrTicketUsed) {
		t.Fatalf("second consume = %v", err)
	}
}

func TestApplicationTicketRevokeInvalidatesAndFreesCapacity(t *testing.T) {
	service := NewTicketService(fixedTaskClock(), time.Hour)
	ticket, err := service.Issue("task-1", "subject-1")
	if err != nil {
		t.Fatal(err)
	}
	service.Revoke(ticket)
	if _, err := service.Validate(ticket); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("revoked ticket err = %v", err)
	}
	service.mu.Lock()
	if len(service.entries) != 0 {
		t.Fatalf("revoked entry retained: %#v", service.entries)
	}
	service.mu.Unlock()
}

func TestApplicationTicketExpiresAfterTenMinutes(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tickets := NewTicketService(func() time.Time { return now }, 10*time.Minute)
	consumeTickets := NewTicketService(func() time.Time { return now }, 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	consumeValue, err := consumeTickets.Issue("task-2", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if _, err := tickets.Validate(value); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("validate error = %v", err)
	}
	if _, err := consumeTickets.Consume(consumeValue); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("consume error = %v", err)
	}
}

func TestApplicationTicketConsumeIsConcurrentSingleUse(t *testing.T) {
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	const consumers = 24
	var group sync.WaitGroup
	results := make(chan error, consumers)
	for range consumers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := tickets.Consume(value)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	successes := 0
	used := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrTicketUsed):
			used++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || used != consumers-1 {
		t.Fatalf("successes=%d used=%d", successes, used)
	}
}

func TestTicketErrorScenarioIssuesExpiredTicket(t *testing.T) {
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.IssueForScenario("task-1", "sub-1", "ticket_error")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Validate(value); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("error = %v", err)
	}
}

func TestTicketStateIsBoundedAndExpiredEntriesAreCleaned(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tickets := NewTicketService(func() time.Time { return now }, time.Minute)
	for index := 0; index < ticketStateCapacity; index++ {
		value, err := tickets.Issue("task", "subject")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tickets.Consume(value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tickets.Issue("task", "subject"); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("capacity error = %v", err)
	}
	tickets.mu.Lock()
	size := len(tickets.entries) + len(tickets.used)
	tickets.mu.Unlock()
	if size > ticketStateCapacity {
		t.Fatalf("ticket state grew to %d", size)
	}

	now = now.Add(time.Minute)
	if _, err := tickets.Issue("task", "subject"); err != nil {
		t.Fatalf("expired state was not cleaned: %v", err)
	}
	tickets.mu.Lock()
	size = len(tickets.entries) + len(tickets.used)
	tickets.mu.Unlock()
	if size != 1 {
		t.Fatalf("state after cleanup = %d", size)
	}
}

func TestTicketIssueIsNilSafeAndRejectsUnknownScenario(t *testing.T) {
	var tickets *TicketService
	if _, err := tickets.Issue("task", "subject"); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("nil issue error = %v", err)
	}
	if _, err := tickets.IssueForScenario("task", "subject", "success"); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("nil scenario issue error = %v", err)
	}

	tickets = NewTicketService(fixedTaskClock(), time.Minute)
	if _, err := tickets.IssueForScenario("task", "subject", "unknown"); !errors.Is(err, ErrUnknownScenario) {
		t.Fatalf("unknown scenario error = %v", err)
	}
}

func TestSubmitAtomicallyStoresTrustedApplication(t *testing.T) {
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	application, err := tickets.Submit(value, "Candidate", "candidate@example.com", "Engineer")
	if err != nil {
		t.Fatal(err)
	}
	if application.ID == "" || application.TaskID != "task-1" || application.SubjectID != "sub-1" ||
		application.Name != "Candidate" || application.Email != "candidate@example.com" ||
		application.Position != "Engineer" || application.CreatedAt != fixedTaskClock()() {
		t.Fatalf("application=%#v", application)
	}
	stored, ok := tickets.GetApplication(application.ID)
	if !ok || stored != application {
		t.Fatalf("stored=%#v ok=%v", stored, ok)
	}
	if _, err := tickets.Submit(value, "Other", "other@example.com", "Other"); !errors.Is(err, ErrTicketUsed) {
		t.Fatalf("second submit error=%v", err)
	}
}

func TestSubmitRandomFailureDoesNotConsumeTicket(t *testing.T) {
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	tickets.randomID = func() (string, error) { return "", errors.New("entropy unavailable") }
	if _, err := tickets.Submit(value, "Candidate", "candidate@example.com", "Engineer"); err == nil {
		t.Fatal("Submit succeeded")
	}
	if _, err := tickets.Validate(value); err != nil {
		t.Fatalf("ticket consumed on random failure: %v", err)
	}
}

func TestApplicationsExpireAndReleasePII(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tickets := NewTicketService(func() time.Time { return now }, 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	application, err := tickets.Submit(value, "Candidate", "candidate@example.com", "Engineer")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(applicationRetention)
	if stored, ok := tickets.GetApplication(application.ID); ok {
		t.Fatalf("expired application retained: %#v", stored)
	}
}

func TestApplicationCapacityFailurePreservesTicketForRetry(t *testing.T) {
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	tickets.mu.Lock()
	for index := range applicationStateCapacity {
		id := fmt.Sprintf("application-%d", index)
		tickets.applications[id] = Application{ID: id, CreatedAt: fixedTaskClock()()}
	}
	tickets.mu.Unlock()

	if _, err := tickets.Submit(value, "Candidate", "candidate@example.com", "Engineer"); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("capacity error=%v", err)
	}
	if _, err := tickets.Validate(value); err != nil {
		t.Fatalf("capacity failure consumed retryable ticket: %v", err)
	}
}

func TestSubmitIsConcurrentSingleUse(t *testing.T) {
	tickets := NewTicketService(fixedTaskClock(), 10*time.Minute)
	value, err := tickets.Issue("task-1", "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	const count = 24
	var group sync.WaitGroup
	results := make(chan error, count)
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := tickets.Submit(value, "Candidate", "candidate@example.com", "Engineer")
			results <- err
		}()
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrTicketUsed) {
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d want 1", successes)
	}
}
