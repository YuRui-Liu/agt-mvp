package mocksvc

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/upload"
)

func TestCreateTaskRequiresExactConsentAndIsIdempotent(t *testing.T) {
	service := newTestTaskService("success")
	request := validCreateTaskRequest("sub-1", "idem-1")

	first, err := service.CreateTask(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate tasks: %q %q", first.ID, second.ID)
	}

	request.IdempotencyKey = "idem-2"
	request.ConsentVersion = ""
	if _, err := service.CreateTask(request); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("error = %v", err)
	}
	request.ConsentVersion = ConsentVersion + "-other"
	if _, err := service.CreateTask(request); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("wrong consent error = %v", err)
	}
}

func TestCreateTaskRequiresTrustedExactIdentity(t *testing.T) {
	store := NewMemoryStore()
	trusted := Identity{
		SubjectID: "sub-1",
		KuAIID:    "KUAI-trusted",
		CreatedAt: fixedTaskClock()(),
	}
	if err := store.Put(trusted); err != nil {
		t.Fatal(err)
	}
	service := NewTaskService(store, fixedTaskClock(), "success", time.Second)

	missing := validCreateTaskRequest("missing", "missing")
	if _, err := service.CreateTask(missing); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("missing identity error = %v", err)
	}
	wrongKuAI := validCreateTaskRequest("sub-1", "wrong-kuai")
	if _, err := service.CreateTask(wrongKuAI); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("KuAI mismatch error = %v", err)
	}
	wrongSubject := validCreateTaskRequest("sub-other", "wrong-subject")
	wrongSubject.Identity.KuAIID = trusted.KuAIID
	if _, err := service.CreateTask(wrongSubject); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("subject mismatch error = %v", err)
	}
}

func TestCreateTaskRejectsIdempotencyConflictAndCopiesPackage(t *testing.T) {
	service := newTestTaskService("success")
	request := validCreateTaskRequest("sub-1", "idem")
	request.Package.Sessions = []upload.Session{{
		ID:    "session-1",
		Agent: "codex",
		Events: []map[string]any{{
			"message": "original",
			"nested":  map[string]any{"value": "original"},
		}},
	}}
	first, err := service.CreateTask(request)
	if err != nil {
		t.Fatal(err)
	}

	request.Package.Sessions[0].Events[0]["message"] = "mutated"
	request.Package.Sessions[0].Events[0]["nested"].(map[string]any)["value"] = "mutated"
	if _, err := service.CreateTask(request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed package error = %v", err)
	}
	stored := service.packages[first.ID].value
	if got := stored.Sessions[0].Events[0]["message"]; got != "original" {
		t.Fatalf("stored package aliased caller: %v", got)
	}
	if got := stored.Sessions[0].Events[0]["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("nested package aliased caller: %v", got)
	}

	original := validCreateTaskRequest("sub-1", "idem")
	original.Package.Sessions = []upload.Session{{
		ID: "session-1", Agent: "codex",
		Events: []map[string]any{{"message": "original", "nested": map[string]any{"value": "original"}}},
	}}
	original.Identity.KuAIID = "KUAI-other"
	if _, err := service.CreateTask(original); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed KuAI error = %v", err)
	}
}

func TestCreateTaskConcurrentIdempotencyConflictIsSafe(t *testing.T) {
	service := newTestTaskService("success")
	left := validCreateTaskRequest("sub-1", "idem")
	right := validCreateTaskRequest("sub-1", "idem")
	left.Package.Project.Label = "left"
	right.Package.Project.Label = "right"

	const count = 20
	var group sync.WaitGroup
	results := make(chan error, count)
	for index := range count {
		group.Add(1)
		go func(request CreateTaskRequest) {
			defer group.Done()
			_, err := service.CreateTask(request)
			results <- err
		}(map[bool]CreateTaskRequest{true: left, false: right}[index%2 == 0])
	}
	group.Wait()
	close(results)
	conflicts := 0
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrIdempotencyConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes == 0 || conflicts == 0 || successes+conflicts != count {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestCreateTaskIdempotencyIsScopedToSubject(t *testing.T) {
	service := newTestTaskService("success")
	first, err := service.CreateTask(validCreateTaskRequest("sub-1", "same"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(validCreateTaskRequest("sub-2", "same"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("subjects shared task %q", first.ID)
	}
}

func TestCreateTaskConcurrentRetriesReturnOneTask(t *testing.T) {
	service := newTestTaskService("success")
	request := validCreateTaskRequest("sub-1", "idem")
	const count = 16
	ids := make(chan string, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			task, err := service.CreateTask(request)
			if err != nil {
				errs <- err
				return
			}
			ids <- task.ID
		}()
	}
	group.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var expected string
	for id := range ids {
		if expected == "" {
			expected = id
		}
		if id != expected {
			t.Fatalf("id = %q, want %q", id, expected)
		}
	}
}

func TestLatestTaskIsSubjectIsolated(t *testing.T) {
	service := newTestTaskService("success")
	one, err := service.CreateTask(validCreateTaskRequest("sub-1", "one"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := service.CreateTask(validCreateTaskRequest("sub-2", "two"))
	if err != nil {
		t.Fatal(err)
	}
	latest, ok := service.LatestTask("sub-1")
	if !ok || latest.ID != one.ID || latest.ID == two.ID {
		t.Fatalf("latest = %#v, ok = %v", latest, ok)
	}
	if _, ok := service.LatestTask("missing"); ok {
		t.Fatal("missing subject had a latest task")
	}
}

func TestCleanupDeletesOnlyExpiredPackages(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := NewMemoryStore()
	for _, subjectID := range []string{"old", "boundary", "fresh"} {
		trustIdentity(t, store, subjectID)
	}
	service := NewTaskService(store, clock, "success", 0)

	oldTask, err := service.CreateTask(validCreateTaskRequest("old", "old"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	boundaryTask, err := service.CreateTask(validCreateTaskRequest("boundary", "boundary"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * 24 * time.Hour)
	freshTask, err := service.CreateTask(validCreateTaskRequest("fresh", "fresh"))
	if err != nil {
		t.Fatal(err)
	}

	service.Cleanup(now)
	if service.HasPackage(oldTask.ID) || service.HasPackage(boundaryTask.ID) {
		t.Fatal("expired package retained")
	}
	if !service.HasPackage(freshTask.ID) {
		t.Fatal("fresh package deleted")
	}
	for _, task := range []Task{oldTask, boundaryTask, freshTask} {
		got, ok := service.Task(task.SubjectID, task.ID)
		if !ok || got.Status != TaskComplete || got.Analysis == nil {
			t.Fatalf("task lost during package cleanup: %#v, ok=%v", got, ok)
		}
	}
}

func TestPublicOperationsTriggerPackageRetentionCleanup(t *testing.T) {
	operations := []struct {
		name string
		call func(*TaskService, Task)
	}{
		{"CreateTask", func(service *TaskService, _ Task) {
			trustIdentity(t, service.identityStore, "fresh")
			if _, err := service.CreateTask(validCreateTaskRequest("fresh", "fresh")); err != nil {
				t.Fatal(err)
			}
		}},
		{"Task", func(service *TaskService, old Task) { _, _ = service.Task(old.SubjectID, old.ID) }},
		{"LatestTask", func(service *TaskService, old Task) { _, _ = service.LatestTask(old.SubjectID) }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore()
			trustIdentity(t, store, "old")
			service := NewTaskService(store, func() time.Time { return now }, "success", 0)
			old, err := service.CreateTask(validCreateTaskRequest("old", "old"))
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(packageRetention)
			operation.call(service, old)
			if service.HasPackage(old.ID) {
				t.Fatal("expired package retained after public operation")
			}
			if task, ok := service.Task(old.SubjectID, old.ID); !ok || task.ID != old.ID {
				t.Fatal("cleanup deleted task metadata")
			}
		})
	}
}

func TestTaskScenarios(t *testing.T) {
	t.Run("otp error does not affect task processing", func(t *testing.T) {
		service := newTestTaskService("otp_error")
		task, err := service.CreateTask(validCreateTaskRequest("sub", "idem"))
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != TaskComplete {
			t.Fatalf("task=%#v", task)
		}
	})
	t.Run("upload error creates no task", func(t *testing.T) {
		service := newTestTaskService("upload_error")
		if _, err := service.CreateTask(validCreateTaskRequest("sub", "idem")); !errors.Is(err, ErrUploadRetryable) {
			t.Fatalf("error = %v", err)
		}
		if _, ok := service.LatestTask("sub"); ok {
			t.Fatal("upload error created task")
		}
	})
	t.Run("analysis error fails task", func(t *testing.T) {
		service := newTestTaskService("analysis_error")
		task, err := service.CreateTask(validCreateTaskRequest("sub", "idem"))
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != TaskFailed || task.ErrorCode != "analysis_error" {
			t.Fatalf("task = %#v", task)
		}
	})
	t.Run("success is qualitative", func(t *testing.T) {
		service := newTestTaskService("success")
		task, err := service.CreateTask(validCreateTaskRequest("sub", "idem"))
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != TaskComplete || task.Analysis == nil || task.Analysis.Headline == "" {
			t.Fatalf("task = %#v", task)
		}
	})
	t.Run("slow starts queued without waiting", func(t *testing.T) {
		service := NewTaskService(NewMemoryStore(), fixedTaskClock(), "slow", time.Hour)
		trustIdentity(t, service.identityStore, "sub")
		task, err := service.CreateTask(validCreateTaskRequest("sub", "idem"))
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != TaskQueued {
			t.Fatalf("status = %q", task.Status)
		}
		service.CompletePending()
		task, ok := service.Task("sub", task.ID)
		if !ok || task.Status != TaskComplete {
			t.Fatalf("task = %#v, ok=%v", task, ok)
		}
	})
}

func TestSlowTaskCompletesInBackgroundAndCloseStopsTimers(t *testing.T) {
	t.Run("background completion", func(t *testing.T) {
		store := NewMemoryStore()
		trustIdentity(t, store, "sub")
		service := NewTaskService(store, time.Now, "slow", 5*time.Millisecond)
		defer service.Close()
		task, err := service.CreateTask(validCreateTaskRequest("sub", "idem"))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(250 * time.Millisecond)
		for {
			current, ok := service.Task("sub", task.ID)
			if ok && current.Status == TaskComplete {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("task did not complete: %#v", current)
			}
			time.Sleep(time.Millisecond)
		}
		service.mu.RLock()
		timerCount := len(service.timers)
		service.mu.RUnlock()
		if timerCount != 0 {
			t.Fatalf("completed timer retained: %d", timerCount)
		}
	})

	t.Run("close prevents completion", func(t *testing.T) {
		store := NewMemoryStore()
		trustIdentity(t, store, "sub")
		service := NewTaskService(store, time.Now, "slow", 50*time.Millisecond)
		task, err := service.CreateTask(validCreateTaskRequest("sub", "idem"))
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
		if err := service.Close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
		time.Sleep(70 * time.Millisecond)
		current, ok := service.Task("sub", task.ID)
		if !ok || current.Status != TaskQueued {
			t.Fatalf("closed task changed: %#v, ok=%v", current, ok)
		}
		service.mu.RLock()
		timerCount := len(service.timers)
		service.mu.RUnlock()
		if timerCount != 0 {
			t.Fatalf("close retained timers: %d", timerCount)
		}
	})
}

func newTestTaskService(scenario string) *TaskService {
	store := NewMemoryStore()
	for _, subjectID := range []string{"sub-1", "sub-2", "sub", "old", "boundary", "fresh"} {
		trustIdentity(nil, store, subjectID)
	}
	return NewTaskService(store, fixedTaskClock(), scenario, 30*time.Second)
}

func fixedTaskClock() Clock {
	return func() time.Time {
		return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	}
}

func validCreateTaskRequest(subjectID, idempotencyKey string) CreateTaskRequest {
	return CreateTaskRequest{
		Identity: Identity{SubjectID: subjectID, KuAIID: "KUAI-" + subjectID, CreatedAt: fixedTaskClock()()},
		Package: upload.Package{
			SchemaVersion: 1,
			Project:       upload.Project{Key: "project", Label: "Project"},
			CreatedAt:     fixedTaskClock()(),
		},
		ConsentVersion: ConsentVersion,
		IdempotencyKey: idempotencyKey,
	}
}

func trustIdentity(t *testing.T, store IdentityStore, subjectID string) {
	err := store.Put(Identity{
		SubjectID: subjectID,
		KuAIID:    "KUAI-" + subjectID,
		CreatedAt: fixedTaskClock()(),
	})
	if err != nil {
		if t == nil {
			panic(err)
		}
		t.Fatal(err)
	}
}
