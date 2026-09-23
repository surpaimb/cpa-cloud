// Package scheduling provides a credential-free, in-process account lease
// scheduler. Persistence, authorization, and model execution remain callers'
// responsibilities.
package scheduling

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ReasonCode string

const (
	ReasonAcquired            ReasonCode = "acquired"
	ReasonInvalidRequest      ReasonCode = "invalid_request"
	ReasonNoAllowedAccount    ReasonCode = "no_allowed_account"
	ReasonNoCompatibleAccount ReasonCode = "no_compatible_account"
	ReasonCapacityUnavailable ReasonCode = "capacity_unavailable"
	ReasonQueueFull           ReasonCode = "queue_full"
	ReasonCancelled           ReasonCode = "cancelled"
	ReasonReleased            ReasonCode = "released"
	ReasonAlreadyReleased     ReasonCode = "already_released"
)

type FailureClass string

const (
	FailureNone       FailureClass = "none"
	FailureRateLimit  FailureClass = "rate_limited"
	FailureOverloaded FailureClass = "overloaded"
	FailureTransient  FailureClass = "transient"
	FailureAuth       FailureClass = "authentication"
	FailurePermanent  FailureClass = "permanent"
)

type Candidate struct {
	ID            string
	Provider      string
	Models        []string
	Enabled       bool
	Priority      int
	Weight        int
	Capacity      int
	CooldownUntil time.Time
}

// Request contains routing metadata only. AllowedAccountIDs must be the
// explicit result of the caller's employee/model authorization checks.
type Request struct {
	Provider          string
	Model             string
	AllowedAccountIDs []string
	StickyKey         string
	Candidates        []Candidate
}

type Decision struct {
	Code           ReasonCode
	AccountID      string
	RetrySuggested bool
}

type ReleaseResult struct {
	Failure            FailureClass
	StreamCommitted    bool
	ExecutionUncertain bool
}

type LeaseSnapshot struct {
	LeaseID   string
	AccountID string
	ExpiresAt time.Time
}

type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type Random interface {
	Intn(int) int
}

type Config struct {
	Clock          Clock
	Random         Random
	LeaseTTL       time.Duration
	MaxWaiters     int
	Cooldowns      map[FailureClass]time.Duration
	RestoredLeases []LeaseSnapshot
}

type Scheduler struct {
	mu        sync.Mutex
	clock     Clock
	random    Random
	leaseTTL  time.Duration
	maxWaiter int
	cooldowns map[FailureClass]time.Duration
	waiters   int
	nextID    uint64
	notify    chan struct{}
	leases    map[string]LeaseSnapshot
	inUse     map[string]int
	cooldown  map[string]time.Time
	sticky    map[string]string
}

type Lease struct {
	scheduler *Scheduler
	id        string
	accountID string
	expiresAt time.Time
}

func (l *Lease) ID() string {
	if l == nil {
		return ""
	}
	return l.id
}
func (l *Lease) AccountID() string {
	if l == nil {
		return ""
	}
	return l.accountID
}
func (l *Lease) ExpiresAt() time.Time {
	if l == nil {
		return time.Time{}
	}
	return l.expiresAt
}

// Release succeeds exactly once. Later calls return the original no-op signal
// false and never change capacity or cooldown state again.
func (l *Lease) Release(result ReleaseResult) (bool, Decision) {
	if l == nil || l.scheduler == nil {
		return false, Decision{Code: ReasonInvalidRequest}
	}
	return l.scheduler.release(l.id, result)
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type lockedRandom struct {
	mu    sync.Mutex
	state uint64
}

func (r *lockedRandom) Intn(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = r.state*6364136223846793005 + 1
	return int((r.state >> 33) % uint64(n))
}

func New(config Config) *Scheduler {
	if config.Clock == nil {
		config.Clock = realClock{}
	}
	if config.Random == nil {
		config.Random = &lockedRandom{state: uint64(config.Clock.Now().UnixNano())}
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 2 * time.Minute
	}
	if config.MaxWaiters < 0 {
		config.MaxWaiters = 0
	}
	s := &Scheduler{clock: config.Clock, random: config.Random, leaseTTL: config.LeaseTTL, maxWaiter: config.MaxWaiters, cooldowns: map[FailureClass]time.Duration{}, notify: make(chan struct{}), leases: map[string]LeaseSnapshot{}, inUse: map[string]int{}, cooldown: map[string]time.Time{}, sticky: map[string]string{}}
	for k, v := range config.Cooldowns {
		if v > 0 {
			s.cooldowns[k] = v
		}
	}
	now := s.clock.Now()
	for _, snapshot := range config.RestoredLeases {
		if snapshot.LeaseID == "" || snapshot.AccountID == "" || !snapshot.ExpiresAt.After(now) {
			continue
		}
		if _, exists := s.leases[snapshot.LeaseID]; exists {
			continue
		}
		s.leases[snapshot.LeaseID] = snapshot
		s.inUse[snapshot.AccountID]++
	}
	return s
}

func (s *Scheduler) Acquire(ctx context.Context, request Request) (*Lease, Decision) {
	if ctx == nil || request.Provider == "" || request.Model == "" || len(request.AllowedAccountIDs) == 0 || len(request.Candidates) == 0 {
		return nil, Decision{Code: ReasonInvalidRequest}
	}
	seenCandidates := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if candidate.ID == "" || candidate.Provider == "" || candidate.Weight < 0 || candidate.Capacity < 0 {
			return nil, Decision{Code: ReasonInvalidRequest}
		}
		if _, duplicate := seenCandidates[candidate.ID]; duplicate {
			return nil, Decision{Code: ReasonInvalidRequest}
		}
		seenCandidates[candidate.ID] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(request.AllowedAccountIDs))
	for _, id := range request.AllowedAccountIDs {
		if id != "" {
			allowed[id] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, Decision{Code: ReasonInvalidRequest}
	}
	waiting := false
	defer func() {
		if waiting {
			s.mu.Lock()
			s.waiters--
			s.mu.Unlock()
		}
	}()
	for {
		s.mu.Lock()
		now := s.clock.Now()
		s.cleanupExpiredLocked(now)
		available, compatible, allowedFound, nextWake := s.availableLocked(request, allowed, now)
		if len(available) > 0 {
			candidate := s.chooseLocked(available, request.StickyKey)
			lease := s.createLeaseLocked(candidate.ID, request.StickyKey, now)
			if waiting {
				s.waiters--
				waiting = false
			}
			s.mu.Unlock()
			return lease, Decision{Code: ReasonAcquired, AccountID: candidate.ID}
		}
		if !allowedFound {
			if waiting {
				s.waiters--
				waiting = false
			}
			s.mu.Unlock()
			return nil, Decision{Code: ReasonNoAllowedAccount}
		}
		if !compatible {
			if waiting {
				s.waiters--
				waiting = false
			}
			s.mu.Unlock()
			return nil, Decision{Code: ReasonNoCompatibleAccount}
		}
		if nextWake.IsZero() {
			if waiting {
				s.waiters--
				waiting = false
			}
			s.mu.Unlock()
			return nil, Decision{Code: ReasonCapacityUnavailable}
		}
		if !waiting {
			if s.maxWaiter == 0 || s.waiters >= s.maxWaiter {
				s.mu.Unlock()
				return nil, Decision{Code: ReasonQueueFull}
			}
			s.waiters++
			waiting = true
		}
		notify := s.notify
		s.mu.Unlock()
		var timer <-chan time.Time
		if !nextWake.IsZero() {
			delay := nextWake.Sub(s.clock.Now())
			if delay < 0 {
				delay = 0
			}
			timer = s.clock.After(delay)
		}
		select {
		case <-ctx.Done():
			return nil, Decision{Code: ReasonCancelled}
		case <-notify:
		case <-timer:
		}
	}
}

func (s *Scheduler) availableLocked(request Request, allowed map[string]struct{}, now time.Time) ([]Candidate, bool, bool, time.Time) {
	available := []Candidate{}
	compatible := false
	allowedFound := false
	var next time.Time
	for _, c := range request.Candidates {
		if _, ok := allowed[c.ID]; !ok || c.ID == "" {
			continue
		}
		allowedFound = true
		if !c.Enabled || c.Provider != request.Provider || !contains(c.Models, request.Model) {
			continue
		}
		compatible = true
		if c.Capacity <= 0 || c.Weight <= 0 {
			continue
		}
		until := c.CooldownUntil
		if internal := s.cooldown[c.ID]; internal.After(until) {
			until = internal
		}
		if until.After(now) {
			if next.IsZero() || until.Before(next) {
				next = until
			}
			continue
		}
		if s.inUse[c.ID] >= c.Capacity {
			for _, lease := range s.leases {
				if lease.AccountID == c.ID && (next.IsZero() || lease.ExpiresAt.Before(next)) {
					next = lease.ExpiresAt
				}
			}
			continue
		}
		available = append(available, c)
	}
	return available, compatible, allowedFound, next
}

func (s *Scheduler) chooseLocked(candidates []Candidate, stickyKey string) Candidate {
	best := candidates[0].Priority
	for _, c := range candidates[1:] {
		if c.Priority > best {
			best = c.Priority
		}
	}
	pool := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Priority == best {
			pool = append(pool, c)
		}
	}
	if stickyKey != "" {
		if id := s.sticky[stickyKey]; id != "" {
			for _, c := range pool {
				if c.ID == id {
					return c
				}
			}
		}
	}
	total := 0
	for _, c := range pool {
		total += c.Weight
	}
	pick := s.random.Intn(total)
	if pick < 0 {
		pick = -pick
	}
	pick %= total
	for _, c := range pool {
		if pick < c.Weight {
			return c
		}
		pick -= c.Weight
	}
	return pool[len(pool)-1]
}

func (s *Scheduler) createLeaseLocked(accountID, stickyKey string, now time.Time) *Lease {
	var id string
	for {
		s.nextID++
		id = fmt.Sprintf("lease-%d", s.nextID)
		if _, exists := s.leases[id]; !exists {
			break
		}
	}
	snapshot := LeaseSnapshot{LeaseID: id, AccountID: accountID, ExpiresAt: now.Add(s.leaseTTL)}
	s.leases[id] = snapshot
	s.inUse[accountID]++
	if stickyKey != "" {
		s.sticky[stickyKey] = accountID
	}
	return &Lease{scheduler: s, id: id, accountID: accountID, expiresAt: snapshot.ExpiresAt}
}

func (s *Scheduler) release(id string, result ReleaseResult) (bool, Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.cleanupExpiredLocked(now)
	snapshot, ok := s.leases[id]
	if !ok {
		return false, Decision{Code: ReasonAlreadyReleased}
	}
	delete(s.leases, id)
	if s.inUse[snapshot.AccountID] > 1 {
		s.inUse[snapshot.AccountID]--
	} else {
		delete(s.inUse, snapshot.AccountID)
	}
	if duration := s.cooldowns[result.Failure]; duration > 0 {
		until := now.Add(duration)
		if until.After(s.cooldown[snapshot.AccountID]) {
			s.cooldown[snapshot.AccountID] = until
		}
	}
	s.signalLocked()
	retry := (result.Failure == FailureRateLimit || result.Failure == FailureOverloaded || result.Failure == FailureTransient) && !result.StreamCommitted && !result.ExecutionUncertain
	return true, Decision{Code: ReasonReleased, AccountID: snapshot.AccountID, RetrySuggested: retry}
}

func (s *Scheduler) Snapshot() []LeaseSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(s.clock.Now())
	out := make([]LeaseSnapshot, 0, len(s.leases))
	for _, v := range s.leases {
		out = append(out, v)
	}
	return out
}
func (s *Scheduler) cleanupExpiredLocked(now time.Time) {
	changed := false
	for id, l := range s.leases {
		if !l.ExpiresAt.After(now) {
			delete(s.leases, id)
			if s.inUse[l.AccountID] > 1 {
				s.inUse[l.AccountID]--
			} else {
				delete(s.inUse, l.AccountID)
			}
			changed = true
		}
	}
	for id, until := range s.cooldown {
		if !until.After(now) {
			delete(s.cooldown, id)
			changed = true
		}
	}
	if changed {
		s.signalLocked()
	}
}
func (s *Scheduler) signalLocked() { close(s.notify); s.notify = make(chan struct{}) }
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
