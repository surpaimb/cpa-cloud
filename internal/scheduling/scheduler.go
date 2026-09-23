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

const (
	maxCandidates      = 10_000
	maxCandidateWeight = 1_000_000
	maxStickyKeyBytes  = 512
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

// DispatchPhase is positive evidence about whether an upstream model request
// could have executed. The zero value deliberately denies retry.
type DispatchPhase uint8

const (
	DispatchUnknown DispatchPhase = iota
	DispatchNotStarted
	MayHaveSent
	OutputCommitted
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
	Provider           string
	Model              string
	AllowedAccountIDs  []string
	ExcludedAccountIDs []string
	StickyKey          string
	Candidates         []Candidate
}

type Decision struct {
	Code           ReasonCode
	AccountID      string
	RetrySuggested bool
}

type ReleaseResult struct {
	Failure FailureClass
	Phase   DispatchPhase
	// Deprecated: retained for source compatibility. False zero values are not
	// evidence that dispatch was safe, so these fields never authorize retry.
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
	StickyTTL      time.Duration
	MaxSticky      int
	MaxWaiters     int
	Cooldowns      map[FailureClass]time.Duration
	RestoredLeases []LeaseSnapshot
}

type Scheduler struct {
	mu        sync.Mutex
	clock     Clock
	random    Random
	leaseTTL  time.Duration
	stickyTTL time.Duration
	maxSticky int
	maxWaiter int
	cooldowns map[FailureClass]time.Duration
	waiters   int
	nextID    uint64
	notify    chan struct{}
	leases    map[string]LeaseSnapshot
	inUse     map[string]int
	cooldown  map[string]time.Time
	sticky    map[string]stickyBinding
}

type stickyBinding struct {
	accountID string
	expiresAt time.Time
}

type Lease struct {
	mu        sync.Mutex
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
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.expiresAt
}

// Renew extends an active lease by the configured TTL. It fails after expiry
// or release and never recreates capacity ownership.
func (l *Lease) Renew() (time.Time, bool) {
	if l == nil || l.scheduler == nil {
		return time.Time{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	expiresAt, ok := l.scheduler.renew(l.id)
	if ok {
		l.expiresAt = expiresAt
	}
	return expiresAt, ok
}

// Release succeeds exactly once. Later calls return the original no-op signal
// false and never change capacity or cooldown state again.
func (l *Lease) Release(result ReleaseResult) (bool, Decision) {
	if l == nil || l.scheduler == nil {
		return false, Decision{Code: ReasonInvalidRequest}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
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
	if config.StickyTTL <= 0 {
		config.StickyTTL = 30 * time.Minute
	}
	if config.MaxSticky <= 0 {
		config.MaxSticky = 10_000
	}
	if config.MaxWaiters < 0 {
		config.MaxWaiters = 0
	}
	s := &Scheduler{clock: config.Clock, random: config.Random, leaseTTL: config.LeaseTTL, stickyTTL: config.StickyTTL, maxSticky: config.MaxSticky, maxWaiter: config.MaxWaiters, cooldowns: map[FailureClass]time.Duration{}, notify: make(chan struct{}), leases: map[string]LeaseSnapshot{}, inUse: map[string]int{}, cooldown: map[string]time.Time{}, sticky: map[string]stickyBinding{}}
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
	if ctx == nil || request.Provider == "" || request.Model == "" || len(request.AllowedAccountIDs) == 0 || len(request.Candidates) == 0 || len(request.Candidates) > maxCandidates || len(request.ExcludedAccountIDs) > maxCandidates || len(request.StickyKey) > maxStickyKeyBytes {
		return nil, Decision{Code: ReasonInvalidRequest}
	}
	if ctx.Err() != nil {
		return nil, Decision{Code: ReasonCancelled}
	}
	seenCandidates := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if candidate.ID == "" || candidate.Provider == "" || candidate.Weight < 0 || candidate.Weight > maxCandidateWeight || candidate.Capacity < 0 {
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
	excluded := make(map[string]struct{}, len(request.ExcludedAccountIDs))
	for _, id := range request.ExcludedAccountIDs {
		if id == "" {
			return nil, Decision{Code: ReasonInvalidRequest}
		}
		if _, duplicate := excluded[id]; duplicate {
			return nil, Decision{Code: ReasonInvalidRequest}
		}
		excluded[id] = struct{}{}
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
		if ctx.Err() != nil {
			if waiting {
				s.waiters--
				waiting = false
			}
			s.mu.Unlock()
			return nil, Decision{Code: ReasonCancelled}
		}
		available, compatible, allowedFound, nextWake := s.availableLocked(request, allowed, excluded, now)
		if len(available) > 0 {
			candidate, ok := s.chooseLocked(available, request.StickyKey)
			if !ok {
				if waiting {
					s.waiters--
					waiting = false
				}
				s.mu.Unlock()
				return nil, Decision{Code: ReasonInvalidRequest}
			}
			if ctx.Err() != nil {
				if waiting {
					s.waiters--
					waiting = false
				}
				s.mu.Unlock()
				return nil, Decision{Code: ReasonCancelled}
			}
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

func (s *Scheduler) availableLocked(request Request, allowed, excluded map[string]struct{}, now time.Time) ([]Candidate, bool, bool, time.Time) {
	available := []Candidate{}
	compatible := false
	allowedFound := false
	var next time.Time
	for _, c := range request.Candidates {
		if _, ok := allowed[c.ID]; !ok || c.ID == "" {
			continue
		}
		if _, skip := excluded[c.ID]; skip {
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

func (s *Scheduler) chooseLocked(candidates []Candidate, stickyKey string) (Candidate, bool) {
	if stickyKey != "" {
		if binding, ok := s.sticky[stickyKey]; ok {
			for _, c := range candidates {
				if c.ID == binding.accountID {
					return c, true
				}
			}
		}
	}
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
	total := 0
	for _, c := range pool {
		if c.Weight > maxInt()-total {
			return Candidate{}, false
		}
		total += c.Weight
	}
	if total <= 0 {
		return Candidate{}, false
	}
	pick := s.random.Intn(total)
	if pick < 0 {
		pick = -(pick + 1)
	}
	pick %= total
	for _, c := range pool {
		if pick < c.Weight {
			return c, true
		}
		pick -= c.Weight
	}
	return pool[len(pool)-1], true
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
		s.setStickyLocked(stickyKey, accountID, now)
	}
	return &Lease{scheduler: s, id: id, accountID: accountID, expiresAt: snapshot.ExpiresAt}
}

func (s *Scheduler) setStickyLocked(key, accountID string, now time.Time) {
	if _, exists := s.sticky[key]; !exists && len(s.sticky) >= s.maxSticky {
		var evictKey string
		var evictAt time.Time
		for candidateKey, binding := range s.sticky {
			if evictKey == "" || binding.expiresAt.Before(evictAt) || (binding.expiresAt.Equal(evictAt) && candidateKey < evictKey) {
				evictKey = candidateKey
				evictAt = binding.expiresAt
			}
		}
		delete(s.sticky, evictKey)
	}
	s.sticky[key] = stickyBinding{accountID: accountID, expiresAt: now.Add(s.stickyTTL)}
}

func (s *Scheduler) renew(id string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.cleanupExpiredLocked(now)
	snapshot, ok := s.leases[id]
	if !ok {
		return time.Time{}, false
	}
	snapshot.ExpiresAt = now.Add(s.leaseTTL)
	s.leases[id] = snapshot
	return snapshot.ExpiresAt, true
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
	retry := result.Phase == DispatchNotStarted && !result.StreamCommitted && !result.ExecutionUncertain && (result.Failure == FailureRateLimit || result.Failure == FailureOverloaded || result.Failure == FailureTransient || result.Failure == FailureAuth || result.Failure == FailurePermanent)
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
	for key, binding := range s.sticky {
		if !binding.expiresAt.After(now) {
			delete(s.sticky, key)
		}
	}
	if changed {
		s.signalLocked()
	}
}
func (s *Scheduler) signalLocked() { close(s.notify); s.notify = make(chan struct{}) }
func maxInt() int                  { return int(^uint(0) >> 1) }
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
