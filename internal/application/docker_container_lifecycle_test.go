package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/sandbox"
)

type dockerLifecycleTestRecorder struct {
	mu     sync.Mutex
	events []string
}

type dockerLifecycleLookupBarrier struct {
	mu       sync.Mutex
	arrivals int
	ready    chan struct{}
}

func newDockerLifecycleLookupBarrier() *dockerLifecycleLookupBarrier {
	return &dockerLifecycleLookupBarrier{ready: make(chan struct{})}
}

func (barrier *dockerLifecycleLookupBarrier) wait() {
	barrier.mu.Lock()
	barrier.arrivals++
	if barrier.arrivals == 2 {
		close(barrier.ready)
	}
	ready := barrier.ready
	barrier.mu.Unlock()
	<-ready
}

func (recorder *dockerLifecycleTestRecorder) add(event string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
}

func (recorder *dockerLifecycleTestRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.events...)
}

type dockerLifecycleSupervisorTestStore struct {
	mu            sync.Mutex
	now           time.Time
	recorder      *dockerLifecycleTestRecorder
	record        sandbox.DockerContainerLifecycleRecord
	found         bool
	fenceErr      error
	beginCalls    int
	acquireCalls  int
	completeCalls int
	lookupBarrier *dockerLifecycleLookupBarrier
}

func (store *dockerLifecycleSupervisorTestStore) GetDockerContainerLifecycleByOperation(
	_ context.Context, operationKeyDigest string,
) (sandbox.DockerContainerLifecycleRecord, bool, error) {
	store.mu.Lock()
	found := store.found && store.record.Intent.OperationKeyDigest == operationKeyDigest
	record := store.record
	barrier := store.lookupBarrier
	store.mu.Unlock()
	if !found && barrier != nil {
		barrier.wait()
	}
	return record, found, nil
}

func (store *dockerLifecycleSupervisorTestStore) BeginDockerContainerLifecycle(_ context.Context,
	intent sandbox.DockerContainerLaunchIntent, ownerID string, ttl time.Duration,
) (sandbox.DockerContainerLifecycleRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.beginCalls++
	store.recorder.add("begin")
	if store.found {
		if store.record.Intent.ID == intent.ID &&
			store.record.Intent.IntentFingerprint == intent.IntentFingerprint {
			return store.record, true, nil
		}
		return sandbox.DockerContainerLifecycleRecord{}, false, apperror.New(
			apperror.CodeConflict, "Docker lifecycle test operation already exists")
	}
	lease, err := sandbox.NewDockerContainerLifecycleLease(intent, "lifecycle-lease-1",
		ownerID, 1, store.now, ttl)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, false, err
	}
	store.record = sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease}
	store.found = true
	return store.record, false, nil
}

func (store *dockerLifecycleSupervisorTestStore) AcquireDockerContainerLifecycle(_ context.Context,
	intentID, requestedBy, ownerID string, ttl time.Duration,
) (sandbox.DockerContainerLifecycleRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.acquireCalls++
	store.recorder.add("acquire")
	if !store.found || store.record.Intent.ID != intentID ||
		store.record.Intent.RequestedBy != requestedBy {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.New(
			apperror.CodeNotFound, "Docker lifecycle test record was not found")
	}
	if store.record.Receipt != nil {
		return store.record, nil
	}
	if store.record.Lease.ActiveAt(store.now) {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.New(
			apperror.CodeConflict, "Docker lifecycle test lease is still active")
	}
	lease, err := sandbox.NewDockerContainerLifecycleLease(store.record.Intent,
		fmt.Sprintf("lifecycle-lease-%d", store.record.Lease.Generation+1), ownerID,
		store.record.Lease.Generation+1, store.now, ttl)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	store.record.Lease = lease
	store.record.TookOver = true
	return store.record, nil
}

func (store *dockerLifecycleSupervisorTestStore) RenewDockerContainerLifecycleLease(
	_ context.Context, expected sandbox.DockerContainerLifecycleLease, ttl time.Duration,
) (sandbox.DockerContainerLifecycleLease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.record.Lease.Fences(expected, store.now) {
		return sandbox.DockerContainerLifecycleLease{}, apperror.New(
			apperror.CodeConflict, "Docker lifecycle test lease was fenced")
	}
	renewed, err := store.record.Lease.Renew(store.now, ttl)
	if err == nil {
		store.record.Lease = renewed
	}
	return renewed, err
}

func (store *dockerLifecycleSupervisorTestStore) ReleaseDockerContainerLifecycleLease(
	_ context.Context, expected sandbox.DockerContainerLifecycleLease,
) (sandbox.DockerContainerLifecycleLease, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.record.Lease.Status == sandbox.DockerContainerLifecycleLeaseReleased {
		return store.record.Lease, true, nil
	}
	if !store.record.Lease.Fences(expected, store.now) {
		return sandbox.DockerContainerLifecycleLease{}, false, apperror.New(
			apperror.CodeConflict, "Docker lifecycle test lease was fenced")
	}
	released := expected
	released.Status = sandbox.DockerContainerLifecycleLeaseReleased
	releasedAt := store.now
	released.ReleasedAt = &releasedAt
	store.record.Lease = released
	return released, false, nil
}

func (store *dockerLifecycleSupervisorTestStore) FenceDockerContainerLifecycle(_ context.Context,
	expected sandbox.DockerContainerLifecycleLease,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.recorder.add("fence")
	if store.fenceErr != nil {
		return store.fenceErr
	}
	if !store.record.Lease.Fences(expected, store.now) {
		return apperror.New(apperror.CodeConflict, "Docker lifecycle test lease was fenced")
	}
	return nil
}

func (store *dockerLifecycleSupervisorTestStore) PrepareDockerContainerLifecycleAction(
	_ context.Context, action sandbox.DockerContainerLifecyclePreparedAction,
	_ sandbox.DockerContainerLifecycleLease,
) (sandbox.DockerContainerLifecycleRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.recorder.add("prepare:" + action.Verb)
	for _, existing := range store.record.Actions {
		if existing.ActionFingerprint == action.ActionFingerprint {
			return store.record, true, nil
		}
	}
	store.record.Actions = append(store.record.Actions, action)
	return store.record, false, nil
}

func (store *dockerLifecycleSupervisorTestStore) AppendDockerContainerLifecycleTransition(
	_ context.Context, transition sandbox.DockerContainerLifecycleTransition,
	_ sandbox.DockerContainerLifecycleLease,
) (sandbox.DockerContainerLifecycleRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.recorder.add("transition:" + transition.State)
	for _, existing := range store.record.Transitions {
		if existing.TransitionFingerprint == transition.TransitionFingerprint {
			return store.record, true, nil
		}
	}
	store.record.Transitions = append(store.record.Transitions, transition)
	return store.record, false, nil
}

func (store *dockerLifecycleSupervisorTestStore) CompleteDockerContainerLifecycle(
	_ context.Context, receipt sandbox.DockerContainerLifecycleReceipt,
	_ sandbox.DockerContainerLifecycleLease,
) (sandbox.DockerContainerLifecycleRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.recorder.add("receipt")
	if store.record.Receipt != nil {
		return store.record, true, nil
	}
	store.completeCalls++
	store.record.Receipt = &receipt
	return store.record, false, nil
}

func (store *dockerLifecycleSupervisorTestStore) GetDockerContainerLifecycle(_ context.Context,
	intentID string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.found || store.record.Intent.ID != intentID {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.New(
			apperror.CodeNotFound, "Docker lifecycle test record was not found")
	}
	return store.record, nil
}

func (store *dockerLifecycleSupervisorTestStore) ListRecoverableDockerContainerLifecycles(
	_ context.Context, _ int,
) ([]sandbox.DockerContainerLifecycleRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.found || store.record.Receipt != nil || store.record.Lease.ActiveAt(store.now) {
		return nil, nil
	}
	return []sandbox.DockerContainerLifecycleRecord{store.record}, nil
}

type dockerLifecycleSupervisorTestAuthority struct {
	plan    sandbox.DockerContainerPlan
	request sandbox.DockerContainerWriteRequest
}

func (authority dockerLifecycleSupervisorTestAuthority) RevalidateDockerContainerLifecycle(
	_ context.Context, _ sandbox.DockerContainerLaunchIntent,
) (sandbox.DockerContainerPlan, sandbox.DockerContainerWriteRequest, error) {
	return authority.plan, authority.request, nil
}

type dockerLifecycleSupervisorTestTransport struct {
	endpoint        sandbox.DockerObservationEndpoint
	recorder        *dockerLifecycleTestRecorder
	state           string
	resourceID      string
	waitErr         error
	stageCalls      int
	startCalls      int
	waitCalls       int
	cleanup         int
	terminate       int
	creates         int
	starts          int
	terms           int
	deletes         int
	naturalExitCode int
}

func (transport *dockerLifecycleSupervisorTestTransport) Endpoint() sandbox.DockerObservationEndpoint {
	return transport.endpoint
}

func (transport *dockerLifecycleSupervisorTestTransport) Stage(_ context.Context,
	_ sandbox.DockerContainerWriteRequest,
) (sandbox.DockerContainerStageResult, error) {
	return sandbox.DockerContainerStageResult{}, errors.New("legacy Stage is not used by Supervisor")
}

func (transport *dockerLifecycleSupervisorTestTransport) StageOwned(ctx context.Context,
	request sandbox.DockerContainerWriteRequest, _ sandbox.DockerContainerLifecycleOwnership,
	fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerStageResult, error) {
	transport.stageCalls++
	transport.recorder.add("stage-owned")
	if transport.state != sandbox.DockerContainerLifecycleStateAbsent {
		stage, err := sandbox.NewDockerContainerStageResult(transport.endpoint, request,
			strings.Repeat("c", 64), true)
		if err != nil {
			return sandbox.DockerContainerStageResult{}, err
		}
		if stage.ContainerIDFingerprint != transport.resourceID {
			return sandbox.DockerContainerStageResult{}, errors.New(
				"adopted Docker lifecycle container identity changed")
		}
		transport.recorder.add("adopt:create")
		return stage, nil
	}
	if err := fence(ctx, sandbox.DockerContainerLifecycleActionCreate); err != nil {
		return sandbox.DockerContainerStageResult{}, err
	}
	transport.creates++
	transport.recorder.add("mutate:create")
	stage, err := sandbox.NewDockerContainerStageResult(transport.endpoint, request,
		strings.Repeat("a", 64), false)
	if err == nil {
		transport.resourceID = stage.ContainerIDFingerprint
		transport.state = sandbox.DockerContainerLifecycleStateCreated
	}
	return stage, err
}

func (transport *dockerLifecycleSupervisorTestTransport) Observe(_ context.Context,
	request sandbox.DockerContainerLifecycleRequest,
) (sandbox.DockerContainerLifecycleObservation, error) {
	return transport.observation(request, transport.state), nil
}

func (transport *dockerLifecycleSupervisorTestTransport) Start(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleObservation, bool, error) {
	transport.startCalls++
	if err := fence(ctx, sandbox.DockerContainerLifecycleActionStart); err != nil {
		return sandbox.DockerContainerLifecycleObservation{}, false, err
	}
	transport.starts++
	transport.recorder.add("mutate:start")
	transport.state = sandbox.DockerContainerLifecycleStateRunning
	return transport.observation(request, transport.state), true, nil
}

func (transport *dockerLifecycleSupervisorTestTransport) Wait(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleObservation, error) {
	transport.waitCalls++
	if err := fence(ctx, sandbox.DockerContainerLifecycleActionWait); err != nil {
		return sandbox.DockerContainerLifecycleObservation{}, err
	}
	if transport.waitErr != nil {
		return sandbox.DockerContainerLifecycleObservation{}, transport.waitErr
	}
	transport.state = sandbox.DockerContainerLifecycleStateExited
	return transport.observation(request, transport.state), nil
}

func (transport *dockerLifecycleSupervisorTestTransport) Terminate(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleTerminationResult, error) {
	transport.terminate++
	if err := fence(ctx, sandbox.DockerContainerLifecycleActionTERM); err != nil {
		return sandbox.DockerContainerLifecycleTerminationResult{}, err
	}
	transport.terms++
	transport.recorder.add("mutate:term")
	transport.state = sandbox.DockerContainerLifecycleStateExited
	observation := transport.observation(request, transport.state)
	return sandbox.DockerContainerLifecycleTerminationResult{
		Observation:        observation,
		ExitCode:           observation.ExitCode,
		DaemonReadCount:    1,
		DaemonWriteCount:   1,
		GracefulSignalSent: true,
	}, nil
}

func (transport *dockerLifecycleSupervisorTestTransport) Cleanup(ctx context.Context,
	request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence,
) (sandbox.DockerContainerLifecycleCleanupResult, error) {
	transport.cleanup++
	if err := fence(ctx, sandbox.DockerContainerLifecycleActionDelete); err != nil {
		return sandbox.DockerContainerLifecycleCleanupResult{}, err
	}
	transport.deletes++
	transport.recorder.add("mutate:delete")
	transport.state = sandbox.DockerContainerLifecycleStateAbsent
	return sandbox.DockerContainerLifecycleCleanupResult{
		Observation:      transport.observation(request, transport.state),
		DaemonReadCount:  1,
		DaemonWriteCount: 1,
		ContainerRemoved: true,
		AbsenceConfirmed: true,
	}, nil
}

func (transport *dockerLifecycleSupervisorTestTransport) Run(_ context.Context,
	_ sandbox.DockerContainerLifecycleRequest,
) (sandbox.DockerContainerLifecycleResult, error) {
	return sandbox.DockerContainerLifecycleResult{}, errors.New("monolithic Run is not used by Supervisor")
}

func (transport *dockerLifecycleSupervisorTestTransport) observation(
	request sandbox.DockerContainerLifecycleRequest, state string,
) sandbox.DockerContainerLifecycleObservation {
	present := state != sandbox.DockerContainerLifecycleStateAbsent
	exitCode := 0
	if state == sandbox.DockerContainerLifecycleStateExited {
		exitCode = transport.naturalExitCode
	}
	observation, _ := sandbox.NewDockerContainerLifecycleObservation(transport.endpoint,
		request, state, map[bool]string{true: transport.resourceID}[present], exitCode, 1)
	return observation
}

func TestDockerContainerLifecycleSupervisorWritesIntentAndActionFenceBeforeCreate(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "begin-fence")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	result, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "begin-before-create")
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || store.beginCalls != 1 || transport.creates != 1 {
		t.Fatalf("lifecycle did not complete once: record=%#v begin=%d creates=%d",
			result, store.beginCalls, transport.creates)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "begin", "stage-owned",
		"prepare:create", "fence", "mutate:create")
}

func TestDockerContainerLifecycleSupervisorBeginReplayIsMetadataOnly(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "begin-replay")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	first, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "same-operation")
	if err != nil {
		t.Fatal(err)
	}
	writes := transport.creates + transport.starts + transport.deletes
	second, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "same-operation")
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt == nil || second.Receipt == nil || !second.Replayed ||
		store.beginCalls != 1 || transport.creates+transport.starts+transport.deletes != writes {
		t.Fatalf("begin replay repeated daemon work: first=%#v second=%#v begins=%d before=%d after=%d",
			first, second, store.beginCalls, writes,
			transport.creates+transport.starts+transport.deletes)
	}
}

func TestDockerContainerLifecycleSupervisorStartAuthorityRunsBeforeCreateAndStart(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "start-authority-order")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	actions := make([]sandbox.DockerContainerLifecycleActionKind, 0, 2)
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, authority DockerContainerLifecycleStartAuthorityRequest) error {
				if err := authority.Validate(); err != nil {
					t.Fatal(err)
				}
				actions = append(actions, authority.Action)
				recorder.add("start-authority:" + authority.Action.String())
				return nil
			}))

	result, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "start-authority-order")
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || len(actions) != 2 ||
		actions[0] != sandbox.DockerContainerLifecycleActionCreate ||
		actions[1] != sandbox.DockerContainerLifecycleActionStart ||
		transport.creates != 1 || transport.starts != 1 {
		t.Fatalf("start authority did not guard create/start exactly once: record=%#v actions=%v creates=%d starts=%d",
			result, actions, transport.creates, transport.starts)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "prepare:create",
		"start-authority:create", "mutate:create", "prepare:start",
		"start-authority:start", "mutate:start")
}

func TestDockerContainerLifecycleSupervisorPreCreateStartAuthorityDeniedHasZeroDaemonWrites(
	t *testing.T,
) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "start-authority-pre-create-denied")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	authorityCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, authority DockerContainerLifecycleStartAuthorityRequest) error {
				authorityCalls++
				if authority.Action != sandbox.DockerContainerLifecycleActionCreate {
					t.Fatalf("pre-create denial received unexpected action: %s", authority.Action)
				}
				recorder.add("start-authority:create")
				return errors.New("process capability is no longer active")
			}))

	result, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "start-authority-pre-create-denied")
	if apperror.CodeOf(err) != apperror.CodePolicyDenied ||
		!strings.Contains(err.Error(), DockerContainerLifecycleStartAuthorityDeniedReason) {
		t.Fatalf("pre-create denial did not return the stable policy boundary: %v", err)
	}
	if result.Receipt == nil ||
		result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeFailed ||
		!result.Receipt.ContainerAlreadyAbsent || authorityCalls != 1 ||
		transport.creates != 0 || transport.starts != 0 || transport.terms != 0 ||
		transport.deletes != 0 {
		t.Fatalf("pre-create denial issued a daemon write or missed its cleanup receipt: record=%#v calls=%d writes=create:%d start:%d term:%d delete:%d",
			result, authorityCalls, transport.creates, transport.starts,
			transport.terms, transport.deletes)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "start-authority:create",
		"transition:cleaning", "transition:cleaned", "receipt")
}

func TestDockerContainerLifecycleSupervisorPostExitRunsAfterCheckpointBeforeDelete(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "post-exit-order")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	hookCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecyclePostExit(DockerContainerLifecyclePostExitFunc(
			func(_ context.Context, postExit DockerContainerLifecyclePostExitRequest) error {
				hookCalls++
				if err := postExit.Validate(); err != nil {
					t.Fatal(err)
				}
				if latestLifecycleTransition(postExit.Record,
					sandbox.DockerContainerLifecycleTransitionExited) == nil ||
					latestLifecycleTransition(postExit.Record,
						sandbox.DockerContainerLifecycleTransitionCleaning) != nil {
					t.Fatalf("post-exit hook did not receive the exact pre-cleaning checkpoint: %#v",
						postExit.Record.Transitions)
				}
				recorder.add("post-exit")
				return nil
			}))

	result, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "post-exit-order")
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || hookCalls != 1 || transport.deletes != 1 {
		t.Fatalf("post-exit hook or cleanup did not complete once: record=%#v hooks=%d deletes=%d",
			result, hookCalls, transport.deletes)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "transition:exited",
		"post-exit", "prepare:delete", "mutate:delete")
}

func TestDockerContainerLifecycleSupervisorPostExitFailureStillDeletesAndReplaysMetadataOnly(
	t *testing.T,
) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "post-exit-failure")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	hookCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecyclePostExit(DockerContainerLifecyclePostExitFunc(
			func(_ context.Context, _ DockerContainerLifecyclePostExitRequest) error {
				hookCalls++
				recorder.add("post-exit")
				return errors.New("private callback failure detail")
			}))

	first, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "post-exit-failure")
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), DockerContainerLifecyclePostExitFailureReason) ||
		strings.Contains(err.Error(), "private callback failure detail") {
		t.Fatalf("post-exit failure was not normalized to the stable boundary: %v", err)
	}
	if first.Receipt == nil || hookCalls != 1 || transport.deletes != 1 ||
		transport.state != sandbox.DockerContainerLifecycleStateAbsent {
		t.Fatalf("post-exit failure leaked the container: record=%#v hooks=%d deletes=%d state=%q",
			first, hookCalls, transport.deletes, transport.state)
	}
	second, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "post-exit-failure")
	if err != nil {
		t.Fatal(err)
	}
	if second.Receipt == nil || !second.Replayed || hookCalls != 1 || transport.deletes != 1 {
		t.Fatalf("completed metadata replay repeated post-exit work: record=%#v hooks=%d deletes=%d",
			second, hookCalls, transport.deletes)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "transition:exited",
		"post-exit", "mutate:delete", "receipt")
}

func TestDockerContainerLifecycleSupervisorConcurrentBeginHasOneDaemonOwner(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "concurrent-begin")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
		lookupBarrier: newDockerLifecycleLookupBarrier()}
	transports := []*dockerLifecycleSupervisorTestTransport{
		newDockerLifecycleSupervisorTransport(t, recorder,
			sandbox.DockerContainerLifecycleStateAbsent, ""),
		newDockerLifecycleSupervisorTransport(t, recorder,
			sandbox.DockerContainerLifecycleStateAbsent, ""),
	}
	supervisors := make([]*DockerContainerLifecycleSupervisor, len(transports))
	for index, transport := range transports {
		var err error
		supervisors[index], err = NewDockerContainerLifecycleSupervisor(store, transport,
			dockerLifecycleSupervisorTestAuthority{plan: plan, request: request},
			fmt.Sprintf("docker-lifecycle-supervisor-%d", index+1), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		supervisors[index].now = func() time.Time { return now }
	}

	start := make(chan struct{})
	errs := make([]error, len(supervisors))
	var workers sync.WaitGroup
	for index, supervisor := range supervisors {
		workers.Add(1)
		go func(index int, supervisor *DockerContainerLifecycleSupervisor) {
			defer workers.Done()
			<-start
			_, errs[index] = supervisor.BeginAndRun(context.Background(), plan, request,
				plan.RequestedBy, "same-concurrent-operation")
		}(index, supervisor)
	}
	close(start)
	workers.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch apperror.CodeOf(err) {
		case "":
			successes++
		case apperror.CodeConflict:
			conflicts++
		default:
			t.Fatalf("concurrent lifecycle returned unexpected error: %v", err)
		}
	}
	createWrites := transports[0].creates + transports[1].creates
	startWrites := transports[0].starts + transports[1].starts
	deleteWrites := transports[0].deletes + transports[1].deletes
	firstOwned := transports[0].creates+transports[0].starts+transports[0].deletes > 0
	secondOwned := transports[1].creates+transports[1].starts+transports[1].deletes > 0
	if successes != 1 || conflicts != 1 || store.beginCalls != 2 ||
		createWrites != 1 || startWrites != 1 || deleteWrites != 1 || firstOwned == secondOwned {
		t.Fatalf("concurrent lifecycle did not elect one daemon owner: errs=%v begins=%d writes=create:%d start:%d delete:%d perTransport=%#v/%#v",
			errs, store.beginCalls, createWrites, startWrites, deleteWrites,
			transports[0], transports[1])
	}
}

func TestDockerContainerLifecycleSupervisorStaleFencePreventsCreateSideEffect(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "stale-fence")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
		fenceErr: apperror.New(apperror.CodeConflict, "stale lifecycle owner")}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	_, err := supervisor.BeginAndRun(context.Background(), plan, request,
		plan.RequestedBy, "stale-before-create")
	if err == nil || transport.creates != 0 {
		t.Fatalf("stale fence reached create side effect: creates=%d err=%v",
			transport.creates, err)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "begin", "stage-owned",
		"prepare:create", "fence")
	for _, event := range recorder.snapshot() {
		if event == "mutate:create" {
			t.Fatalf("stale fence allowed create mutation: events=%v", recorder.snapshot())
		}
	}
}

func TestDockerContainerLifecycleSupervisorStartAuthorityStaleAndCancelledFailClosed(
	t *testing.T,
) {
	for _, test := range []struct {
		name      string
		cancel    bool
		fenceErr  error
		wantCalls int
	}{
		{name: "stale-lease", fenceErr: apperror.New(apperror.CodeConflict,
			"stale lifecycle start owner"), wantCalls: 0},
		{name: "cancelled-context", cancel: true, wantCalls: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, request := newDockerLifecycleSupervisorPlan(t,
				"start-authority-"+test.name)
			recorder := &dockerLifecycleTestRecorder{}
			now := time.Now().UTC()
			store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
				fenceErr: test.fenceErr}
			transport := newDockerLifecycleSupervisorTransport(t, recorder,
				sandbox.DockerContainerLifecycleStateAbsent, "")
			authorityCalls := 0
			supervisor := newDockerLifecycleSupervisorForTest(t, store, transport,
				plan, request, now).WithDockerContainerLifecycleStartAuthority(
				DockerContainerLifecycleStartAuthorityFunc(func(_ context.Context,
					_ DockerContainerLifecycleStartAuthorityRequest,
				) error {
					authorityCalls++
					return nil
				}))
			ctx := context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			result, err := supervisor.BeginAndRun(ctx, plan, request,
				plan.RequestedBy, "start-authority-"+test.name)
			if apperror.CodeOf(err) != apperror.CodePolicyDenied || result.Receipt == nil ||
				result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeFailed ||
				authorityCalls != test.wantCalls || transport.creates != 0 ||
				transport.starts != 0 || transport.deletes != 0 {
				t.Fatalf("%s start authority did not fail closed: record=%#v err=%v calls=%d creates=%d starts=%d deletes=%d",
					test.name, result, err, authorityCalls, transport.creates,
					transport.starts, transport.deletes)
			}
		})
	}
}

func TestDockerContainerLifecycleSupervisorRecoveryDoesNotRestartRunningContainer(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "running-recovery")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "", now.Add(-80*time.Second))
	started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionStarted,
		sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
		created.TransitionFingerprint, now.Add(-70*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created, started}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateRunning, resourceID)
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || store.acquireCalls != 1 || transport.startCalls != 0 ||
		transport.starts != 0 || transport.waitCalls != 1 || transport.deletes != 1 {
		t.Fatalf("running recovery duplicated or skipped work: record=%#v acquire=%d startCalls=%d starts=%d waits=%d deletes=%d",
			result, store.acquireCalls, transport.startCalls, transport.starts,
			transport.waitCalls, transport.deletes)
	}
}

func TestDockerContainerLifecycleSupervisorCreatedRecoveryDeniedDeletesWithoutStart(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "start-authority-created-denied")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-80*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateCreated, resourceID)
	authorityCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, authority DockerContainerLifecycleStartAuthorityRequest) error {
				authorityCalls++
				recorder.add("start-authority:" + authority.Action.String())
				return errors.New("start capability was revoked")
			}))

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("created recovery denial returned an unstable error: %v", err)
	}
	if result.Receipt == nil ||
		result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeFailed ||
		authorityCalls != 1 || transport.startCalls != 1 || transport.starts != 0 ||
		transport.waitCalls != 0 || transport.terminate != 0 || transport.deletes != 1 {
		t.Fatalf("created recovery started or failed to delete: record=%#v calls=%d startCalls=%d starts=%d waits=%d terminate=%d deletes=%d",
			result, authorityCalls, transport.startCalls, transport.starts,
			transport.waitCalls, transport.terminate, transport.deletes)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "start-authority:start",
		"transition:cleaning", "prepare:delete", "mutate:delete", "receipt")
}

func TestDockerContainerLifecycleSupervisorCreatedCleaningRecoveryNeverRechecksStart(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "start-authority-created-cleaning")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-80*time.Second))
	cleaning := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionCleaning,
		sandbox.DockerContainerLifecycleReasonCleanupStarted, nil, "",
		created.TransitionFingerprint, now.Add(-70*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created, cleaning}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateCreated, resourceID)
	authorityCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, _ DockerContainerLifecycleStartAuthorityRequest) error {
				authorityCalls++
				return nil
			}))

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil ||
		result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeFailed ||
		authorityCalls != 0 || transport.startCalls != 0 || transport.starts != 0 ||
		transport.deletes != 1 {
		t.Fatalf("created cleaning recovery crossed back into start authority: record=%#v calls=%d startCalls=%d starts=%d deletes=%d",
			result, authorityCalls, transport.startCalls, transport.starts, transport.deletes)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "acquire", "prepare:delete",
		"mutate:delete", "transition:cleaned", "receipt")
}

func TestDockerContainerLifecycleSupervisorRunningRecoveryDeniedTerminatesWithoutWait(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "start-authority-running-denied")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-80*time.Second))
	started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionStarted,
		sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
		created.TransitionFingerprint, now.Add(-70*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created, started}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateRunning, resourceID)
	authorityCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, authority DockerContainerLifecycleStartAuthorityRequest) error {
				authorityCalls++
				recorder.add("start-authority:" + authority.Action.String())
				return errors.New("permission snapshot no longer permits start")
			}))

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("running recovery denial returned an unstable error: %v", err)
	}
	if result.Receipt == nil ||
		result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeFailed ||
		authorityCalls != 1 || transport.startCalls != 0 || transport.starts != 0 ||
		transport.waitCalls != 0 || transport.terminate != 1 || transport.terms != 1 ||
		transport.deletes != 1 {
		t.Fatalf("running recovery waited/restarted or skipped cleanup: record=%#v calls=%d startCalls=%d starts=%d waits=%d terminate=%d terms=%d deletes=%d",
			result, authorityCalls, transport.startCalls, transport.starts,
			transport.waitCalls, transport.terminate, transport.terms, transport.deletes)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "start-authority:start",
		"transition:cleaning", "mutate:term", "transition:exited",
		"mutate:delete", "receipt")
}

func TestDockerContainerLifecycleSupervisorAbsentRecoveryDeniedWritesFailedCleanupReceipt(
	t *testing.T,
) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "start-authority-absent-denied")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-80*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, resourceID)
	authorityCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, _ DockerContainerLifecycleStartAuthorityRequest) error {
				authorityCalls++
				recorder.add("start-authority:start")
				return errors.New("capability expired")
			}))

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("absent recovery denial returned an unstable error: %v", err)
	}
	if result.Receipt == nil ||
		result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeFailed ||
		!result.Receipt.ContainerAlreadyAbsent || authorityCalls != 1 ||
		transport.creates != 0 || transport.starts != 0 || transport.terms != 0 ||
		transport.deletes != 0 {
		t.Fatalf("absent recovery wrote to Docker or missed failed cleanup receipt: record=%#v calls=%d writes=create:%d start:%d term:%d delete:%d",
			result, authorityCalls, transport.creates, transport.starts,
			transport.terms, transport.deletes)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "start-authority:start",
		"transition:cleaning", "transition:cleaned", "receipt")
}

func TestDockerContainerLifecycleSupervisorPostExitRecoveryIsIdempotent(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "post-exit-recovery")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-80*time.Second))
	started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionStarted,
		sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
		created.TransitionFingerprint, now.Add(-70*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created, started}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateExited, resourceID)
	hookCalls := 0
	invocationKey := ""
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecyclePostExit(DockerContainerLifecyclePostExitFunc(
			func(_ context.Context, postExit DockerContainerLifecyclePostExitRequest) error {
				hookCalls++
				invocationKey = postExit.InvocationKeyDigest
				recorder.add("post-exit")
				return nil
			}))

	first, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt == nil || second.Receipt == nil || !second.Replayed ||
		hookCalls != 1 || invocationKey == "" || transport.deletes != 1 ||
		store.acquireCalls != 1 || store.completeCalls != 1 {
		t.Fatalf("restart recovery repeated post-exit work: first=%#v second=%#v hooks=%d key=%q deletes=%d acquires=%d completes=%d",
			first, second, hookCalls, invocationKey, transport.deletes,
			store.acquireCalls, store.completeCalls)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "acquire", "transition:exited",
		"post-exit", "mutate:delete", "receipt")
}

func TestDockerContainerLifecycleSupervisorPostExitRunsOnceForTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		waitErr error
		cancel  bool
		outcome string
	}{
		{name: "timeout", waitErr: context.DeadlineExceeded,
			outcome: sandbox.DockerContainerLifecycleOutcomeTimedOut},
		{name: "cancelled", waitErr: context.Canceled, cancel: true,
			outcome: sandbox.DockerContainerLifecycleOutcomeCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, request := newDockerLifecycleSupervisorPlan(t, "post-exit-"+test.name)
			recorder := &dockerLifecycleTestRecorder{}
			now := time.Now().UTC()
			store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder}
			transport := newDockerLifecycleSupervisorTransport(t, recorder,
				sandbox.DockerContainerLifecycleStateAbsent, "")
			transport.waitErr = test.waitErr
			hookCalls := 0
			supervisor := newDockerLifecycleSupervisorForTest(t, store, transport,
				plan, request, now).WithDockerContainerLifecyclePostExit(
				DockerContainerLifecyclePostExitFunc(func(_ context.Context,
					_ DockerContainerLifecyclePostExitRequest,
				) error {
					hookCalls++
					recorder.add("post-exit")
					return nil
				}))
			ctx := context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			result, err := supervisor.BeginAndRun(ctx, plan, request,
				plan.RequestedBy, "post-exit-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			if result.Receipt == nil || result.Receipt.Outcome != test.outcome ||
				hookCalls != 1 || transport.terms != 1 || transport.deletes != 1 {
				t.Fatalf("%s lifecycle skipped or repeated post-exit: record=%#v hooks=%d terms=%d deletes=%d",
					test.name, result, hookCalls, transport.terms, transport.deletes)
			}
			assertDockerLifecycleEventOrder(t, recorder.snapshot(), "transition:exited",
				"post-exit", "mutate:delete")
		})
	}
}

func TestDockerContainerLifecycleSupervisorRecoveryBeforeCreateContinuesOnce(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "pre-create-recovery")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, _ := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	record := sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
		found: true, record: record}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || store.acquireCalls != 1 || transport.stageCalls != 1 ||
		transport.creates != 1 || transport.starts != 1 || transport.deletes != 1 {
		t.Fatalf("pre-create recovery did not converge once: record=%#v acquire=%d stage=%d create=%d start=%d delete=%d",
			result, store.acquireCalls, transport.stageCalls, transport.creates,
			transport.starts, transport.deletes)
	}
}

func TestDockerContainerLifecycleSupervisorRecoveryAdoptsCreateBeforeTransitionCommit(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "create-commit-window")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	createAction := newDockerLifecyclePreparedAction(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleActionCreate, now.Add(-110*time.Second))
	record := sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
		Actions: []sandbox.DockerContainerLifecyclePreparedAction{createAction}}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
		found: true, record: record}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateCreated, resourceID)
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || store.acquireCalls != 1 || transport.stageCalls != 1 ||
		transport.creates != 0 || transport.startCalls != 1 || transport.starts != 1 {
		t.Fatalf("create commit-window recovery was not idempotent: record=%#v acquire=%d stage=%d creates=%d startCalls=%d starts=%d",
			result, store.acquireCalls, transport.stageCalls, transport.creates,
			transport.startCalls, transport.starts)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "acquire", "stage-owned",
		"adopt:create", "transition:created")
	for _, event := range recorder.snapshot() {
		if event == "mutate:create" {
			t.Fatalf("create commit-window recovery duplicated create: events=%v",
				recorder.snapshot())
		}
	}
}

func TestDockerContainerLifecycleSupervisorRecoveryObservesStartBeforeTransitionCommit(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "start-commit-window")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	createAction := newDockerLifecyclePreparedAction(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleActionCreate, now.Add(-115*time.Second))
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-110*time.Second))
	startAction := newDockerLifecyclePreparedAction(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleActionStart, now.Add(-105*time.Second))
	record := sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
		Actions:     []sandbox.DockerContainerLifecyclePreparedAction{createAction, startAction},
		Transitions: []sandbox.DockerContainerLifecycleTransition{created}}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
		found: true, record: record}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateRunning, resourceID)
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || store.acquireCalls != 1 || transport.stageCalls != 0 ||
		transport.startCalls != 0 || transport.starts != 0 || transport.waitCalls != 1 {
		t.Fatalf("start commit-window recovery was not idempotent: record=%#v acquire=%d stage=%d startCalls=%d starts=%d waits=%d",
			result, store.acquireCalls, transport.stageCalls, transport.startCalls,
			transport.starts, transport.waitCalls)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "acquire", "transition:started")
	for _, event := range recorder.snapshot() {
		if event == "mutate:start" {
			t.Fatalf("start commit-window recovery duplicated start: events=%v",
				recorder.snapshot())
		}
	}
}

func TestDockerContainerLifecycleSupervisorAbsentAfterCleaningCommitsOneReceipt(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "absent-recovery")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	exitCode := 0
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "", now.Add(-80*time.Second))
	started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionStarted,
		sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
		created.TransitionFingerprint, now.Add(-70*time.Second))
	exited := newDockerLifecycleTransition(t, intent.ID, 3, lease,
		sandbox.DockerContainerLifecycleTransitionExited,
		sandbox.DockerContainerLifecycleReasonNaturalExit, &exitCode, resourceID,
		started.TransitionFingerprint, now.Add(-60*time.Second))
	cleaning := newDockerLifecycleTransition(t, intent.ID, 4, lease,
		sandbox.DockerContainerLifecycleTransitionCleaning,
		sandbox.DockerContainerLifecycleReasonNaturalExit, nil, "",
		exited.TransitionFingerprint, now.Add(-50*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{
				created, started, exited, cleaning,
			}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, resourceID)
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	first, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt == nil || second.Receipt == nil || !second.Replayed ||
		store.completeCalls != 1 || store.acquireCalls != 1 || transport.cleanup != 0 {
		t.Fatalf("absent recovery was not idempotent: first=%#v second=%#v completes=%d acquires=%d cleanup=%d",
			first, second, store.completeCalls, store.acquireCalls, transport.cleanup)
	}
}

func TestDockerContainerLifecycleSupervisorCleanedWithoutReceiptTakeover(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "cleaned-no-receipt")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	exitCode := 0
	createAction := newDockerLifecyclePreparedAction(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleActionCreate, now.Add(-119*time.Second))
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-115*time.Second))
	startAction := newDockerLifecyclePreparedAction(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleActionStart, now.Add(-112*time.Second))
	started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionStarted,
		sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
		created.TransitionFingerprint, now.Add(-108*time.Second))
	exited := newDockerLifecycleTransition(t, intent.ID, 3, lease,
		sandbox.DockerContainerLifecycleTransitionExited,
		sandbox.DockerContainerLifecycleReasonNaturalExit, &exitCode, resourceID,
		started.TransitionFingerprint, now.Add(-100*time.Second))
	cleaning := newDockerLifecycleTransition(t, intent.ID, 4, lease,
		sandbox.DockerContainerLifecycleTransitionCleaning,
		sandbox.DockerContainerLifecycleReasonNaturalExit, nil, "",
		exited.TransitionFingerprint, now.Add(-95*time.Second))
	deleteAction := newDockerLifecyclePreparedAction(t, intent.ID, 3, lease,
		sandbox.DockerContainerLifecycleActionDelete, now.Add(-90*time.Second))
	cleaned := newDockerLifecycleTransition(t, intent.ID, 5, lease,
		sandbox.DockerContainerLifecycleTransitionCleaned,
		sandbox.DockerContainerLifecycleReasonCleanupCompleted, nil, "",
		cleaning.TransitionFingerprint, now.Add(-85*time.Second))
	record := sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
		Actions: []sandbox.DockerContainerLifecyclePreparedAction{
			createAction, startAction, deleteAction,
		},
		Transitions: []sandbox.DockerContainerLifecycleTransition{
			created, started, exited, cleaning, cleaned,
		}}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
		found: true, record: record}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, resourceID)
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	first, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt == nil || second.Receipt == nil || !second.Replayed ||
		first.Receipt.FinalTransitionFingerprint != cleaned.TransitionFingerprint ||
		first.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeNaturalExit ||
		store.acquireCalls != 1 || store.completeCalls != 1 || transport.cleanup != 0 ||
		len(first.Transitions) != len(record.Transitions) {
		t.Fatalf("cleaned-without-receipt takeover was not idempotent: first=%#v second=%#v acquires=%d completes=%d cleanup=%d",
			first, second, store.acquireCalls, store.completeCalls, transport.cleanup)
	}
}

func TestDockerContainerLifecycleSupervisorCleaningRecoveryTerminatesWithStickyOutcome(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "cleaning-sticky-recovery")
	tests := []struct {
		name    string
		reason  string
		outcome string
	}{
		{name: "timeout", reason: sandbox.DockerContainerLifecycleReasonTimeout,
			outcome: sandbox.DockerContainerLifecycleOutcomeTimedOut},
		{name: "cancelled", reason: sandbox.DockerContainerLifecycleReasonCancelled,
			outcome: sandbox.DockerContainerLifecycleOutcomeCancelled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &dockerLifecycleTestRecorder{}
			now := time.Now().UTC()
			intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
			createAction := newDockerLifecyclePreparedAction(t, intent.ID, 1, lease,
				sandbox.DockerContainerLifecycleActionCreate, now.Add(-119*time.Second))
			created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
				sandbox.DockerContainerLifecycleTransitionCreated,
				sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
				now.Add(-115*time.Second))
			startAction := newDockerLifecyclePreparedAction(t, intent.ID, 2, lease,
				sandbox.DockerContainerLifecycleActionStart, now.Add(-112*time.Second))
			started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
				sandbox.DockerContainerLifecycleTransitionStarted,
				sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
				created.TransitionFingerprint, now.Add(-108*time.Second))
			cleaning := newDockerLifecycleTransition(t, intent.ID, 3, lease,
				sandbox.DockerContainerLifecycleTransitionCleaning, test.reason, nil, "",
				started.TransitionFingerprint, now.Add(-100*time.Second))
			record := sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
				Actions: []sandbox.DockerContainerLifecyclePreparedAction{
					createAction, startAction,
				},
				Transitions: []sandbox.DockerContainerLifecycleTransition{
					created, started, cleaning,
				}}
			if err := record.Validate(); err != nil {
				t.Fatal(err)
			}
			store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
				found: true, record: record}
			transport := newDockerLifecycleSupervisorTransport(t, recorder,
				sandbox.DockerContainerLifecycleStateRunning, resourceID)
			supervisor := newDockerLifecycleSupervisorForTest(t, store, transport,
				plan, request, now)

			first, err := supervisor.RecoverOne(context.Background(), intent.ID)
			if err != nil {
				t.Fatal(err)
			}
			second, err := supervisor.RecoverOne(context.Background(), intent.ID)
			if err != nil {
				t.Fatal(err)
			}
			exited := latestLifecycleTransition(first,
				sandbox.DockerContainerLifecycleTransitionExited)
			if first.Receipt == nil || second.Receipt == nil || !second.Replayed ||
				first.Receipt.Outcome != test.outcome || exited == nil ||
				exited.ReasonCode != test.reason || transport.waitCalls != 0 ||
				transport.terminate != 1 || transport.terms != 1 || transport.deletes != 1 ||
				store.acquireCalls != 1 || store.completeCalls != 1 {
				t.Fatalf("%s cleaning recovery lost sticky outcome or repeated cleanup: first=%#v second=%#v exited=%#v wait=%d terminate=%d term=%d delete=%d acquire=%d complete=%d",
					test.name, first, second, exited, transport.waitCalls,
					transport.terminate, transport.terms, transport.deletes,
					store.acquireCalls, store.completeCalls)
			}
		})
	}
}

func TestDockerContainerLifecycleSupervisorCancelOneConvergesAbsentCreatedAndRunning(
	t *testing.T,
) {
	for _, test := range []struct {
		state          string
		wantDeletes    int
		wantTerminates int
		wantAbsent     bool
	}{
		{state: sandbox.DockerContainerLifecycleStateAbsent, wantAbsent: true},
		{state: sandbox.DockerContainerLifecycleStateCreated, wantDeletes: 1},
		{state: sandbox.DockerContainerLifecycleStateRunning, wantDeletes: 1,
			wantTerminates: 1},
	} {
		t.Run(test.state, func(t *testing.T) {
			plan, request := newDockerLifecycleSupervisorPlan(t, "cancel-one-"+test.state)
			recorder := &dockerLifecycleTestRecorder{}
			now := time.Now().UTC()
			intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
			transitions := make([]sandbox.DockerContainerLifecycleTransition, 0, 2)
			if test.state != sandbox.DockerContainerLifecycleStateAbsent {
				created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
					sandbox.DockerContainerLifecycleTransitionCreated,
					sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
					now.Add(-80*time.Second))
				transitions = append(transitions, created)
				if test.state == sandbox.DockerContainerLifecycleStateRunning {
					started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
						sandbox.DockerContainerLifecycleTransitionStarted,
						sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
						created.TransitionFingerprint, now.Add(-70*time.Second))
					transitions = append(transitions, started)
				}
			}
			record := sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
				Transitions: transitions}
			if err := record.Validate(); err != nil {
				t.Fatal(err)
			}
			store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder,
				found: true, record: record}
			transportResourceID := resourceID
			if test.state == sandbox.DockerContainerLifecycleStateAbsent {
				transportResourceID = ""
			}
			transport := newDockerLifecycleSupervisorTransport(t, recorder,
				test.state, transportResourceID)
			startAuthorityCalls := 0
			supervisor := newDockerLifecycleSupervisorForTest(t, store, transport,
				plan, request, now).WithDockerContainerLifecycleStartAuthority(
				DockerContainerLifecycleStartAuthorityFunc(func(_ context.Context,
					_ DockerContainerLifecycleStartAuthorityRequest,
				) error {
					startAuthorityCalls++
					return errors.New("cancel convergence must not request start authority")
				}))

			result, err := supervisor.CancelOne(context.Background(), intent.ID)
			if err != nil {
				t.Fatal(err)
			}
			cleaning := latestLifecycleTransition(result,
				sandbox.DockerContainerLifecycleTransitionCleaning)
			if result.Receipt == nil ||
				result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeCancelled ||
				result.Receipt.ContainerAlreadyAbsent != test.wantAbsent || cleaning == nil ||
				cleaning.ReasonCode != sandbox.DockerContainerLifecycleReasonCancelled ||
				startAuthorityCalls != 0 || transport.stageCalls != 0 ||
				transport.creates != 0 || transport.startCalls != 0 || transport.starts != 0 ||
				transport.waitCalls != 0 || transport.terminate != test.wantTerminates ||
				transport.deletes != test.wantDeletes {
				t.Fatalf("%s cancellation did not converge without start: record=%#v cleaning=%#v auth=%d stage=%d create=%d startCalls=%d starts=%d waits=%d terminates=%d deletes=%d",
					test.state, result, cleaning, startAuthorityCalls, transport.stageCalls,
					transport.creates, transport.startCalls, transport.starts,
					transport.waitCalls, transport.terminate, transport.deletes)
			}
			assertDockerLifecycleEventOrder(t, recorder.snapshot(), "acquire",
				"transition:cleaning", "transition:cleaned", "receipt")
		})
	}
}

func TestDockerContainerLifecycleSupervisorCancelOneExitedRunsPostExitOnceAndReplays(
	t *testing.T,
) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "cancel-one-exited")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	exitCode := 0
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-90*time.Second))
	started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionStarted,
		sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
		created.TransitionFingerprint, now.Add(-80*time.Second))
	exited := newDockerLifecycleTransition(t, intent.ID, 3, lease,
		sandbox.DockerContainerLifecycleTransitionExited,
		sandbox.DockerContainerLifecycleReasonNaturalExit, &exitCode, resourceID,
		started.TransitionFingerprint, now.Add(-70*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created, started, exited}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateExited, resourceID)
	postExitCalls, startAuthorityCalls := 0, 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, _ DockerContainerLifecycleStartAuthorityRequest) error {
				startAuthorityCalls++
				return nil
			})).
		WithDockerContainerLifecyclePostExit(DockerContainerLifecyclePostExitFunc(
			func(_ context.Context, postExit DockerContainerLifecyclePostExitRequest) error {
				postExitCalls++
				if err := postExit.Validate(); err != nil {
					t.Fatal(err)
				}
				recorder.add("post-exit")
				return nil
			}))

	first, err := supervisor.CancelOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.CancelOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt == nil ||
		first.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeCancelled ||
		second.Receipt == nil || !second.Replayed || postExitCalls != 1 ||
		startAuthorityCalls != 0 || transport.startCalls != 0 || transport.waitCalls != 0 ||
		transport.terminate != 0 || transport.deletes != 1 || store.acquireCalls != 1 {
		t.Fatalf("exited cancellation was not idempotent: first=%#v second=%#v postExit=%d startAuth=%d start=%d wait=%d terminate=%d delete=%d acquire=%d",
			first, second, postExitCalls, startAuthorityCalls, transport.startCalls,
			transport.waitCalls, transport.terminate, transport.deletes, store.acquireCalls)
	}
	assertDockerLifecycleEventOrder(t, recorder.snapshot(), "transition:cleaning",
		"post-exit", "mutate:delete", "receipt")
}

func TestDockerContainerLifecycleSupervisorRecoversDurableCancellationWithoutStart(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "cancel-one-crash-recovery")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, lease, resourceID := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	created := newDockerLifecycleTransition(t, intent.ID, 1, lease,
		sandbox.DockerContainerLifecycleTransitionCreated,
		sandbox.DockerContainerLifecycleReasonCreated, nil, resourceID, "",
		now.Add(-90*time.Second))
	started := newDockerLifecycleTransition(t, intent.ID, 2, lease,
		sandbox.DockerContainerLifecycleTransitionStarted,
		sandbox.DockerContainerLifecycleReasonStarted, nil, resourceID,
		created.TransitionFingerprint, now.Add(-80*time.Second))
	cleaning := newDockerLifecycleTransition(t, intent.ID, 3, lease,
		sandbox.DockerContainerLifecycleTransitionCleaning,
		sandbox.DockerContainerLifecycleReasonCancelled, nil, "",
		started.TransitionFingerprint, now.Add(-70*time.Second))
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: lease,
			Transitions: []sandbox.DockerContainerLifecycleTransition{created, started, cleaning}}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateRunning, resourceID)
	startAuthorityCalls := 0
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now).
		WithDockerContainerLifecycleStartAuthority(DockerContainerLifecycleStartAuthorityFunc(
			func(_ context.Context, _ DockerContainerLifecycleStartAuthorityRequest) error {
				startAuthorityCalls++
				return nil
			}))

	result, err := supervisor.RecoverOne(context.Background(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil ||
		result.Receipt.Outcome != sandbox.DockerContainerLifecycleOutcomeCancelled ||
		startAuthorityCalls != 0 || transport.startCalls != 0 || transport.waitCalls != 0 ||
		transport.terminate != 1 || transport.deletes != 1 {
		t.Fatalf("durable cancellation recovery restarted or waited: record=%#v auth=%d start=%d wait=%d terminate=%d delete=%d",
			result, startAuthorityCalls, transport.startCalls, transport.waitCalls,
			transport.terminate, transport.deletes)
	}
}

func TestDockerContainerLifecycleSupervisorCancelOneConflictsWithActiveOwner(t *testing.T) {
	plan, request := newDockerLifecycleSupervisorPlan(t, "cancel-one-active-owner")
	recorder := &dockerLifecycleTestRecorder{}
	now := time.Now().UTC()
	intent, _, _ := newDockerLifecycleRecoveryIntent(t, plan, request, now)
	activeLease, err := sandbox.NewDockerContainerLifecycleLease(intent,
		"docker-lifecycle-active-cancel-lease", "active-lifecycle-owner", 1,
		now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store := &dockerLifecycleSupervisorTestStore{now: now, recorder: recorder, found: true,
		record: sandbox.DockerContainerLifecycleRecord{Intent: intent, Lease: activeLease}}
	transport := newDockerLifecycleSupervisorTransport(t, recorder,
		sandbox.DockerContainerLifecycleStateAbsent, "")
	supervisor := newDockerLifecycleSupervisorForTest(t, store, transport, plan, request, now)

	_, err = supervisor.CancelOne(context.Background(), intent.ID)
	if apperror.CodeOf(err) != apperror.CodeConflict || store.acquireCalls != 1 ||
		len(store.record.Transitions) != 0 || transport.stageCalls != 0 ||
		transport.creates != 0 || transport.starts != 0 || transport.terms != 0 ||
		transport.deletes != 0 {
		t.Fatalf("active owner cancellation did not conflict before mutation: err=%v acquire=%d transitions=%v stage=%d create=%d start=%d term=%d delete=%d",
			err, store.acquireCalls, store.record.Transitions, transport.stageCalls,
			transport.creates, transport.starts, transport.terms, transport.deletes)
	}
}

func TestDockerContainerLifecycleApplicationErrorCodesAreStableAndFailClosed(t *testing.T) {
	tests := []struct {
		reason string
		want   apperror.Code
	}{
		{sandbox.DockerContainerLifecycleFailureDisabled, apperror.CodeUnavailable},
		{sandbox.DockerContainerLifecycleFailureUnsupported, apperror.CodeUnavailable},
		{sandbox.DockerContainerLifecycleFailureConnection, apperror.CodeUnavailable},
		{sandbox.DockerContainerLifecycleFailureInvalidResponse, apperror.CodeFailedPrecondition},
		{sandbox.DockerContainerLifecycleFailureConfigMismatch, apperror.CodeFailedPrecondition},
		{sandbox.DockerContainerLifecycleFailureUnsafeExisting, apperror.CodeFailedPrecondition},
		{sandbox.DockerContainerLifecycleReasonTimeout, apperror.CodeDeadlineExceeded},
		{sandbox.DockerContainerLifecycleReasonCancelled, apperror.CodeCancelled},
	}
	for _, test := range tests {
		if got := dockerLifecycleApplicationCode(test.reason); got != test.want {
			t.Errorf("reason %q mapped to %q, want %q", test.reason, got, test.want)
		}
	}
}

func newDockerLifecycleSupervisorPlan(t *testing.T, prefix string,
) (sandbox.DockerContainerPlan, sandbox.DockerContainerWriteRequest) {
	t.Helper()
	ctx := context.Background()
	st, run, root := newSandboxManifestTestRuntime(t, ctx)
	service := NewSandboxManifestService(st, policy.NewDefaultChecker())
	requestedBy := "docker_lifecycle_operator"
	manifest, observation := prepareDockerContainerPlanAuthority(t, ctx, service, run.ID,
		root, prefix, requestedBy)
	service.WithDockerContainerTransactionHarness(sandbox.NewInMemoryDockerWriteTransaction())
	plan, err := service.CompileDockerContainerPlan(ctx, CompileDockerContainerPlanRequest{
		ObservationID: observation.ID,
		Manifest:      manifest,
		OperationKey:  prefix + "-plan",
		RequestedBy:   requestedBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := sandbox.CompileDockerContainerSpec(ctx, observation, manifest)
	if err != nil {
		t.Fatal(err)
	}
	request, err := sandbox.NewDockerContainerWriteRequest(ctx, root, spec)
	if err != nil {
		t.Fatal(err)
	}
	return plan, request
}

func newDockerLifecycleSupervisorTransport(t *testing.T,
	recorder *dockerLifecycleTestRecorder, state, resourceID string,
) *dockerLifecycleSupervisorTestTransport {
	t.Helper()
	endpoint, err := sandbox.NewDockerObservationEndpoint(
		sandbox.DockerObservationEndpointLocalUnix)
	if err != nil {
		t.Fatal(err)
	}
	return &dockerLifecycleSupervisorTestTransport{endpoint: endpoint, recorder: recorder,
		state: state, resourceID: resourceID}
}

func newDockerLifecycleSupervisorForTest(t *testing.T,
	store *dockerLifecycleSupervisorTestStore,
	transport *dockerLifecycleSupervisorTestTransport,
	plan sandbox.DockerContainerPlan, request sandbox.DockerContainerWriteRequest,
	now time.Time,
) *DockerContainerLifecycleSupervisor {
	t.Helper()
	supervisor, err := NewDockerContainerLifecycleSupervisor(store, transport,
		dockerLifecycleSupervisorTestAuthority{plan: plan, request: request},
		"docker-lifecycle-supervisor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.now = func() time.Time { return now }
	return supervisor
}

func newDockerLifecycleRecoveryIntent(t *testing.T,
	plan sandbox.DockerContainerPlan, request sandbox.DockerContainerWriteRequest,
	now time.Time,
) (sandbox.DockerContainerLaunchIntent, sandbox.DockerContainerLifecycleLease, string) {
	t.Helper()
	endpoint, err := sandbox.NewDockerObservationEndpoint(
		sandbox.DockerObservationEndpointLocalUnix)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := sandbox.NewDockerContainerLaunchIntent(
		"docker-lifecycle-recovery-intent", "docker-lifecycle-recovery-attempt",
		strings.Repeat("b", 64), plan, request, endpoint, plan.RequestedBy, 1,
		now.Add(-3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := sandbox.NewDockerContainerLifecycleLease(intent,
		"docker-lifecycle-expired-lease", "crashed-supervisor", 1,
		now.Add(-2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := sandbox.NewDockerContainerStageResult(endpoint, request,
		strings.Repeat("c", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	return intent, lease, stage.ContainerIDFingerprint
}

func newDockerLifecycleTransition(t *testing.T, intentID string, ordinal int,
	lease sandbox.DockerContainerLifecycleLease, state, reason string, exitCode *int,
	resourceID, previous string, at time.Time,
) sandbox.DockerContainerLifecycleTransition {
	t.Helper()
	transition, err := sandbox.NewDockerContainerLifecycleTransition(intentID, ordinal,
		lease, state, reason, exitCode, resourceID, previous, at)
	if err != nil {
		t.Fatal(err)
	}
	return transition
}

func newDockerLifecyclePreparedAction(t *testing.T, intentID string, ordinal int,
	lease sandbox.DockerContainerLifecycleLease,
	action sandbox.DockerContainerLifecycleActionKind, at time.Time,
) sandbox.DockerContainerLifecyclePreparedAction {
	t.Helper()
	prepared, err := sandbox.NewDockerContainerLifecyclePreparedAction(intentID, ordinal,
		lease, string(action), at)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func assertDockerLifecycleEventOrder(t *testing.T, events []string, expected ...string) {
	t.Helper()
	position := 0
	for _, event := range events {
		if position < len(expected) && event == expected[position] {
			position++
		}
	}
	if position != len(expected) {
		t.Fatalf("Docker lifecycle event order mismatch: expected=%v events=%v", expected, events)
	}
}
