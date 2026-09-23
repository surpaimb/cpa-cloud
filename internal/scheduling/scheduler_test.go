package scheduling

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}
type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock      { return &fakeClock{now: time.Unix(1_800_000_000, 0)} }
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	at := c.now.Add(d)
	if !at.After(c.now) {
		ch <- c.now
	} else {
		c.timers = append(c.timers, fakeTimer{at: at, ch: ch})
	}
	return ch
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	remaining := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(now) {
			timer.ch <- now
		} else {
			remaining = append(remaining, timer)
		}
	}
	c.timers = remaining
	c.mu.Unlock()
}
func (c *fakeClock) waitForTimer(t *testing.T) {
	t.Helper()
	for range 100 {
		c.mu.Lock()
		count := len(c.timers)
		c.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scheduler timer was not registered")
}

type sequenceRandom struct {
	mu     sync.Mutex
	values []int
	next   int
}

func (r *sequenceRandom) Intn(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.values) == 0 {
		return 0
	}
	value := r.values[r.next%len(r.values)]
	r.next++
	return value % n
}

func candidate(id string) Candidate {
	return Candidate{ID: id, Provider: "codex", Models: []string{"gpt"}, Enabled: true, Priority: 1, Weight: 1, Capacity: 1}
}
func request(candidates ...Candidate) Request {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	return Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: ids, Candidates: candidates}
}

func TestSchedulerPermissionCompatibilityPriorityWeightAndSticky(t *testing.T) {
	clock := newFakeClock()
	random := &sequenceRandom{values: []int{0, 1, 2, 3}}
	s := New(Config{Clock: clock, Random: random, LeaseTTL: time.Minute})
	a, b := candidate("a"), candidate("b")
	if _, invalid := s.Acquire(context.Background(), request(a, a)); invalid.Code != ReasonInvalidRequest {
		t.Fatalf("duplicate candidate=%+v", invalid)
	}
	b.Priority = 2
	lease, decision := s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a"}, Candidates: []Candidate{a, b}})
	if decision.Code != ReasonAcquired || lease.AccountID() != "a" {
		t.Fatalf("permission decision=%+v lease=%+v", decision, lease)
	}
	lease.Release(ReleaseResult{})
	bad := request(a)
	bad.Model = "other"
	if _, d := s.Acquire(context.Background(), bad); d.Code != ReasonNoCompatibleAccount {
		t.Fatalf("compatibility=%+v", d)
	}
	if _, d := s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"missing"}, Candidates: []Candidate{a}}); d.Code != ReasonNoAllowedAccount {
		t.Fatalf("allowlist=%+v", d)
	}
	lease, d := s.Acquire(context.Background(), request(a, b))
	if d.Code != ReasonAcquired || lease.AccountID() != "b" {
		t.Fatalf("priority=%+v account=%s", d, lease.AccountID())
	}
	lease.Release(ReleaseResult{})

	b.Priority = 1
	b.Weight = 3
	counts := map[string]int{}
	for range 4 {
		lease, d = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a", "b"}, StickyKey: "", Candidates: []Candidate{a, b}})
		if d.Code != ReasonAcquired {
			t.Fatal(d)
		}
		counts[lease.AccountID()]++
		lease.Release(ReleaseResult{})
	}
	if counts["a"] != 1 || counts["b"] != 3 {
		t.Fatalf("weighted selection=%v", counts)
	}
	lease, _ = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a", "b"}, StickyKey: "thread", Candidates: []Candidate{a, b}})
	sticky := lease.AccountID()
	lease.Release(ReleaseResult{})
	lease, _ = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a", "b"}, StickyKey: "thread", Candidates: []Candidate{a, b}})
	if lease.AccountID() != sticky {
		t.Fatalf("sticky moved %s -> %s", sticky, lease.AccountID())
	}
	lease.Release(ReleaseResult{})
	other := a
	if sticky == "a" {
		other = b
	}
	lease, _ = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{other.ID}, StickyKey: "thread", Candidates: []Candidate{a, b}})
	if lease.AccountID() != other.ID {
		t.Fatal("sticky bypassed current allowlist")
	}
}

func TestSchedulerCapacityWaitCancellationQueueAndExactlyOnce(t *testing.T) {
	clock := newFakeClock()
	s := New(Config{Clock: clock, LeaseTTL: time.Minute, MaxWaiters: 1})
	a := candidate("a")
	first, d := s.Acquire(context.Background(), request(a))
	if d.Code != ReasonAcquired {
		t.Fatal(d)
	}
	type result struct {
		lease    *Lease
		decision Decision
	}
	waited := make(chan result, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { lease, decision := s.Acquire(ctx, request(a)); waited <- result{lease, decision} }()
	for i := 0; i < 100; i++ {
		s.mu.Lock()
		n := s.waiters
		s.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, decision := s.Acquire(context.Background(), request(a)); decision.Code != ReasonQueueFull {
		t.Fatalf("queue=%+v", decision)
	}
	cancel()
	if got := <-waited; got.lease != nil || got.decision.Code != ReasonCancelled {
		t.Fatalf("cancel=%+v", got)
	}
	ok, released := first.Release(ReleaseResult{})
	if !ok || released.Code != ReasonReleased {
		t.Fatalf("release=%v %+v", ok, released)
	}
	if ok, again := first.Release(ReleaseResult{}); ok || again.Code != ReasonAlreadyReleased {
		t.Fatalf("double release=%v %+v", ok, again)
	}
	zero := a
	zero.Capacity = 0
	if _, decision := s.Acquire(context.Background(), request(zero)); decision.Code != ReasonCapacityUnavailable {
		t.Fatalf("zero capacity=%+v", decision)
	}
}

func TestSchedulerConcurrencyCapacityAndTTLRecovery(t *testing.T) {
	clock := newFakeClock()
	s := New(Config{Clock: clock, LeaseTTL: 10 * time.Second, MaxWaiters: 8})
	a := candidate("a")
	a.Capacity = 2
	one, _ := s.Acquire(context.Background(), request(a))
	two, _ := s.Acquire(context.Background(), request(a))
	if one == nil || two == nil {
		t.Fatal("capacity leases missing")
	}
	result := make(chan *Lease, 1)
	go func() { lease, _ := s.Acquire(context.Background(), request(a)); result <- lease }()
	select {
	case <-result:
		t.Fatal("capacity exceeded")
	case <-time.After(20 * time.Millisecond):
	}
	one.Release(ReleaseResult{})
	select {
	case lease := <-result:
		if lease == nil {
			t.Fatal("waiter did not acquire")
		}
		lease.Release(ReleaseResult{})
	case <-time.After(time.Second):
		t.Fatal("waiter not notified")
	}
	snapshot := s.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot=%v", snapshot)
	}
	restored := New(Config{Clock: clock, LeaseTTL: 10 * time.Second, RestoredLeases: snapshot})
	blocked := a
	blocked.Capacity = 1
	if _, d := restored.Acquire(context.Background(), request(blocked)); d.Code != ReasonQueueFull {
		t.Fatalf("restored active lease=%+v", d)
	}
	clock.Advance(11 * time.Second)
	lease, d := restored.Acquire(context.Background(), request(blocked))
	if d.Code != ReasonAcquired || lease == nil {
		t.Fatalf("expired restore=%+v", d)
	}
}

func TestSchedulerCooldownRecoveryAndSafeRetryAdvice(t *testing.T) {
	clock := newFakeClock()
	s := New(Config{Clock: clock, LeaseTTL: time.Minute, MaxWaiters: 2, Cooldowns: map[FailureClass]time.Duration{FailureRateLimit: 30 * time.Second, FailureTransient: 10 * time.Second, FailureAuth: time.Hour}})
	a := candidate("a")
	lease, _ := s.Acquire(context.Background(), request(a))
	ok, decision := lease.Release(ReleaseResult{Failure: FailureRateLimit})
	if !ok || !decision.RetrySuggested {
		t.Fatalf("safe retry=%+v", decision)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan *Lease, 1)
	go func() { next, _ := s.Acquire(ctx, request(a)); result <- next }()
	clock.waitForTimer(t)
	clock.Advance(29 * time.Second)
	select {
	case <-result:
		t.Fatal("cooldown ended early")
	case <-time.After(20 * time.Millisecond):
	}
	clock.Advance(time.Second)
	select {
	case next := <-result:
		if next == nil {
			t.Fatal("cooldown did not recover")
		}
		next.Release(ReleaseResult{})
	case <-time.After(time.Second):
		t.Fatal("cooldown waiter not awakened")
	}
	for _, release := range []ReleaseResult{{Failure: FailureTransient, StreamCommitted: true}, {Failure: FailureTransient, ExecutionUncertain: true}, {Failure: FailureAuth}} {
		lease, _ = s.Acquire(context.Background(), request(a))
		_, decision = lease.Release(release)
		if decision.RetrySuggested {
			t.Fatalf("unsafe retry suggested for %+v", release)
		}
		clock.Advance(time.Hour)
	}
	external := a
	external.CooldownUntil = clock.Now().Add(time.Minute)
	waitCtx, waitCancel := context.WithCancel(context.Background())
	defer waitCancel()
	done := make(chan *Lease, 1)
	go func() { l, _ := s.Acquire(waitCtx, request(external)); done <- l }()
	clock.waitForTimer(t)
	clock.Advance(time.Minute)
	select {
	case l := <-done:
		if l == nil {
			t.Fatal("external cooldown did not recover")
		}
	case <-time.After(time.Second):
		t.Fatal("external cooldown wait stuck")
	}
}
