package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/dependencycache"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
)

const ProjectPreparationProtocolVersion = projectprep.ProtocolVersion

type ProjectPreparationStore interface {
	ProjectActionsStore
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	GetProjectPreparation(context.Context, string, string) (domain.ProjectPreparationOperation, error)
	LatestProjectPreparation(context.Context, string, string) (domain.ProjectPreparationOperation, bool, error)
	ActiveProjectPreparation(context.Context, string) (domain.ProjectPreparationOperation, bool, error)
	InterruptStaleProjectPreparation(context.Context, domain.ProjectPreparationOperation) (domain.ProjectPreparationOperation, error)
	BeginProjectPreparation(context.Context, domain.ProjectPreparationOperation) (domain.ProjectPreparationOperation, bool, error)
	UpdateProjectPreparation(context.Context, domain.ProjectPreparationOperation, int64) (domain.ProjectPreparationOperation, error)
}

type ProjectPreparationPlanView struct {
	Fingerprint      string             `json:"fingerprint"`
	Ecosystem        string             `json:"ecosystem"`
	Toolchain        string             `json:"toolchain"`
	RequestedVersion string             `json:"requested_version"`
	WorkspaceRoot    string             `json:"workspace_root,omitempty"`
	Files            []projectprep.File `json:"files"`
	Sources          []string           `json:"sources"`
	PackageCount     int                `json:"package_count"`
	InstallCommand   string             `json:"install_command"`
	TestCommand      string             `json:"test_command"`
	Ready            bool               `json:"ready"`
	Issues           []string           `json:"issues"`
}

type ProjectPreparationCacheView struct {
	Status         string `json:"status"`
	CachedPackages int    `json:"cached_packages"`
	TotalPackages  int    `json:"total_packages"`
}

type ProjectPreparationOperationView struct {
	ProtocolVersion   string                `json:"protocol_version"`
	RunID             string                `json:"run_id"`
	Target            string                `json:"target"`
	ID                string                `json:"id"`
	State             string                `json:"state"`
	CompletedPackages int                   `json:"completed_packages"`
	TotalPackages     int                   `json:"total_packages"`
	DownloadedBytes   int64                 `json:"downloaded_bytes"`
	CacheHits         int                   `json:"cache_hits"`
	Job               *ProjectActionJobView `json:"job,omitempty"`
	Error             string                `json:"error"`
	CanCancel         bool                  `json:"can_cancel"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
}

type ProjectPreparationView struct {
	ProtocolVersion string                           `json:"protocol_version"`
	RunID           string                           `json:"run_id"`
	Backend         string                           `json:"backend"`
	Target          string                           `json:"target"`
	Available       bool                             `json:"available"`
	Reason          string                           `json:"reason"`
	Plan            ProjectPreparationPlanView       `json:"plan"`
	Cache           ProjectPreparationCacheView      `json:"cache"`
	Latest          *ProjectPreparationOperationView `json:"latest,omitempty"`
}

type PrepareProjectRequest struct {
	RunID               string   `json:"-"`
	OperationKey        string   `json:"-"`
	RequestedBy         string   `json:"-"`
	Target              string   `json:"target"`
	ExpectedFingerprint string   `json:"expected_fingerprint"`
	AllowedSources      []string `json:"allowed_sources"`
	ConfirmExecution    bool     `json:"confirm_execution"`
	RunInstallScripts   bool     `json:"run_install_scripts"`
}

type projectPreparationBroker interface {
	Prepare(context.Context, projectprep.Plan, []string, string, func(dependencycache.Progress), func(context.Context) error) (dependencycache.Stats, error)
	CachedPackages(context.Context, projectprep.Plan) (int, error)
}

type ProjectPreparationService struct {
	store    ProjectPreparationStore
	runtime  ProjectActionRuntime
	drydocks *RunWorktreeService
	broker   projectPreparationBroker
	jobs     *ProjectActionsService
	ownerID  string
	mu       sync.Mutex
	active   map[string]context.CancelFunc
	closed   bool
	wg       sync.WaitGroup
}

func NewProjectPreparationService(store ProjectPreparationStore, runtime any, drydocks *RunWorktreeService, home string) (*ProjectPreparationService, error) {
	broker, err := dependencycache.New(filepath.Join(home, "dependency-cache"))
	if err != nil {
		return nil, err
	}
	commands, _ := runtime.(ProjectActionRuntime)
	return &ProjectPreparationService{store: store, runtime: commands, drydocks: drydocks, broker: broker, jobs: NewProjectActionsService(store, runtime, drydocks), ownerID: idgen.New("project-preparation-owner"), active: make(map[string]context.CancelFunc)}, nil
}

func (s *ProjectPreparationService) inspect(ctx context.Context, runID, target string) (ProjectPreparationView, projectprep.Plan, string, *CommandRuntimeService, error) {
	view := ProjectPreparationView{ProtocolVersion: ProjectPreparationProtocolVersion, RunID: runID, Target: target}
	if !domain.ValidAgentID(runID) {
		return view, projectprep.Plan{}, "", nil, apperror.New(apperror.CodeInvalidArgument, "task identity is invalid")
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return view, projectprep.Plan{}, "", nil, err
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return view, projectprep.Plan{}, "", nil, err
	}
	files, err := ResolveRunFileWorkspace(ctx, s.store, run, mission, s.drydocks)
	if err != nil {
		return view, projectprep.Plan{}, "", nil, err
	}
	plan, err := projectprep.Detect(ctx, files.Workspace.RootPath, target)
	if err != nil {
		return view, plan, "", nil, apperror.Wrap(apperror.CodeInvalidArgument, "inspect project preparation", err)
	}
	policy, err := projectconfig.PathPolicy(run.Config)
	if err != nil {
		return view, plan, "", nil, err
	}
	for _, directory := range []string{plan.Target, plan.WorkspaceRoot} {
		if directory != "" {
			if err := policy.Check(directory); err != nil {
				return view, plan, "", nil, err
			}
		}
	}
	for _, file := range append([]projectprep.File{plan.Manifest}, plan.Lockfiles...) {
		if file.Path != "" {
			if err := policy.Check(file.Path); err != nil {
				return view, plan, "", nil, err
			}
		}
	}
	view.Target = plan.Target
	view.Plan = ProjectPreparationPlanView{Fingerprint: plan.Fingerprint, Ecosystem: plan.Ecosystem, Toolchain: plan.Toolchain, RequestedVersion: plan.RequestedVersion, Files: append([]projectprep.File{}, plan.Lockfiles...), Sources: append([]string{}, plan.Sources...), PackageCount: len(plan.Packages), InstallCommand: plan.Install.Display, TestCommand: plan.Test.Display, Ready: plan.Ready, Issues: append([]string{}, plan.Issues...)}
	view.Plan.WorkspaceRoot = plan.WorkspaceRoot
	if plan.Manifest.Path != "" {
		view.Plan.Files = append([]projectprep.File{plan.Manifest}, view.Plan.Files...)
	}
	view.Cache = ProjectPreparationCacheView{Status: "empty", TotalPackages: len(plan.Packages)}
	if plan.Ready {
		cached, cacheErr := s.broker.CachedPackages(ctx, plan)
		if cacheErr != nil {
			return view, plan, "", nil, cacheErr
		}
		view.Cache.CachedPackages = cached
		if cached == len(plan.Packages) {
			view.Cache.Status = "ready"
		} else if cached > 0 {
			view.Cache.Status = "partial"
		}
	}
	var service *CommandRuntimeService
	if s.runtime != nil {
		service, err = s.runtime.projectCommandService(ctx, runID)
	} else {
		err = errors.New("开始编码以准备所选沙箱")
	}
	if plan.Ready && plan.Ecosystem == "bun" && service == nil && err != nil {
		err = fmt.Errorf("%s; %w", bunRuntimeReason, err)
	}
	if err == nil && service.adapter.Kind != commandruntimeadapter.KindSandboxedWorkspace {
		err = errors.New("请为任务选择 Local、Docker Engine 或 Docker Sandboxes")
	}
	if service != nil {
		view.Backend = service.adapter.Backend
	}
	project, projectErr := projectconfig.Pinned(run.Config)
	if projectErr != nil {
		return view, plan, "", nil, projectErr
	}
	if project != nil && project.ReadOnly {
		err = errors.New("项目为只读；请在允许修改的任务中准备依赖")
	}
	if err == nil && plan.Ready {
		_, err = PrepareProjectPreparationCommand(plan, service.adapter, files.Workspace.RootPath, false)
		if err == nil {
			err = service.projectPreparationReady(ctx, plan.Ecosystem)
		}
	}
	view.Available = err == nil && plan.Ready
	if err != nil {
		view.Reason = redact.String(err.Error())
	}
	if !plan.Ready && view.Reason == "" {
		view.Reason = strings.Join(plan.Issues, "; ")
	}
	if latest, found, readErr := s.store.LatestProjectPreparation(ctx, runID, plan.Target); readErr != nil {
		return view, plan, "", nil, readErr
	} else if found {
		v := s.operationView(ctx, latest)
		view.Latest = &v
	}
	return view, plan, files.Workspace.RootPath, service, nil
}

func (s *ProjectPreparationService) Inspect(ctx context.Context, runID, target string) (ProjectPreparationView, error) {
	view, _, _, _, err := s.inspect(ctx, runID, target)
	return view, err
}

func preparationRequestFingerprint(r PrepareProjectRequest) string {
	body, _ := json.Marshal(r)
	return runmutation.Fingerprint("project_preparation_request.v1", r.RunID, r.RequestedBy, string(body))
}

func (s *ProjectPreparationService) Prepare(ctx context.Context, r PrepareProjectRequest) (ProjectPreparationView, error) {
	var empty ProjectPreparationView
	if !domain.ValidAgentID(r.RunID) || !domain.ValidAgentID(r.RequestedBy) || !r.ConfirmExecution || len(r.ExpectedFingerprint) != 64 || r.AllowedSources == nil {
		return empty, apperror.New(apperror.CodeInvalidArgument, "confirm an inspected project preparation")
	}
	key, err := domain.NormalizeAgentOperationKey(r.OperationKey)
	if err != nil || key != r.OperationKey {
		return empty, apperror.New(apperror.CodeInvalidArgument, "preparation request key is invalid")
	}
	keyDigest := runmutation.Fingerprint("project_preparation_key.v1", r.RunID, key)
	opID := "prep-" + keyDigest
	fingerprint := preparationRequestFingerprint(r)
	if saved, err := s.store.GetProjectPreparation(ctx, r.RunID, opID); err == nil {
		if saved.RequestFingerprint != fingerprint {
			return empty, apperror.New(apperror.CodeConflict, "preparation request key belongs to a different intent")
		}
		// Replay reads the receipt even when the project or adapter has changed.
		view, _, _, _, readErr := s.inspect(ctx, r.RunID, r.Target)
		if readErr != nil {
			view = ProjectPreparationView{ProtocolVersion: ProjectPreparationProtocolVersion, RunID: r.RunID, Target: saved.Target, Backend: saved.Backend, Reason: "读取已保存的准备结果", Plan: ProjectPreparationPlanView{Fingerprint: saved.PlanFingerprint, Files: []projectprep.File{}, Sources: []string{}, Issues: []string{}, PackageCount: saved.TotalPackages}, Cache: ProjectPreparationCacheView{Status: "empty", TotalPackages: saved.TotalPackages}}
		}
		v := s.operationView(ctx, saved)
		view.Latest = &v
		return view, nil
	} else if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
		return empty, err
	}
	view, plan, root, service, err := s.inspect(ctx, r.RunID, r.Target)
	if err != nil {
		return view, err
	}
	if !view.Available {
		return view, apperror.New(apperror.CodeFailedPrecondition, view.Reason)
	}
	if r.RunInstallScripts && plan.Ecosystem != "npm" && plan.Ecosystem != "pnpm" {
		return view, apperror.New(apperror.CodeInvalidArgument, "installation scripts require npm or pnpm preparation")
	}
	if r.Target != plan.Target || r.ExpectedFingerprint != plan.Fingerprint {
		return view, apperror.New(apperror.CodeConflict, "项目文件已变化，请重新检测并确认依赖源")
	}
	if !slices.IsSorted(r.AllowedSources) || len(slices.Compact(slices.Clone(r.AllowedSources))) != len(r.AllowedSources) {
		return view, apperror.New(apperror.CodeInvalidArgument, "package source selection must be unique and sorted")
	}
	for _, source := range r.AllowedSources {
		if !slices.Contains(plan.Sources, source) {
			return view, apperror.New(apperror.CodePolicyDenied, "依赖源未包含在已检测的锁文件中")
		}
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, r.RunID)
	if err != nil {
		return view, err
	}
	// Admission is exclusive across the Run, including other project targets.
	if active, found, err := s.store.ActiveProjectPreparation(ctx, r.RunID); err != nil {
		return view, err
	} else if found {
		if _, err := s.interruptStale(ctx, r.RunID, active.ID); err != nil {
			return view, err
		}
	}
	requestJSON, _ := json.Marshal(r)
	now := time.Now().UTC()
	o := domain.ProjectPreparationOperation{ID: opID, RunID: r.RunID, KeyDigest: keyDigest, RequestFingerprint: fingerprint, Target: plan.Target, PlanFingerprint: plan.Fingerprint, Backend: service.adapter.Backend, RequestedBy: r.RequestedBy, RequestJSON: string(requestJSON), OwnerID: s.ownerID, PermissionRevision: permission.Revision, State: "pending", TotalPackages: len(plan.Packages), Revision: 1, CreatedAt: now, UpdatedAt: now}
	if plan.WorkspaceRoot != "" {
		o.WorkspaceRoot = plan.WorkspaceRoot
	}
	if plan.Ecosystem == "pnpm" {
		o.SeedDirectory = projectprep.PNPMSeedDirectory(plan.Target)
	}
	// Close and admission serialize the WaitGroup addition. Once Close has
	// started, a new durable operation cannot be left without an owner.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return view, apperror.New(apperror.CodeFailedPrecondition, "project preparation service is closed")
	}
	o, replayed, err := s.store.BeginProjectPreparation(ctx, o)
	if err != nil {
		s.mu.Unlock()
		return view, err
	}
	if !replayed {
		owned, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		s.active[o.ID] = cancel
		s.wg.Add(1)
		s.mu.Unlock()
		go s.execute(owned, cancel, o, r, plan, root, service)
	} else {
		s.mu.Unlock()
	}
	v := s.operationView(ctx, o)
	view.Latest = &v
	return view, nil
}

func (s *ProjectPreparationService) change(ctx context.Context, runID, id string, fn func(*domain.ProjectPreparationOperation)) (domain.ProjectPreparationOperation, error) {
	for attempt := 0; attempt < 5; attempt++ {
		o, err := s.store.GetProjectPreparation(ctx, runID, id)
		if err != nil {
			return o, err
		}
		if o.Terminal() {
			return o, nil
		}
		revision := o.Revision
		previousUpdated := o.UpdatedAt
		fn(&o)
		o.Revision++
		o.UpdatedAt = time.Now().UTC()
		if o.UpdatedAt.Before(previousUpdated) {
			o.UpdatedAt = previousUpdated
		}
		if o.UpdatedAt.Before(o.CreatedAt) {
			o.UpdatedAt = o.CreatedAt
		}
		o, err = s.store.UpdateProjectPreparation(ctx, o, revision)
		if apperror.CodeOf(err) == apperror.CodeConflict {
			continue
		}
		return o, err
	}
	return domain.ProjectPreparationOperation{}, apperror.New(apperror.CodeConflict, "preparation progress changed repeatedly")
}

func (s *ProjectPreparationService) execute(ctx context.Context, cancel context.CancelFunc, o domain.ProjectPreparationOperation, r PrepareProjectRequest, plan projectprep.Plan, root string, service *CommandRuntimeService) {
	defer s.wg.Done()
	defer cancel()
	defer func() { s.mu.Lock(); delete(s.active, o.ID); s.mu.Unlock() }()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := s.store.GetProjectPreparation(ctx, o.RunID, o.ID)
				permission, pErr := s.store.GetRunExecutionPermission(ctx, o.RunID)
				if err != nil || pErr != nil || current.CancelRequested || permission.Revision != o.PermissionRevision {
					cancel()
					return
				}
			}
		}
	}()
	check := func(c context.Context) error {
		if err := c.Err(); err != nil {
			return err
		}
		current, err := s.store.GetProjectPreparation(c, o.RunID, o.ID)
		if err != nil {
			return err
		}
		if current.CancelRequested {
			return context.Canceled
		}
		if current.Terminal() || current.OwnerID != s.ownerID {
			return apperror.New(apperror.CodeFailedPrecondition, "project preparation no longer owns this operation")
		}
		permission, err := s.store.GetRunExecutionPermission(c, o.RunID)
		if err != nil {
			return err
		}
		if permission.Revision != o.PermissionRevision || !service.commandRuntimeAdapterCurrent() {
			return apperror.New(apperror.CodePolicyDenied, "任务的执行授权已变化，请重新准备")
		}
		return nil
	}
	checkPlan := func(c context.Context, currentRoot string) error {
		if err := check(c); err != nil {
			return err
		}
		if err := service.projectPreparationReady(c, plan.Ecosystem); err != nil {
			return err
		}
		return recheckPreparationPlan(c, currentRoot, root, plan)
	}
	before := func(c context.Context, currentRoot string) error {
		if err := checkPlan(c, currentRoot); err != nil {
			return err
		}
		if _, err := s.change(c, o.RunID, o.ID, func(v *domain.ProjectPreparationOperation) { v.State = "downloading" }); err != nil {
			return err
		}
		var progressErr error
		stats, err := s.broker.Prepare(c, plan, r.AllowedSources, currentRoot, func(p dependencycache.Progress) {
			if progressErr != nil {
				return
			}
			_, progressErr = s.change(c, o.RunID, o.ID, func(v *domain.ProjectPreparationOperation) {
				v.CompletedPackages = p.Completed
				if p.FromCache {
					v.CacheHits++
				}
				v.DownloadedBytes = p.Bytes
			})
		}, func(c context.Context) error {
			if progressErr != nil {
				return progressErr
			}
			return check(c)
		})
		if err != nil {
			return err
		}
		if progressErr != nil {
			return progressErr
		}
		current, err := projectprep.Detect(c, currentRoot, r.Target)
		if err != nil {
			return err
		}
		if current.Fingerprint != plan.Fingerprint {
			return apperror.New(apperror.CodeConflict, "项目锁文件在下载期间变化，请重新检测")
		}
		_, err = s.change(c, o.RunID, o.ID, func(v *domain.ProjectPreparationOperation) {
			v.State = "installing"
			v.CompletedPackages = len(plan.Packages)
			v.CacheHits = stats.CachedPackages
			v.DownloadedBytes = stats.Bytes
		})
		return err
	}
	after := func(c context.Context, currentRoot string) error {
		if err := check(c); err != nil {
			return err
		}
		if plan.Ecosystem == "npm" {
			if err := recheckPreparationPlan(c, currentRoot, root, plan); err != nil {
				return err
			}
			return dependencycache.FinalizeNPMWorkspace(c, currentRoot, plan, service.adapter.Backend != CommandRuntimeLocalSandboxBackend)
		}
		if plan.Ecosystem == "pnpm" || plan.Ecosystem == "yarn" || plan.Ecosystem == "bun" || plan.Ecosystem == "rust" {
			return recheckPreparationPlan(c, currentRoot, root, plan)
		}
		return nil
	}
	spec, err := PrepareProjectPreparationCommand(plan, service.adapter, root, false)
	var result OperatorCommandResult
	var fetchJobID, installJobID, scriptJobID string
	var jobPersistenceError error
	persistJob := func(v OperatorCommandResult, phase string) {
		switch phase {
		case "fetch":
			fetchJobID = v.Job.ID
		case "scripts":
			scriptJobID = v.Job.ID
		default:
			installJobID = v.Job.ID
		}
		writeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		_, writeErr := s.change(writeCtx, o.RunID, o.ID, func(o *domain.ProjectPreparationOperation) {
			switch phase {
			case "fetch":
				o.FetchJobID = v.Job.ID
			case "scripts":
				o.ScriptJobID = v.Job.ID
			default:
				o.JobID = v.Job.ID
			}
		})
		if writeErr != nil {
			jobPersistenceError = errors.Join(jobPersistenceError, writeErr)
			cancel()
		}
	}
	if err == nil && plan.Ecosystem == "pnpm" {
		// Populate the native store inside the same sandbox that installs the
		// workspace. The broker supplies only verified, locally staged tarballs.
		var fetchSpec runner.CommandRuntimeSpec
		fetchSpec, err = PrepareProjectPreparationFetchCommand(plan, service.adapter, root)
		if err == nil {
			fetchSpec.Purpose = "Populate pnpm dependency store " + o.RequestFingerprint
			result, err = service.runPreparedOperatorCommand(ctx, OperatorCommandRequest{RunID: o.RunID, OperationKey: "prep-fetch-" + o.KeyDigest, RequestedBy: r.RequestedBy, ConfirmExecution: true, Command: fetchSpec}, func(v OperatorCommandResult) {
				persistJob(v, "fetch")
			}, before, nil)
			err = errors.Join(err, jobPersistenceError)
			if err == nil && !preparationCommandSucceeded(result.Job) {
				err = errors.New("pnpm 缓存准备失败；请查看命令输出")
			}
		}
		// Installation has its own lease and must recheck the reviewed project
		// and authority; it must not download or replay progress a second time.
		before = checkPlan
	}
	if err == nil {
		spec.Purpose = "Prepare project dependencies " + o.RequestFingerprint
		result, err = service.runPreparedOperatorCommand(ctx, OperatorCommandRequest{RunID: o.RunID, OperationKey: "prep-install-" + o.KeyDigest, RequestedBy: r.RequestedBy, ConfirmExecution: true, Command: spec}, func(v OperatorCommandResult) {
			persistJob(v, "install")
		}, before, after)
		if result.Job.ID != "" {
			installJobID = result.Job.ID
		}
		err = errors.Join(err, jobPersistenceError)
		if err == nil && !preparationCommandSucceeded(result.Job) {
			err = fmt.Errorf("依赖安装失败；请查看安装输出")
		}
	}
	if err == nil && r.RunInstallScripts && (plan.Ecosystem == "npm" || plan.Ecosystem == "pnpm") {
		spec, err = PrepareProjectPreparationRebuildCommand(plan, service.adapter, root)
		if err == nil {
			spec.Purpose = "Run dependency installation scripts " + o.RequestFingerprint
			result, err = service.runPreparedOperatorCommand(ctx, OperatorCommandRequest{RunID: o.RunID, OperationKey: "prep-scripts-" + o.KeyDigest, RequestedBy: r.RequestedBy, ConfirmExecution: true, Command: spec}, func(v OperatorCommandResult) {
				persistJob(v, "scripts")
			}, func(c context.Context, currentRoot string) error {
				if err := check(c); err != nil {
					return err
				}
				return recheckPreparationPlan(c, currentRoot, root, plan)
			}, nil)
			if result.Job.ID != "" {
				scriptJobID = result.Job.ID
			}
			err = errors.Join(err, jobPersistenceError)
			if err == nil && !preparationCommandSucceeded(result.Job) {
				err = errors.New("依赖安装脚本失败；请查看脚本输出")
			}
		}
	}
	finalCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	if err == nil {
		err = ctx.Err()
	}
	_, _ = s.finishPreparation(finalCtx, o.RunID, o.ID, root, fetchJobID, installJobID, scriptJobID, err)
}

// Each command lease, including the separately authorized rebuild, binds to
// the same reviewed project. Another operation may edit files between leases.
func recheckPreparationPlan(ctx context.Context, currentRoot, reviewedRoot string, plan projectprep.Plan) error {
	if currentRoot != reviewedRoot {
		return apperror.New(apperror.CodeConflict, "任务工作目录已变化")
	}
	current, err := projectprep.Detect(ctx, currentRoot, plan.Target)
	if err != nil {
		return err
	}
	if current.Fingerprint != plan.Fingerprint {
		return apperror.New(apperror.CodeConflict, "项目锁文件已变化，请重新检测")
	}
	return nil
}

func preparationCommandSucceeded(job runner.CommandRuntimeJob) bool {
	return job.State == runner.CommandRuntimeJobCompleted && job.ExitCode != nil && *job.ExitCode == 0 && job.TreeReaped
}

func (s *ProjectPreparationService) finishPreparation(ctx context.Context, runID, id, root, fetchJobID, installJobID, scriptJobID string, executionError error) (domain.ProjectPreparationOperation, error) {
	return s.change(ctx, runID, id, func(v *domain.ProjectPreparationOperation) {
		if v.FetchJobID == "" && fetchJobID != "" {
			v.FetchJobID = fetchJobID
		}
		if v.JobID == "" && installJobID != "" {
			v.JobID = installJobID
		}
		if v.ScriptJobID == "" && scriptJobID != "" {
			v.ScriptJobID = scriptJobID
		}
		if v.CancelRequested {
			v.State, v.Error = "cancelled", preparationError(context.Canceled, root)
			return
		}
		if executionError == nil {
			v.State = "ready"
			return
		}
		v.State = "failed"
		if errors.Is(executionError, context.Canceled) || errors.Is(executionError, context.DeadlineExceeded) {
			v.State = "interrupted"
		}
		v.Error = preparationError(executionError, root)
	})
}

func preparationError(err error, root string) string {
	if err == nil {
		return ""
	}
	message := redact.String(err.Error())
	if root != "" {
		message = strings.ReplaceAll(message, root, "[workspace]")
	}
	if len(message) > 4096 {
		message = strings.ToValidUTF8(message[:4096], "")
	}
	return message
}

func (s *ProjectPreparationService) operationStale(ctx context.Context, o domain.ProjectPreparationOperation) bool {
	if o.Terminal() || time.Since(o.UpdatedAt) < 30*time.Second {
		return false
	}
	s.mu.Lock()
	_, active := s.active[o.ID]
	s.mu.Unlock()
	if active {
		return false
	}
	lease, found, err := s.store.GetRunExecutionLease(ctx, o.RunID)
	return err == nil && (!found || !lease.ActiveAt(time.Now().UTC()))
}

func (s *ProjectPreparationService) interruptStale(ctx context.Context, runID, id string) (domain.ProjectPreparationOperation, error) {
	// Admission adds its local owner under this lock. Hold it through recovery
	// so a just-admitted worker cannot be mistaken for an abandoned operation.
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.store.GetProjectPreparation(ctx, runID, id)
	if err != nil {
		return o, err
	}
	if _, active := s.active[o.ID]; active || o.Terminal() {
		return o, nil
	}
	// The store rechecks the observed revision, age and Run lease atomically.
	return s.store.InterruptStaleProjectPreparation(ctx, o)
}

func (s *ProjectPreparationService) operationView(ctx context.Context, o domain.ProjectPreparationOperation) ProjectPreparationOperationView {
	v := ProjectPreparationOperationView{ProtocolVersion: ProjectPreparationProtocolVersion, RunID: o.RunID, Target: o.Target, ID: o.ID, State: o.State, CompletedPackages: o.CompletedPackages, TotalPackages: o.TotalPackages, DownloadedBytes: o.DownloadedBytes, CacheHits: o.CacheHits, Error: o.Error, CanCancel: !o.Terminal() && !o.CancelRequested, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
	if s.operationStale(ctx, o) {
		v.State = "interrupted"
		v.CanCancel = false
		v.Error = "上次准备已中断；重新准备将复用已校验的下载"
	}
	jobID := o.JobID
	if jobID == "" {
		jobID = o.FetchJobID
	}
	if o.ScriptJobID != "" {
		jobID = o.ScriptJobID
	}
	if jobID != "" {
		if job, err := s.store.GetCommandRuntimeJob(ctx, jobID); err == nil && job.RunID == o.RunID {
			projection := s.jobs.projectResult(ctx, "prepare", "", job, true)
			v.Job = &projection.Job
		}
	}
	return v
}

func (s *ProjectPreparationService) GetOperation(ctx context.Context, runID, id string) (ProjectPreparationOperationView, error) {
	o, err := s.store.GetProjectPreparation(ctx, runID, id)
	if err != nil {
		return ProjectPreparationOperationView{}, err
	}
	return s.operationView(ctx, o), nil
}

func (s *ProjectPreparationService) Cancel(ctx context.Context, runID, id string) (ProjectPreparationOperationView, error) {
	if _, err := s.interruptStale(ctx, runID, id); err != nil {
		return ProjectPreparationOperationView{}, err
	}
	o, err := s.change(ctx, runID, id, func(v *domain.ProjectPreparationOperation) { v.CancelRequested = true })
	if err != nil {
		return ProjectPreparationOperationView{}, err
	}
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.operationView(ctx, o), nil
}

// Close stops this instance's preparations. Job shutdown continues through the
// Command Runtime owner; durable interrupted receipts remain inspectable.
func (s *ProjectPreparationService) Close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
