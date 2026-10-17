package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
)

// RunDue uses the existing scheduler lifetime and a durable lease. The lease
// spans the whole Thread turn; a second desktop process cannot submit it again.
func (s *PRFollowupService) RunDue(ctx context.Context, owner string, now time.Time) (bool, error) {
	state, found, err := s.store.ClaimDuePRFollowup(ctx, owner, now)
	if err != nil || !found {
		return found, err
	}
	lease := state
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.store.ReleasePRFollowupLease(releaseCtx, lease.Record.ID, lease.LeaseOwner, lease.LeaseToken)
	}()
	if !now.Before(state.Record.DeadlineAt) {
		state.Record.Status = domain.PRFollowupExhausted
		state.Record.LastResult = "持续跟进已到期"
		return true, s.saveFollowup(ctx, &state)
	}
	runCtx, cancel := context.WithDeadline(ctx, state.Record.DeadlineAt)
	s.activeMu.Lock()
	s.activeID, s.activeCancel = state.Record.ID, cancel
	s.activeMu.Unlock()
	defer func() {
		cancel()
		s.activeMu.Lock()
		s.activeID, s.activeCancel = "", nil
		s.activeMu.Unlock()
	}()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				ok, renewErr := s.store.RenewPRFollowupLease(runCtx, lease.Record.ID, lease.LeaseOwner, lease.LeaseToken, s.now())
				if renewErr != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	err = s.runFollowup(runCtx, &state)
	cancel()
	<-stopped
	return true, err
}

func (s *PRFollowupService) saveFollowup(ctx context.Context, state *domain.PRFollowupState) error {
	updated, err := s.store.SavePRFollowup(ctx, *state, state.Record.Revision)
	if err == nil {
		*state = updated
	}
	return err
}

func (s *PRFollowupService) deferFollowup(ctx context.Context, state *domain.PRFollowupState, result string, err error) error {
	state.Record.NextCheckAt = s.now().Add(time.Duration(state.Record.IntervalSeconds) * time.Second)
	if state.Record.NextCheckAt.After(state.Record.DeadlineAt) {
		state.Record.NextCheckAt = state.Record.DeadlineAt
	}
	state.Record.LastResult, state.Record.LastError = result, ""
	if err != nil {
		state.Record.LastError = redact.String(err.Error())
	}
	return s.saveFollowup(ctx, state)
}

func (s *PRFollowupService) blockFollowup(ctx context.Context, state *domain.PRFollowupState, reason string) error {
	state.Record.Status = domain.PRFollowupBlocked
	state.Record.LastResult = "持续跟进需要你处理后继续"
	state.Record.LastError = redact.String(reason)
	return s.saveFollowup(ctx, state)
}

func (s *PRFollowupService) boundFollowup(ctx context.Context, state *domain.PRFollowupState) (ThreadGitContext, error) {
	bound, err := s.capture(ctx, state.Record.ThreadID)
	if err != nil {
		return bound, err
	}
	if bound.WorkspaceID != state.Record.WorkspaceID || githubreview.Fingerprint(bound.RootPath) != state.Record.RootPathSHA256 || bound.Branch != state.Record.Branch {
		return bound, apperror.New(apperror.CodeFailedPrecondition, "任务的工作目录或分支已改变，请在原工作区核对后继续")
	}
	repository, err := githubreview.ParseRepository(state.Record.Repository)
	if err != nil {
		return bound, err
	}
	if err = requireThreadPRRepository(bound, repository); err != nil {
		return bound, err
	}
	return bound, nil
}

func (s *PRFollowupService) runFollowup(ctx context.Context, state *domain.PRFollowupState) error {
	// An admitted turn keeps its exact body and operation key across restarts.
	// New remote changes never replace an unfinished input.
	if state.Pending != nil && state.Pending.State != domain.PRFollowupTurnCompleted && state.Pending.State != domain.PRFollowupTurnFailed {
		bound, err := s.boundFollowup(ctx, state)
		if err != nil {
			return s.blockFollowup(ctx, state, err.Error())
		}
		// A crash before the submission receipt must not start stale work after
		// the PR was closed or replaced. Keep the saved input and key unchanged.
		if state.Pending.RunID == "" && state.Pending.MessageID == "" {
			snapshot, err := s.read(ctx, state.Record.ConnectionID, state.Record.PullRequest)
			if err != nil {
				return s.deferFollowup(ctx, state, "恢复前无法核对 PR，稍后重试", err)
			}
			if snapshot.State != githubreview.EvidenceVerified {
				return s.deferFollowup(ctx, state, "恢复前的 PR 资料不完整，本轮未执行修复", nil)
			}
			if snapshot.PullRequest.Repository.FullName != state.Record.Repository || snapshot.PullRequest.Number != state.Record.PullRequest {
				return s.blockFollowup(ctx, state, "GitHub 连接指向的仓库或 PR 已改变")
			}
			if snapshot.PullRequest.State != "open" || snapshot.PullRequest.Merged {
				state.Record.Status = domain.PRFollowupCompleted
				state.Record.LastResult, state.Record.LastError = "PR 已关闭或合并，持续跟进结束", ""
				return s.saveFollowup(ctx, state)
			}
			if snapshot.PullRequest.HeadSHA != state.Record.LastHeadSHA || bound.HeadSHA != snapshot.PullRequest.HeadSHA || bound.Branch != snapshot.PullRequest.HeadBranch {
				return s.blockFollowup(ctx, state, "中断后 PR 或本地提交已改变，请核对原轮次后继续")
			}
		}
		return s.continueFollowupTurn(ctx, state)
	}
	if state.Pending != nil && state.Pending.State == domain.PRFollowupTurnFailed {
		if err := s.turns.CheckAutomaticTurn(ctx, state.Record.ThreadID); err != nil {
			return s.blockFollowup(ctx, state, err.Error())
		}
	}
	snapshot, err := s.read(ctx, state.Record.ConnectionID, state.Record.PullRequest)
	if err != nil {
		return s.deferFollowup(ctx, state, "本次 GitHub 检查失败，稍后重试", err)
	}
	checkedAt := s.now()
	state.Record.LastCheckedAt = &checkedAt
	if snapshot.State != githubreview.EvidenceVerified {
		return s.deferFollowup(ctx, state, "远端资料不完整，本轮未执行修复", fmt.Errorf("%s: %s", snapshot.State, strings.Join(snapshot.Omissions, "; ")))
	}
	if snapshot.PullRequest.Repository.FullName != state.Record.Repository || snapshot.PullRequest.Number != state.Record.PullRequest {
		return s.blockFollowup(ctx, state, "GitHub 连接指向的仓库或 PR 已改变")
	}
	if snapshot.PullRequest.State != "open" || snapshot.PullRequest.Merged {
		state.Record.Status = domain.PRFollowupCompleted
		state.Record.LastResult, state.Record.LastError = "PR 已关闭或合并，持续跟进结束", ""
		return s.saveFollowup(ctx, state)
	}
	bound, err := s.boundFollowup(ctx, state)
	if err != nil {
		return s.blockFollowup(ctx, state, err.Error())
	}
	if err = requireThreadPRRepository(bound, snapshot.PullRequest.Repository); err != nil {
		return s.blockFollowup(ctx, state, err.Error())
	}
	if bound.HeadSHA != snapshot.PullRequest.HeadSHA || bound.Branch != snapshot.PullRequest.HeadBranch {
		return s.blockFollowup(ctx, state, "当前工作区提交与 PR 不一致，请核对提交、推送或外部更新后继续")
	}
	observation := observePRFollowup(snapshot, state.Observed, true)
	state.Record.LastHeadSHA = snapshot.PullRequest.HeadSHA
	if len(observation.changes) == 0 {
		state.Observed = observation.seen
		message := "没有新的 CI 失败或评论，继续等待"
		if state.Pending != nil && state.Pending.ID != state.ReviewedTurnID && !observation.pending {
			if observation.failed {
				return s.blockFollowup(ctx, state, "本轮续跑已结束，但远端仍是已处理的失败结果，请检查修复及 CI 重跑情况")
			}
			state.Record.Status = domain.PRFollowupReady
			message = "本轮续跑已结束，远端当前提交的 CI 检查通过，请复核改动"
			if !observation.hasChecks {
				message = "本轮续跑已结束，PR 未提供 CI 结果，请人工复核"
			}
		}
		return s.deferFollowup(ctx, state, message, nil)
	}
	if state.Record.TurnsStarted >= state.Record.MaxTurns {
		state.Record.Status = domain.PRFollowupExhausted
		state.Record.LastResult = "已达到自动修复次数上限，新的反馈留待人工处理"
		return s.saveFollowup(ctx, state)
	}
	live, err := s.turns.ExecutionState(ctx, state.Record.ThreadID)
	if err != nil {
		return s.deferFollowup(ctx, state, "暂时无法读取任务状态", err)
	}
	if live.State != "idle" {
		return s.deferFollowup(ctx, state, "任务正在执行，等待当前操作结束", nil)
	}
	if err = s.turns.CheckAutomaticTurn(ctx, state.Record.ThreadID); err != nil {
		return s.blockFollowup(ctx, state, err.Error())
	}
	logs := ""
	if observation.failed {
		logs, err = s.logs(ctx, state.Record.ConnectionID, snapshot)
		if err != nil {
			return s.deferFollowup(ctx, state, "无法读取本次 CI 的诊断资料，稍后重试", err)
		}
	}
	content, err := prFollowupPrompt(snapshot, observation.changes, logs)
	if err != nil {
		return s.blockFollowup(ctx, state, err.Error())
	}
	// Check again after remote I/O, before persisting the exact work to run.
	current, err := s.boundFollowup(ctx, state)
	if err != nil {
		return s.blockFollowup(ctx, state, err.Error())
	}
	if current.HeadSHA != bound.HeadSHA {
		return s.blockFollowup(ctx, state, "读取反馈期间本地提交已改变，请核对后继续")
	}
	now := s.now()
	id := idgen.New("pr-followup-turn")
	state.Pending = &domain.PRFollowupTurn{ID: id, OperationKey: id, Content: content, State: domain.PRFollowupTurnQueued, CreatedAt: now, UpdatedAt: now}
	state.Observed = observation.seen
	state.Record.TurnsStarted++
	state.Record.LastResult, state.Record.LastError = "发现新反馈，已提交本轮续跑", ""
	if err = s.saveFollowup(ctx, state); err != nil {
		return err
	}
	return s.continueFollowupTurn(ctx, state)
}

func (s *PRFollowupService) continueFollowupTurn(ctx context.Context, state *domain.PRFollowupState) error {
	pending := state.Pending
	if pending.State == domain.PRFollowupTurnWaitingApproval {
		observed, err := s.turns.ObserveAutomaticTurn(ctx, state.Record.ThreadID, pending.RunID, pending.MessageID)
		if err != nil {
			return s.deferFollowup(ctx, state, "正在核对待审批轮次的执行结果", err)
		}
		return s.finishObservedFollowup(ctx, state, observed)
	}
	live, err := s.turns.ExecutionState(ctx, state.Record.ThreadID)
	if err != nil {
		return s.deferFollowup(ctx, state, "暂时无法读取任务状态", err)
	}
	if live.State != "idle" {
		return s.deferFollowup(ctx, state, "等待任务当前执行结束", nil)
	}
	pending.State, pending.UpdatedAt = domain.PRFollowupTurnRunning, s.now()
	if err = s.saveFollowup(ctx, state); err != nil {
		return err
	}
	pending = state.Pending
	result, executeErr := s.turns.ExecuteAutomatic(ctx, ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: state.Record.ThreadID, Content: pending.Content, OperationKey: pending.OperationKey, RequestedBy: "pr-followup"})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if result.Submission.Run.ID != "" {
		pending.RunID = result.Submission.Run.ID
	}
	if result.Submission.Message.ID != "" {
		pending.MessageID = result.Submission.Message.ID
	}
	pending.UpdatedAt = s.now()
	if executeErr != nil {
		pending.State, pending.Error = domain.PRFollowupTurnUnknown, redact.String(executeErr.Error())
		var failed *ThreadTurnFailedError
		if errors.As(executeErr, &failed) {
			pending.State = domain.PRFollowupTurnFailed
		}
		return s.blockFollowup(ctx, state, executeErr.Error())
	}
	if pending.RunID == "" || pending.MessageID == "" {
		pending.State = domain.PRFollowupTurnUnknown
		return s.blockFollowup(ctx, state, "续跑未返回可核对的执行身份，请查看任务记录后继续")
	}
	observed, err := s.turns.ObserveAutomaticTurn(ctx, state.Record.ThreadID, pending.RunID, pending.MessageID)
	if err != nil {
		return s.deferFollowup(ctx, state, "续跑结果尚未确认", err)
	}
	return s.finishObservedFollowup(ctx, state, observed)
}

func (s *PRFollowupService) finishObservedFollowup(ctx context.Context, state *domain.PRFollowupState, observed AutomaticThreadTurnObservation) error {
	state.Pending.UpdatedAt = s.now()
	switch observed.State {
	case "completed":
		state.Pending.State = domain.PRFollowupTurnCompleted
		return s.deferFollowup(ctx, state, "本轮续跑已结束，等待重新检查远端 CI 和评论", nil)
	case "waiting_approval":
		state.Pending.State = domain.PRFollowupTurnWaitingApproval
		return s.deferFollowup(ctx, state, "任务正在等待你的审批", nil)
	case "running":
		state.Pending.State = domain.PRFollowupTurnWaitingApproval // observe only; the existing turn still owns execution
		return s.deferFollowup(ctx, state, "等待原任务轮次执行结束", nil)
	default:
		state.Pending.State = domain.PRFollowupTurnUnknown
		if observed.TerminalFailure {
			state.Pending.State = domain.PRFollowupTurnFailed
		}
		return s.blockFollowup(ctx, state, observed.Reason)
	}
}
