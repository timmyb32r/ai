package storage

import (
	"fmt"
	"sync"
	"time"

	"github.com/timmyb32r/yt2srt/internal/models"
)

// InMemoryStore holds transcription jobs with concurrent access protection.
type InMemoryStore struct {
	mu   sync.RWMutex
	jobs map[string]*models.Job
}

// NewInMemoryStore creates a new job store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		jobs: make(map[string]*models.Job),
	}
}

// Create adds a new job to the store.
func (s *InMemoryStore) Create(job *models.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

// Get returns a job by ID (nil if not found).
func (s *InMemoryStore) Get(id string) *models.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jobs[id]
}

// Update modifies an existing job's mutable fields.
// Returns error if the job does not exist.
func (s *InMemoryStore) Update(id string, fn func(*models.Job)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job %q not found", id)
	}
	fn(job)
	job.UpdatedAt = time.Now()
	return nil
}

// Delete removes a job from the store.
func (s *InMemoryStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

// CleanupOlderThan removes all jobs older than the given duration.
func (s *InMemoryStore) CleanupOlderThan(age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-age)
	for id, job := range s.jobs {
		if job.CreatedAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}
