package store

import (
	"fmt"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
)

func peerStoreRun(t *testing.T, st *SQLiteStore, title string) domain.Run {
	t.Helper()
	service := application.NewRunService(st)
	_, created, err := service.Create(t.Context(), application.CreateRunRequest{Goal: title, Profile: "review", ModelRoute: "test/model", Interactive: true, Budget: domain.Budget{MaxTurns: 20}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestThreadPeerStoreAutomaticRepliesAreFencedAndBounded(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	a := peerStoreRun(t, st, "A")
	b := peerStoreRun(t, st, "B")
	first, err := st.EnqueueThreadPeerMessage(ctx, domain.SendThreadPeerMessageRequest{SourceThreadID: domain.InitialThreadID(a.ID), TargetThreadID: domain.InitialThreadID(b.ID), Content: "Please share results within your existing scope", OperationKey: "peer-store-first-user-message", RequestedBy: "operator"}, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	current := first.Message
	leases := map[string]domain.RunExecutionLease{a.ID: acquireTestRunExecutionLease(t, ctx, st, a.ID), b.ID: acquireTestRunExecutionLease(t, ctx, st, b.ID)}
	for depth := 1; depth <= domain.MaxThreadPeerReplyDepth+1; depth++ {
		sender, recipient := b, a
		if depth%2 == 0 {
			sender, recipient = a, b
		}
		turn, err := st.BeginSupervisorTurn(ctx, leases[sender.ID], "")
		if err != nil {
			t.Fatal(err)
		}
		input, err := st.PreparedSupervisorInputMessage(ctx, turn.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if input.SessionID != sender.SessionID || input.Provenance.SourceKind != session.SourcePeerMessage || input.Provenance.InstructionAuthorized {
			t.Fatalf("sender input provenance: %+v", input)
		}
		request := domain.SendThreadPeerMessageRequest{SourceThreadID: domain.InitialThreadID(sender.ID), TargetThreadID: domain.InitialThreadID(recipient.ID), Content: fmt.Sprintf("Reply %d", depth), ReplyTo: current.ID, OperationKey: fmt.Sprintf("peer-store-model-reply-%02d", depth), RequestedBy: "run_supervisor", ModelInitiated: true, SourceRunID: sender.ID, SourceAttemptID: turn.Checkpoint.AttemptID, SourceLeaseID: leases[sender.ID].LeaseID, SourceLeaseGeneration: leases[sender.ID].Generation}
		unlinked := request
		unlinked.ReplyTo = ""
		unlinked.OperationKey += "-unlinked"
		if _, err := st.EnqueueThreadPeerMessage(ctx, unlinked, recipient.ID); apperror.CodeOf(err) != apperror.CodePolicyDenied {
			t.Fatalf("peer-triggered unlinked send accepted: %v", err)
		}
		stale := request
		stale.SourceLeaseGeneration++
		stale.OperationKey += "-stale"
		if _, err := st.EnqueueThreadPeerMessage(ctx, stale, recipient.ID); err == nil {
			t.Fatal("stale lease sent peer message")
		}
		reply, err := st.EnqueueThreadPeerMessage(ctx, request, recipient.ID)
		if depth > domain.MaxThreadPeerReplyDepth {
			if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
				t.Fatalf("automatic reply limit: %v", err)
			}
			request.ModelInitiated = false
			request.OperationKey += "-human"
			human, err := st.EnqueueThreadPeerMessage(ctx, request, recipient.ID)
			if err != nil || human.Message.Depth != 0 {
				t.Fatalf("human reply did not reset chain: %+v %v", human, err)
			}
			break
		}
		if err != nil || reply.Message.Depth != depth {
			t.Fatalf("automatic reply %d: %+v %v", depth, reply, err)
		}
		checkpoint, response := recordOperatorSteeringModelSuccess(t, ctx, st, turn.Checkpoint, "Reply sent")
		_, _, messages, err := st.CompleteSupervisorTurn(ctx, checkpoint, response, domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionFinish, Message: response.Text, Summary: "done"}, policy.Decision{Allowed: true}, 0)
		if err != nil || messages.User.Provenance.SourceKind != session.SourcePeerMessage || messages.User.Provenance.InstructionAuthorized {
			t.Fatalf("peer completion reclassified input: %+v %v", messages, err)
		}
		if err := st.FailThreadPeerMessage(ctx, current.ID, "later failure must not undo delivery"); err != nil {
			t.Fatal(err)
		}
		delivered, err := st.GetThreadPeerMessage(ctx, current.ID)
		if err != nil || delivered.Status != "delivered" {
			t.Fatalf("delivered input changed outcome: %+v %v", delivered, err)
		}
		current = reply.Message
	}
}

func TestThreadPeerStoreBindingPreventsOperatorPromotion(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	source := peerStoreRun(t, st, "Source")
	target := peerStoreRun(t, st, "Target")
	receipt, err := st.EnqueueThreadPeerMessage(ctx, domain.SendThreadPeerMessageRequest{SourceThreadID: domain.InitialThreadID(source.ID), TargetThreadID: domain.InitialThreadID(target.ID), Content: "peer text", OperationKey: "peer-store-provenance-binding", RequestedBy: "operator"}, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := st.SaveSessionMessage(ctx, session.NewMessage(target.SessionID, "user", receipt.Message.Content))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE operator_steering_messages SET status='committed',session_message_id=?,committed_at=created_at WHERE id=?`, forged.ID, receipt.Message.SteeringMessageID); err == nil {
		t.Fatal("peer message accepted authorized operator Session binding")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE thread_peer_messages SET source_thread_id=target_thread_id WHERE id=?`, receipt.Message.ID); err == nil {
		t.Fatal("peer sender identity was mutable")
	}
	if _, err := st.ReviseOperatorSteering(ctx, domain.ReviseOperatorSteeringRequest{SessionID: target.SessionID, MessageID: receipt.Message.SteeringMessageID, ExpectedRevision: 0, Content: "changed", OperationKey: "peer-store-recipient-revision", RequestedBy: "operator"}); err == nil {
		t.Fatal("recipient edited peer input")
	}
	cancelled, err := st.CancelOperatorSteering(ctx, domain.CancelOperatorSteeringRequest{MessageID: receipt.Message.SteeringMessageID, OperationKey: "peer-store-cancel-message-key", RequestedBy: "operator", Reason: "cancel before delivery"})
	if err != nil || cancelled.Message.Status != domain.OperatorSteeringCancelled {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	message, err := st.GetThreadPeerMessage(ctx, receipt.Message.ID)
	if err != nil || message.Status != "cancelled" {
		t.Fatalf("cancel receipt: %+v %v", message, err)
	}
}

func TestThreadPeerStoreFailedDeliveryCancelsInputAndSchedulingIncludesOtherTargets(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	source := peerStoreRun(t, st, "Source")
	blocked := peerStoreRun(t, st, "Blocked target")
	ready := peerStoreRun(t, st, "Ready target")
	var first domain.ThreadPeerMessage
	for i := range domain.MaxPendingOperatorSteering {
		receipt, err := st.EnqueueThreadPeerMessage(ctx, domain.SendThreadPeerMessageRequest{SourceThreadID: domain.InitialThreadID(source.ID), TargetThreadID: domain.InitialThreadID(blocked.ID), Content: "Wait at the next boundary", OperationKey: fmt.Sprintf("peer-fairness-blocked-%03d", i), RequestedBy: "operator"}, blocked.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = receipt.Message
		}
	}
	other, err := st.EnqueueThreadPeerMessage(ctx, domain.SendThreadPeerMessageRequest{SourceThreadID: domain.InitialThreadID(source.ID), TargetThreadID: domain.InitialThreadID(ready.ID), Content: "Dispatch this target", OperationKey: "peer-fairness-ready-operation", RequestedBy: "operator"}, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPendingThreadPeerMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].ID != first.ID || pending[1].ID != other.Message.ID {
		t.Fatalf("one target hid another target's input: %+v", pending)
	}
	if err := st.FailThreadPeerMessage(ctx, other.Message.ID, "Recipient was archived before dispatch"); err != nil {
		t.Fatal(err)
	}
	failed, err := st.GetThreadPeerMessage(ctx, other.Message.ID)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("failed receipt: %+v %v", failed, err)
	}
	input, err := st.GetOperatorSteering(ctx, other.Message.SteeringMessageID)
	if err != nil || input.Status != domain.OperatorSteeringCancelled {
		t.Fatalf("failed input remained consumable: %+v %v", input, err)
	}
	queue, err := st.GetOperatorSteeringQueueSummary(ctx, ready.ID)
	if err != nil || queue.Pending+queue.Prepared != 0 {
		t.Fatalf("failed input remained in handoff queue: %+v %v", queue, err)
	}
}

func TestThreadPeerStoreFailedDeliveryBeforeRecipientStarts(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	source := peerStoreRun(t, st, "Source")
	_, target, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{Goal: "Unstarted recipient", Profile: "review", ModelRoute: "test/model", Interactive: true, Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := st.EnqueueThreadPeerMessage(ctx, domain.SendThreadPeerMessageRequest{SourceThreadID: domain.InitialThreadID(source.ID), TargetThreadID: domain.InitialThreadID(target.ID), Content: "unstarted peer input", OperationKey: "peer-store-unstarted-message", RequestedBy: "operator"}, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FailThreadPeerMessage(ctx, receipt.Message.ID, "Recipient was archived before starting"); err != nil {
		t.Fatal(err)
	}
	failed, err := st.GetThreadPeerMessage(ctx, receipt.Message.ID)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("unstarted failure: %+v %v", failed, err)
	}
	input, err := st.GetOperatorSteering(ctx, receipt.Message.SteeringMessageID)
	if err != nil || input.Status != domain.OperatorSteeringCancelled {
		t.Fatalf("unstarted failed input remained consumable: %+v %v", input, err)
	}
}
