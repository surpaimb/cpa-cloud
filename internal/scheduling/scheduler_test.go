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
	lease, d = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a", "b"}, ExcludedAccountIDs: []string{sticky}, StickyKey: "thread", Candidates: []Candidate{a, b}})
	if d.Code != ReasonAcquired || lease.AccountID() != other.ID {
		t.Fatalf("excluded sticky decision=%+v account=%v", d, lease)
	}
	lease.Release(ReleaseResult{})
	if _, d := s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a", "b"}, ExcludedAccountIDs: []string{"a", "a"}, Candidates: []Candidate{a, b}}); d.Code != ReasonInvalidRequest {
		t.Fatalf("duplicate exclusion=%+v", d)
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

type cancellingRandom struct {
	cancel context.CancelFunc
}

func (r cancellingRandom) Intn(int) int {
	r.cancel()
	return 0
}

func TestSchedulerCancellationBeforeAndDuringLockedSelection(t *testing.T) {
	a := candidate("a")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	s := New(Config{})
	if lease, decision := s.Acquire(cancelled, request(a)); lease != nil || decision.Code != ReasonCancelled {
		t.Fatalf("initial cancellation lease=%v decision=%+v", lease, decision)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("initially cancelled request consumed capacity")
	}

	lockedContext, cancelLocked := context.WithCancel(context.Background())
	s = New(Config{Random: cancellingRandom{cancel: cancelLocked}})
	if lease, decision := s.Acquire(lockedContext, request(a)); lease != nil || decision.Code != ReasonCancelled {
		t.Fatalf("locked cancellation lease=%v decision=%+v", lease, decision)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("request cancelled during selection consumed capacity")
	}
}

func TestSchedulerWeightSumOverflowIsRejected(t *testing.T) {
	s := New(Config{Random: &sequenceRandom{}})
	a, b := candidate("a"), candidate("b")
	a.Weight = maxInt()
	b.Weight = 1
	if selected, ok := s.chooseLocked([]Candidate{a, b}, ""); ok {
		t.Fatalf("overflowing pool selected %+v", selected)
	}

	a.Weight = maxCandidateWeight + 1
	if lease, decision := s.Acquire(context.Background(), request(a)); lease != nil || decision.Code != ReasonInvalidRequest {
		t.Fatalf("oversized candidate weight lease=%v decision=%+v", lease, decision)
	}
}

func TestSchedulerStickyPrecedesPriorityAndIsBoundedByTTLAndCapacity(t *testing.T) {
	clock := newFakeClock()
	s := New(Config{Clock: clock, Random: &sequenceRandom{}, LeaseTTL: time.Minute, StickyTTL: 10 * time.Second, MaxSticky: 2})
	a, b, c := candidate("a"), candidate("b"), candidate("c")
	b.Priority = 100

	lease, decision := s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a"}, StickyKey: "one", Candidates: []Candidate{a, b}})
	if decision.Code != ReasonAcquired || lease.AccountID() != "a" {
		t.Fatalf("seed sticky decision=%+v lease=%v", decision, lease)
	}
	lease.Release(ReleaseResult{})
	lease, decision = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"a", "b"}, StickyKey: "one", Candidates: []Candidate{a, b}})
	if decision.Code != ReasonAcquired || lease.AccountID() != "a" {
		t.Fatalf("healthy sticky lost to priority decision=%+v account=%s", decision, lease.AccountID())
	}
	lease.Release(ReleaseResult{})

	clock.Advance(time.Second)
	lease, _ = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"b"}, StickyKey: "two", Candidates: []Candidate{b}})
	lease.Release(ReleaseResult{})
	clock.Advance(time.Second)
	lease, _ = s.Acquire(context.Background(), Request{Provider: "codex", Model: "gpt", AllowedAccountIDs: []string{"c"}, StickyKey: "three", Candidates: []Candidate{c}})
	lease.Release(ReleaseResult{})

	s.mu.Lock()
	_, hasOne := s.sticky["one"]
	_, hasTwo := s.sticky["two"]
	_, hasThree := s.sticky["three"]
	stickyCount := len(s.sticky)
	s.mu.Unlock()
	if hasOne || !hasTwo || !hasThree || stickyCount != 2 {
		t.Fatalf("sticky eviction one=%v two=%v three=%v count=%d", hasOne, hasTwo, hasThree, stickyCount)
	}

	clock.Advance(11 * time.Second)
	s.Snapshot()
	s.mu.Lock()
	stickyCount = len(s.sticky)
	s.mu.Unlock()
	if stickyCount != 0 {
		t.Fatalf("expired sticky bindings retained: %d", stickyCount)
	}
}

func TestSchedulerRenewKeepsCapacityAndFailsAfterExpiryOrRelease(t *testing.T) {
	clock := newFakeClock()
	s := New(Config{Clock: clock, LeaseTTL: 10 * time.Second, MaxWaiters: 1})
	a := candidate("a")
	active, decision := s.Acquire(context.Background(), request(a))
	if decision.Code != ReasonAcquired {
		t.Fatal(decision)
	}

	type result struct {
		lease    *Lease
		decision Decision
	}
	waited := make(chan result, 1)
	go func() {
		lease, got := s.Acquire(context.Background(), request(a))
		waited <- result{lease: lease, decision: got}
	}()
	clock.waitForTimer(t)
	clock.Advance(9 * time.Second)
	renewedUntil, ok := active.Renew()
	if !ok || !renewedUntil.Equal(clock.Now().Add(10*time.Second)) || !active.ExpiresAt().Equal(renewedUntil) {
		t.Fatalf("renew ok=%v returned=%v lease=%v", ok, renewedUntil, active.ExpiresAt())
	}
	clock.Advance(time.Second)
	clock.waitForTimer(t)
	select {
	case got := <-waited:
		t.Fatalf("renewed capacity oversold: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	clock.Advance(9 * time.Second)
	select {
	case got := <-waited:
		if got.lease == nil || got.decision.Code != ReasonAcquired {
			t.Fatalf("waiter after renewed expiry: %+v", got)
		}
		got.lease.Release(ReleaseResult{})
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire after renewed expiry")
	}
	if expiresAt, ok := active.Renew(); ok || !expiresAt.IsZero() {
		t.Fatalf("expired lease renewed ok=%v expires=%v", ok, expiresAt)
	}

	released, _ := s.Acquire(context.Background(), request(a))
	if ok, _ := released.Release(ReleaseResult{}); !ok {
		t.Fatal("fresh lease release failed")
	}
	if expiresAt, ok := released.Renew(); ok || !expiresAt.IsZero() {
		t.Fatalf("released lease renewed ok=%v expires=%v", ok, expiresAt)
	}
}

func TestSchedulerConcurrentRenewReleaseAndSnapshot(t *testing.T) {
	s := New(Config{LeaseTTL: time.Minute})
	lease, decision := s.Acquire(context.Background(), request(candidate("a")))
	if decision.Code != ReasonAcquired {
		t.Fatal(decision)
	}

	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				lease.Renew()
				_ = lease.ExpiresAt()
				_ = s.Snapshot()
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		lease.Release(ReleaseResult{})
	}()
	workers.Wait()
	if len(s.Snapshot()) != 0 {
		t.Fatal("concurrent release left an active lease")
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
	ok, decision := lease.Release(ReleaseResult{Failure: FailureRateLimit, Phase: DispatchNotStarted})
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
	for _, release := range []ReleaseResult{
		{Failure: FailureTransient},
		{Failure: FailureTransient, Phase: MayHaveSent},
		{Failure: FailureTransient, Phase: OutputCommitted},
		{Failure: FailureTransient, StreamCommitted: false, ExecutionUncertain: false},
		{Failure: FailureTransient, Phase: DispatchNotStarted, StreamCommitted: true},
		{Failure: FailureTransient, Phase: DispatchNotStarted, ExecutionUncertain: true},
	} {
		lease, _ = s.Acquire(context.Background(), request(a))
		_, decision = lease.Release(release)
		if decision.RetrySuggested {
			t.Fatalf("unsafe retry suggested for %+v", release)
		}
		clock.Advance(time.Hour)
	}
	for _, failure := range []FailureClass{FailureAuth, FailurePermanent} {
		lease, _ = s.Acquire(context.Background(), request(a))
		_, decision = lease.Release(ReleaseResult{Failure: failure, Phase: DispatchNotStarted})
		if !decision.RetrySuggested {
			t.Fatalf("pre-dispatch retry not suggested for %s", failure)
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
