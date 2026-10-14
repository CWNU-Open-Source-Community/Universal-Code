package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/agent"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/buildinfo"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/pricing"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/webevidence"
	"cyberagent-workbench/internal/workspace"
)

var Version = buildinfo.Version

const Name = buildinfo.ProductName

const defaultDeepSeekModel = modelregistry.DefaultDeepSeekModel

const codeIntelConfigEnvironment = "CYBERAGENT_CODE_INTEL_CONFIG"

const webSearchEndpointEnvironment = "CYBERAGENT_WEB_SEARCH_ENDPOINT"

type App struct {
	home                  string
	out                   io.Writer
	errOut                io.Writer
	store                 *store.SQLiteStore
	router                *llm.Router
	models                *modelregistry.Registry
	credentials           credential.Store
	checker               policy.Checker
	calls                 *application.ActiveCallRegistry
	dockerObserver        sandbox.DockerProductionObserver
	dockerWriteTransport  sandbox.DockerContainerWriteTransport
	dockerReadinessProbe  sandbox.ReadinessProbe
	dockerLifecycle       sandbox.DockerContainerLifecycleTransport
	dockerIO              sandbox.DockerContainerOwnedIOTransport
	dockerSandbox         *application.DockerSandboxService
	hostInputStager       sandbox.DockerHostInputStager
	hostInputHandoff      sandbox.DockerHostInputHandoffTransport
	runtimeInputApply     sandbox.DockerRuntimeInputApplicationTransport
	runtimeResourceRead   sandbox.DockerRuntimeInputResourceInspector
	runtimeResourceClean  sandbox.DockerRuntimeInputResourceCleanupTransport
	productionEvidence    sandbox.DockerProductionEvidenceCollector
	codeIntel             *codeintel.Manager
	codeIntelConfigPath   string
	codeIntelConfigDigest string
	codeIntelConfigLoaded bool
}

func Execute(args []string, out io.Writer, errOut io.Writer) int {
	return ExecuteContext(context.Background(), args, out, errOut)
}

func ExecuteContext(ctx context.Context, args []string, out io.Writer, errOut io.Writer) int {
	return executeContextWithConfig(ctx, args, out, errOut, nil)
}

func executeContextWithConfig(ctx context.Context, args []string, out io.Writer, errOut io.Writer,
	configure func(*App),
) int {
	if ctx == nil {
		ctx = context.Background()
	}
	credentials := credential.NewSystemStore()
	models := modelregistry.NewFromEnvironment()
	if credentials.Available() {
		var err error
		models, err = modelregistry.NewFromEnvironmentWithCredentials(credentials)
		if err != nil {
			classified := apperror.Normalize(err)
			fmt.Fprintln(errOut, "error:", classified)
			return apperror.ExitCode(classified)
		}
	}
	app := &App{
		home:                DefaultHome(),
		out:                 out,
		errOut:              errOut,
		router:              models.Router(),
		models:              models,
		credentials:         credentials,
		checker:             policy.NewDefaultChecker(),
		calls:               application.NewActiveCallRegistry(),
		codeIntelConfigPath: strings.TrimSpace(os.Getenv(codeIntelConfigEnvironment)),
	}
	if configure != nil {
		configure(app)
	}
	defer app.Close()

	if err := app.dispatch(ctx, args); err != nil {
		classified := apperror.Normalize(err)
		fmt.Fprintln(errOut, "error:", classified)
		return apperror.ExitCode(classified)
	}
	return 0
}

func (a *App) newAgentRunner() *application.AgentRunner {
	return application.NewAgentRunnerWithRuntime(a.store, a.router, a.checker,
		a.runRuntimeDependencies())
}

func (a *App) newToolGateway() *toolgateway.Gateway {
	gateway := toolgateway.New(a.store, a.checker).
		WithStructuredMemoryExecutor(application.NewStructuredMemoryToolExecutor(a.store)).
		WithSpecialistDelegationExecutor(application.NewSpecialistDelegationToolExecutor(a.store)).
		WithChildTaskProposalExecutor(application.NewChildTaskToolExecutor(a.store)).
		WithSkillCandidateExecutor(application.NewSkillCandidateToolExecutor(a.store)).
		WithPlanDeliveryExecutor(application.NewPlanDeliveryToolExecutor(a.store)).
		WithWorkspaceRootResolver(func(ctx context.Context, workspaceID string) (string, error) {
			rec, err := a.store.GetWorkspaceByID(ctx, workspaceID)
			return rec.RootPath, err
		})
	gateway.WithAgentCodeExecutor(application.NewAgentCodeToolExecutor(a.store, a.checker)).
		WithAgentCodeWorkspaceResolver(application.NewAgentCodeWorkspaceResolver(a.store, nil))
	if a.codeIntel != nil {
		gateway.WithCodeIntelExecutor(application.NewCodeIntelToolExecutor(
			a.store, a.checker, a.codeIntel))
	}
	if executor := a.newDockerSandboxProposalExecutor(); executor != nil {
		gateway.WithDockerSandboxProposalExecutor(executor)
	}
	if client := a.newMCPClientManager(); client != nil {
		if executor, err := application.NewMCPClientToolExecutor(client, a.store,
			domain.ExecutionPermissionRuntimeCapabilities{}); err == nil {
			gateway.WithMCPExecutor(executor)
		}
	}
	if engine := a.newLifecycleHookEngine(); engine != nil {
		gateway.WithLifecycleHooks(engine)
	}
	if service := a.newWebEvidenceService(); service != nil {
		if executor, err := application.NewWebEvidenceToolExecutor(a.store, service); err == nil {
			gateway.WithWebEvidenceExecutor(executor)
		}
	}
	return gateway
}

func (a *App) newWebEvidenceService() *webevidence.Service {
	if a == nil || a.store == nil || a.models == nil || a.credentials == nil {
		return nil
	}
	client := webevidence.NewSafeHTTPClient()
	providerSearchClient := webevidence.NewProviderSearchHTTPClient()
	var provider webevidence.SearchProvider
	if endpoint := strings.TrimSpace(os.Getenv(webSearchEndpointEnvironment)); endpoint != "" {
		provider, _ = webevidence.NewSearXNGProvider(client, endpoint)
	}
	service := webevidence.NewService(a.store, provider, webevidence.NewFetcher(client)).
		WithSourceConnectors(webevidence.NewDefaultSourceConnectors(client)...)
	resolver, err := application.NewProviderSearchResolver(a.models, a.store,
		a.credentials, provider, providerSearchClient)
	if err != nil {
		return service
	}
	return service.WithSearchProviderResolver(resolver)
}

func (a *App) newMCPClientManager() *mcp.Manager {
	if a == nil || a.store == nil {
		return nil
	}
	nativeSources, err := application.NewNativeMCPSourceResolver(a.store, filepath.Join(a.home, "plugins", "mcp"))
	if err != nil {
		return nil
	}
	manager, err := mcp.NewClientManager(a.store, a.credentials, mcp.ManagerOptions{InputBroker: mcp.NewInputBroker(), NativeSources: nativeSources})
	if err != nil {
		return nil
	}
	return manager
}

func (a *App) newLifecycleHookEngine() *hooks.Engine {
	if a == nil || a.store == nil {
		return nil
	}
	service, err := plugins.NewService(a.store)
	if err != nil {
		return nil
	}
	return hooks.NewEngine(a.store).WithLoader(service.ActiveHooks)
}

func (a *App) newDockerSandboxProposalExecutor() *application.DockerSandboxProposalExecutor {
	if a == nil || a.dockerSandbox == nil {
		return nil
	}
	executor, err := application.NewDockerSandboxProposalExecutor(a.dockerSandbox)
	if err != nil {
		return nil
	}
	return executor
}

func DefaultHome() string {
	if home := strings.TrimSpace(os.Getenv("CYBERAGENT_HOME")); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ".cyberagent-workbench"
	}
	return filepath.Join(userHome, ".cyberagent-workbench")
}

func (a *App) Close() {
	if a.codeIntel != nil {
		ctx, cancel := context.WithTimeout(context.Background(),
			codeintel.MaximumShutdownGracePeriod)
		_ = a.codeIntel.Close(ctx)
		cancel()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
}

func (a *App) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		a.printHelp()
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		a.printHelp()
		return nil
	case "version":
		fmt.Fprintf(a.out, "%s cyberagent %s\n", Name, Version)
		return nil
	case "doctor":
		return a.doctorCommand(ctx, args[1:])
	case "debug":
		return a.debugCommand(ctx, args[1:])
	case "workspace":
		return a.workspaceCommand(ctx, args[1:])
	case "script":
		return a.scriptCommand(ctx, args[1:])
	case "ctf":
		return a.ctfCommand(ctx, args[1:])
	case "learn":
		return a.learnCommand(ctx, args[1:])
	case "provider":
		return a.providerCommand(ctx, args[1:])
	case "model":
		return a.modelCommand(ctx, args[1:])
	case "skill":
		return a.skillCommand(ctx, args[1:])
	case "context":
		return a.contextCommand(ctx, args[1:])
	case "thread":
		return a.threadCommand(ctx, args[1:])
	case "session":
		return a.sessionCommand(ctx, args[1:])
	case "tool":
		return a.toolCommand(ctx, args[1:])
	case "edit":
		return a.editCommand(ctx, args[1:])
	case "approval":
		return a.approvalCommand(ctx, args[1:])
	case "sandbox":
		return a.sandboxCommand(ctx, args[1:])
	case "artifact":
		return a.artifactCommand(ctx, args[1:])
	case "ui-evidence":
		return a.uiEvidenceCommand(ctx, args[1:])
	case "web-evidence":
		return a.webEvidenceCommand(ctx, args[1:])
	case "analyzer":
		return a.analyzerCommand(ctx, args[1:])
	case "report":
		return a.reportCommand(ctx, args[1:])
	case "api":
		return a.apiCommand(ctx, args[1:])
	case "mcp":
		return a.mcpCommand(ctx, args[1:])
	case "plugin":
		return a.pluginCommand(ctx, args[1:])
	case "code-intel":
		return a.codeIntelCommand(ctx, args[1:])
	case "project-config":
		return a.projectConfigCommand(ctx, args[1:])
	case "once-command":
		return a.onceCommandCommand(ctx, args[1:])
	case "git-op":
		return a.gitOpCommand(ctx, args[1:])
	case "git-remote":
		return a.gitRemoteCommand(ctx, args[1:])
	case "git-advanced":
		return a.gitAdvancedCommand(ctx, args[1:])
	case "drydock":
		return a.drydockCommand(ctx, args[1:])
	case "github-review":
		return a.githubReviewCommand(ctx, args[1:])
	case "headless":
		return a.headlessCommand(ctx, args[1:])
	case "run":
		return a.runCommand(ctx, args[1:])
	case "todo":
		return a.todoCommand(ctx, args[1:])
	case "note":
		return a.noteCommand(ctx, args[1:])
	case "tui":
		return a.tuiCommand(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *App) printHelp() {
	fmt.Fprintln(a.out, Name)
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "Product vocabulary / 产品词汇:")
	fmt.Fprintln(a.out, "  Thread / 任务: stable user task, URL, succession, and history identity / 稳定的用户任务、URL、后继运行与历史身份")
	fmt.Fprintln(a.out, "  Run / 执行尝试: one finite execution attempt / 一次有限执行尝试")
	fmt.Fprintln(a.out, "  Step / 步骤; Tool Item / 工具项: narrative grouping and one structured tool lifecycle / 叙事步骤分组与单个结构化工具生命周期")
	fmt.Fprintln(a.out, "  Workspace / 工作区: operator-selected source scope, not a sandbox / 操作者选择的源码范围，不等于沙箱")
	fmt.Fprintln(a.out, "  Plan item / 计划项: user label for the compatibility WorkItem/work_item identity / 兼容 WorkItem/work_item identity 的用户标签")
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "Advanced diagnostics / 高级诊断:")
	fmt.Fprintln(a.out, "  Mission: immutable Thread intent, profile, Workspace, and Scope / Thread 的不可变意图、档位、Workspace 与 Scope")
	fmt.Fprintln(a.out, "  Session: Run-local context and authority boundary; never merged across Runs / Run 独占的上下文与授权边界，不跨 Run 合并")
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "Compatibility identifiers / 兼容标识:")
	fmt.Fprintln(a.out, "  cyberagent, cyberagent-workbench, CYBERAGENT_*, and .prayu/... remain stable compatibility identifiers / 保持为稳定兼容标识。")
	fmt.Fprintln(a.out, "  /api/v1/runs, /api/v1/sessions, work_item, OpenAPI, event, and SQLite identities are unchanged / 身份不变。")
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "Usage:")
	fmt.Fprintln(a.out, "  cyberagent version")
	fmt.Fprintln(a.out, "  cyberagent doctor portable [--json]")
	fmt.Fprintln(a.out, "  cyberagent doctor snapshot [--run <run-id>] [--json]")
	fmt.Fprintln(a.out, "  cyberagent doctor bundle --run <run-id> [bounded debug filters]")
	fmt.Fprintln(a.out, "  cyberagent debug query --run <run-id> [bounded filters] [--json]")
	fmt.Fprintln(a.out, "  cyberagent doctor browser-readiness --product <edge|chrome|chromium> [--json]")
	fmt.Fprintln(a.out, "  cyberagent workspace init|list|show|tree|read|checkpoint")
	fmt.Fprintln(a.out, "  cyberagent script new|run  (new writes a local template; use thread create/run execute for agent work)")
	fmt.Fprintln(a.out, "  cyberagent ctf init|analyze|writeup  (analyze writes a worksheet; it does not perform analysis)")
	fmt.Fprintln(a.out, "  cyberagent learn ask")
	fmt.Fprintln(a.out, "  cyberagent provider list|test|qualify")
	fmt.Fprintln(a.out, "  cyberagent model list|set")
	fmt.Fprintln(a.out, "  cyberagent skill list|show|validate|package|import|installed|remove|candidate|candidates|select|selection|select-external|external-selection")
	fmt.Fprintln(a.out, "  cyberagent context compact|show|instructions|memory")
	fmt.Fprintln(a.out, "  cyberagent context memory create|list|show|edit|enable|disable|delete|export")
	fmt.Fprintln(a.out, "  cyberagent thread create|list|show|send|history|archive|restore|delete|export")
	fmt.Fprintln(a.out, "  cyberagent session create|list|send|history|tree|checkpoint|fork|resume")
	fmt.Fprintln(a.out, "  cyberagent tool schema|invoke|list|show|approve|deny")
	fmt.Fprintln(a.out, "  cyberagent edit propose|list|show|review-approve|review-deny|approve|deny")
	fmt.Fprintln(a.out, "  cyberagent approval list|show|approve-once|approve-for-thread|deny|grant")
	fmt.Fprintln(a.out, "  cyberagent sandbox validate|template|local-readiness")
	fmt.Fprintln(a.out, "  cyberagent artifact list|show|read|verify")
	fmt.Fprintln(a.out, "  cyberagent ui-evidence list|show|artifact")
	fmt.Fprintln(a.out, "  cyberagent web-evidence list --run <run-id> [--limit <n>]")
	fmt.Fprintln(a.out, "  cyberagent analyzer execute")
	fmt.Fprintln(a.out, "  cyberagent report show|finding|check")
	fmt.Fprintln(a.out, "  cyberagent report finding attach|validate|reject|accept|remediation|fix|verify")
	fmt.Fprintln(a.out, "  cyberagent api serve|openapi")
	fmt.Fprintln(a.out, "  cyberagent git-advanced status|discover-hunks|preview|run")
	fmt.Fprintln(a.out, "  cyberagent drydock status|create|use|checkpoint|rewind|undo|fork|deliver|cleanup|reconcile|gc")
	fmt.Fprintln(a.out, "  cyberagent github-review configure|credential|login|qualify|fetch|evidence|status|write|recover")
	fmt.Fprintln(a.out, "  cyberagent mcp serve|client|credential")
	fmt.Fprintln(a.out, "  cyberagent plugin stage|list|show|review|rollback|trust-publisher|revoke-publisher|stage-mcp")
	fmt.Fprintln(a.out, "  cyberagent code-intel status|qualify")
	fmt.Fprintln(a.out, "  cyberagent headless events")
	fmt.Fprintln(a.out, "  cyberagent run create|adapt-task|list|show|mode|phase|execution-profile|execution-interaction|execution-permission|capability-readiness|command-plan|command-execute|host-execute|project-action|events|usage|start|step|execute|checkpoint|graph|lease|finish|fail|pause|resume|cancel|delegations|delegation|plans|plan|delivery|steer|fanouts|fanout|sandbox|wake|schedule")
	fmt.Fprintln(a.out, "  cyberagent run plan show|choose|selection")
	fmt.Fprintln(a.out, "  cyberagent run sandbox docker-readiness|docker-admit|docker-start|docker-cancel|docker-status")
	fmt.Fprintln(a.out, "  cyberagent run standard-code docker-readiness|docker-prepare|docker-execute|docker-cancel|docker-recover")
	fmt.Fprintln(a.out, "  cyberagent run delivery report|record|checkpoint|list|show")
	fmt.Fprintln(a.out, "  cyberagent run fanout plan|execute|show|execution|report")
	fmt.Fprintln(a.out, "  cyberagent todo create|list|show|update|start|block|reopen|complete|cancel")
	fmt.Fprintln(a.out, "  cyberagent note create|list|show|update|archive|restore")
	fmt.Fprintln(a.out, "  cyberagent tui [--run <run-id> | --session <session-id>] [--print]")
}

func (a *App) ensureStore() error {
	if a.store != nil {
		return nil
	}
	dbPath := filepath.Join(a.home, "cyberagent.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	a.store = st
	if err := a.models.LoadRouteSettings(context.Background(), a.store); err != nil {
		a.store = nil
		_ = st.Close()
		return err
	}
	if err := a.ensureCodeIntel(); err != nil {
		a.store = nil
		_ = st.Close()
		return err
	}
	return nil
}

func (a *App) ensureCodeIntel() error {
	if a == nil || a.codeIntelConfigLoaded {
		return nil
	}
	a.codeIntelConfigLoaded = true
	path := strings.TrimSpace(a.codeIntelConfigPath)
	if path == "" {
		return nil
	}
	manager, digest, err := codeintel.NewManagerFromConfig(path)
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"load explicit code-intel configuration", err)
	}
	a.codeIntel = manager
	a.codeIntelConfigDigest = digest
	return nil
}

func (a *App) workspaceManager() (*workspace.Manager, error) {
	if err := a.ensureStore(); err != nil {
		return nil, err
	}
	return workspace.NewManager(a.home, a.store), nil
}

func (a *App) workspaceCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("workspace subcommand is required")
	}
	mgr, err := a.workspaceManager()
	if err != nil {
		return err
	}
	switch args[0] {
	case "init":
		fs := newFlagSet("workspace init", a.errOut)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: cyberagent workspace init <name>")
		}
		rec, err := mgr.Init(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "workspace %s initialized at %s\n", rec.Name, rec.RootPath)
		return nil
	case "list":
		recs, err := a.store.ListWorkspaces(ctx)
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			fmt.Fprintln(a.out, "no workspaces")
			return nil
		}
		for _, rec := range recs {
			fmt.Fprintf(a.out, "%s\t%s\n", rec.Name, rec.RootPath)
		}
		return nil
	case "show":
		fs := newFlagSet("workspace show", a.errOut)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: cyberagent workspace show <name>")
		}
		rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(fs.Arg(0)))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "name: %s\nid: %s\npath: %s\ncreated_at: %s\n", rec.Name, rec.ID, rec.RootPath, rec.CreatedAt.Format(time.RFC3339))
		return nil
	case "tree":
		return a.workspaceTree(ctx, args[1:])
	case "read":
		return a.workspaceRead(ctx, args[1:])
	case "checkpoint":
		return a.workspaceCheckpointCommand(ctx, args[1:])
	default:
		return fmt.Errorf("unknown workspace subcommand %q", args[0])
	}
}

func (a *App) workspaceTree(ctx context.Context, args []string) error {
	fs := newFlagSet("workspace tree", a.errOut)
	depth := fs.Int("depth", 2, "maximum recursive depth")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"depth": true})); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("usage: cyberagent workspace tree <workspace> [path] [--depth <n>]")
	}
	rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(fs.Arg(0)))
	if err != nil {
		return err
	}
	path := "."
	if fs.NArg() == 2 {
		path = fs.Arg(1)
	}
	outcome, err := a.newToolGateway().Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ListWorkspaceTool, WorkspaceID: rec.ID, WorkspaceRoot: rec.RootPath, RequestedBy: "cli",
		Arguments: map[string]string{
			"path":      path,
			"max_depth": strconv.Itoa(*depth),
		},
	})
	if outcome.Result != nil && outcome.Result.Stdout != "" {
		fmt.Fprintln(a.out, outcome.Result.Stdout)
	}
	if outcome.Result != nil && outcome.Result.Stderr != "" {
		fmt.Fprintln(a.errOut, outcome.Result.Stderr)
	}
	if err == nil && !outcome.Decision.Allowed {
		return apperror.New(apperror.CodePolicyDenied, "policy denied workspace list: "+outcome.Decision.Reason)
	}
	return err
}

func (a *App) workspaceRead(ctx context.Context, args []string) error {
	fs := newFlagSet("workspace read", a.errOut)
	maxBytes := fs.Int("max-bytes", tools.DefaultMaxReadBytes, "maximum bytes to print")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"max-bytes": true})); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: cyberagent workspace read <workspace> <path> [--max-bytes <n>]")
	}
	rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(fs.Arg(0)))
	if err != nil {
		return err
	}
	outcome, err := a.newToolGateway().Invoke(ctx, toolgateway.ToolCall{
		Name: toolgateway.ReadFileTool, WorkspaceID: rec.ID, WorkspaceRoot: rec.RootPath, RequestedBy: "cli",
		Arguments: map[string]string{
			"path":      fs.Arg(1),
			"max_bytes": strconv.Itoa(*maxBytes),
		},
	})
	if outcome.Result != nil && outcome.Result.Stdout != "" {
		fmt.Fprintln(a.out, outcome.Result.Stdout)
	}
	if outcome.Result != nil && outcome.Result.Stderr != "" {
		fmt.Fprintln(a.errOut, outcome.Result.Stderr)
	}
	if err == nil && !outcome.Decision.Allowed {
		return apperror.New(apperror.CodePolicyDenied, "policy denied workspace read: "+outcome.Decision.Reason)
	}
	return err
}

func (a *App) scriptCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("script subcommand is required")
	}
	switch args[0] {
	case "new":
		return a.scriptNew(ctx, args[1:])
	case "run":
		return a.scriptRun(ctx, args[1:])
	default:
		return fmt.Errorf("unknown script subcommand %q", args[0])
	}
}

func (a *App) scriptNew(ctx context.Context, args []string) error {
	mgr, err := a.workspaceManager()
	if err != nil {
		return err
	}
	fs := newFlagSet("script new", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace name")
	language := fs.String("language", "python", "script language")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"workspace": true, "language": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(`usage: cyberagent script new "goal" --workspace <name>`)
	}
	goal := fs.Arg(0)
	wsName := *workspaceName
	if wsName == "" {
		wsName = goal
	}
	rec, err := mgr.Ensure(ctx, wsName)
	if err != nil {
		return err
	}
	task := agent.Task{
		ID:          idgen.New("task"),
		Kind:        agent.TaskScript,
		Goal:        goal,
		WorkspaceID: rec.ID,
		Mode:        *language,
		Status:      agent.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
	if err := a.store.SaveTask(ctx, task); err != nil {
		return err
	}
	scriptPath := mgr.ScriptPath(rec, task, scriptExt(*language))
	if err := os.WriteFile(scriptPath, []byte(scriptTemplate(goal, *language)), 0o644); err != nil {
		return err
	}
	if err := a.store.SaveArtifact(ctx, store.ArtifactRecord{
		ID:          idgen.New("artifact"),
		WorkspaceID: rec.ID,
		TaskID:      task.ID,
		Path:        scriptPath,
		Kind:        "script",
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		return err
	}
	relativeScriptPath, err := filepath.Rel(rec.RootPath, scriptPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "script template created\nlegacy_task: %s\nworkspace: %s\nscript: %s\nscript_relative: %s\nstatus: template_only\nmodel_calls: 0\nnext: cyberagent thread create %q --workspace %s --profile script\n",
		task.ID, rec.RootPath, scriptPath, filepath.ToSlash(relativeScriptPath), goal, rec.Name)
	return nil
}

func (a *App) scriptRun(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("script run", a.errOut)
	workspaceName := fs.String("workspace", "", "workspace containing the script")
	local := fs.Bool("local", false, "record a local backend request; execution remains disabled")
	operationKey := fs.String("idempotency-key", "", "stable retry key; generated when omitted")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"workspace": true, "local": false, "idempotency-key": true,
	})); err != nil {
		return err
	}
	if fs.NArg() < 1 || strings.TrimSpace(*workspaceName) == "" {
		return errors.New("usage: cyberagent script run <workspace-relative-path> --workspace <name> [--local] [args...]")
	}
	rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(*workspaceName))
	if err != nil {
		return err
	}
	rawPath := strings.TrimSpace(fs.Arg(0))
	if _, err := tools.NewWorkspaceFS(rec.RootPath).ResolveFileForRead(rawPath); err != nil {
		if os.IsNotExist(err) {
			return err
		}
		return apperror.Wrap(apperror.CodeInvalidArgument, "invalid script path: "+err.Error(), err)
	}
	scriptPath := filepath.ToSlash(filepath.Clean(rawPath))
	cmd, cmdArgs := commandForScript(scriptPath, fs.Args()[1:])
	requestedBackend := "sandbox"
	if *local {
		requestedBackend = "local"
	}
	processProposal := toolgateway.ScriptProcessProposal{
		Executable: cmd, Arguments: cmdArgs, WorkingDirectory: ".", RequestedBackend: requestedBackend,
	}
	if _, err := toolgateway.EncodeScriptProcessProposal(processProposal); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	key := strings.TrimSpace(*operationKey)
	if key == "" {
		key = idgen.New("scriptop")
	}
	gateway := a.newToolGateway()
	result, err := application.NewScriptProcessService(a.store, gateway).Create(ctx, application.CreateScriptProcessRunRequest{
		Run: application.CreateRunRequest{
			Goal: "Review execution of workspace script " + scriptPath, Profile: string(domain.ProfileScript),
			WorkspaceID: rec.ID, Interactive: true, Budget: domain.DefaultBudget(),
		},
		OperationKey: key, RequestedBy: "script_cli", Process: processProposal,
	})
	if err != nil {
		return err
	}
	if result.Outcome.Proposal == nil {
		return errors.New("script run did not create a tool proposal")
	}
	fmt.Fprintf(a.out, "script process proposal %s\nmission: %s\nrun: %s\nsession: %s\nworkspace: %s\nscript: %s\nrequested_backend: %s\nstatus: %s\napproval: %s\nreplayed: %t\nidempotency_key: %s\nexecution: disabled; approval completes as dry run\n",
		result.Process.ID, result.Mission.ID, result.Run.ID, result.Run.SessionID, rec.ID, scriptPath,
		requestedBackend, result.Outcome.Proposal.Status, result.Outcome.Decision.Approval,
		result.Replayed, redact.String(key))
	if !result.Outcome.Decision.Allowed {
		return apperror.New(apperror.CodePolicyDenied, "policy denied script run: "+result.Outcome.Decision.Reason)
	}
	return nil
}

func (a *App) ctfCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("ctf subcommand is required")
	}
	switch args[0] {
	case "init":
		return a.ctfInit(ctx, args[1:])
	case "analyze":
		return a.ctfAnalyze(ctx, args[1:])
	case "writeup":
		return a.ctfWriteup(ctx, args[1:])
	default:
		return fmt.Errorf("unknown ctf subcommand %q", args[0])
	}
}

func (a *App) ctfInit(ctx context.Context, args []string) error {
	mgr, err := a.workspaceManager()
	if err != nil {
		return err
	}
	fs := newFlagSet("ctf init", a.errOut)
	category := fs.String("category", "misc", "challenge category")
	scope := fs.String("scope", "local", "authorized target scope")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"category": true, "scope": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent ctf init <name> --category <category>")
	}
	rec, err := mgr.Init(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	task := agent.Task{
		ID:          idgen.New("task"),
		Kind:        agent.TaskCTF,
		Goal:        fmt.Sprintf("Initialize %s challenge in category %s with scope %s", rec.Name, *category, *scope),
		WorkspaceID: rec.ID,
		Mode:        *category,
		Status:      agent.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
	if err := a.store.SaveTask(ctx, task); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "ctf workspace %s initialized\ncategory: %s\nscope: %s\npath: %s\n", rec.Name, *category, *scope, rec.RootPath)
	return nil
}

func (a *App) ctfAnalyze(ctx context.Context, args []string) error {
	if err := a.ensureStore(); err != nil {
		return err
	}
	fs := newFlagSet("ctf analyze", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent ctf analyze <workspace>")
	}
	rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(fs.Arg(0)))
	if err != nil {
		return err
	}
	task := agent.Task{
		ID:          idgen.New("task"),
		Kind:        agent.TaskCTF,
		Goal:        "analyze CTF workspace " + rec.Name,
		WorkspaceID: rec.ID,
		Mode:        "analysis-template",
		Status:      agent.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
	if err := a.store.SaveTask(ctx, task); err != nil {
		return err
	}
	analysisPath := filepath.Join(rec.RootPath, "outputs", "analysis-"+task.ID+".md")
	if err := os.WriteFile(analysisPath, []byte(ctfAnalysisTemplate(rec.Name)), 0o644); err != nil {
		return err
	}
	if err := a.store.SaveArtifact(ctx, store.ArtifactRecord{
		ID:          idgen.New("artifact"),
		WorkspaceID: rec.ID,
		TaskID:      task.ID,
		Path:        analysisPath,
		Kind:        "analysis_template",
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		return err
	}
	relativeAnalysisPath, err := filepath.Rel(rec.RootPath, analysisPath)
	if err != nil {
		return err
	}
	goal := "Analyze CTF workspace " + rec.Name
	fmt.Fprintf(a.out, "ctf analysis template created\nlegacy_task: %s\nworkspace: %s\nanalysis: %s\nanalysis_relative: %s\nstatus: template_only\nmodel_calls: 0\nnext: cyberagent thread create %q --workspace %s --profile code\n",
		task.ID, rec.RootPath, analysisPath, filepath.ToSlash(relativeAnalysisPath), goal, rec.Name)
	return nil
}

func (a *App) ctfWriteup(ctx context.Context, args []string) error {
	mgr, err := a.workspaceManager()
	if err != nil {
		return err
	}
	fs := newFlagSet("ctf writeup", a.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cyberagent ctf writeup <workspace>")
	}
	rec, err := a.store.GetWorkspaceByName(ctx, workspace.Slug(fs.Arg(0)))
	if err != nil {
		return err
	}
	path := mgr.WriteupPath(rec)
	content := fmt.Sprintf("# %s Writeup\n\nStatus: mock writeup scaffold.\n\n- Record observations in attachments/ and logs/.\n- Keep exploit steps scoped to authorized CTF targets.\n", rec.Name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "writeup scaffold: %s\n", path)
	return nil
}

func (a *App) learnCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "ask" {
		return errors.New(`usage: cyberagent learn ask "question"`)
	}
	fs := newFlagSet("learn ask", a.errOut)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(`usage: cyberagent learn ask "question"`)
	}
	resp, err := a.router.Chat(ctx, "learn", llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: fs.Arg(0)}},
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, resp.Text)
	return nil
}

func (a *App) providerCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("provider subcommand is required")
	}
	switch args[0] {
	case "list":
		if a.models == nil {
			return errors.New("model registry is unavailable")
		}
		for _, provider := range a.models.Snapshot().Providers {
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", provider.Name, provider.Kind,
				provider.Status)
		}
		return nil
	case "test":
		if err := a.ensureStore(); err != nil {
			return err
		}
		route := "learn"
		if len(args) > 1 {
			route = args[1]
		}
		ref, err := a.providerModelRef(route)
		if err != nil {
			return err
		}
		result, err := application.NewModelControlService(a.models, a.store).Diagnose(ctx,
			application.DiagnoseProviderRequest{
				Version: modelregistry.DiagnosticProtocolVersion, Provider: ref.Provider,
				Model: ref.Model, ConfirmDiagnostic: true,
			})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "protocol: %s\nprovider: %s\nmodel: %s\nstatus: %s\noutcome: %s\nfailure_reason: %s\nqualification_status: %s\nretryable: %t\nnetwork_request_attempted: %t\nmodel_called: %t\ntool_called: %t\nresponse_content_returned: %t\nduration_ms: %d\n",
			result.ProtocolVersion, result.Provider, result.Model, result.Status,
			result.Outcome, result.FailureReason, result.QualificationStatus, result.Retryable,
			result.NetworkRequestAttempted,
			result.ModelCalled, result.ToolCalled, result.ResponseContentReturned,
			result.DurationMillis)
		return nil
	case "qualify":
		if len(args) != 2 {
			return errors.New("usage: cyberagent provider qualify <route|provider/model>")
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		ref, err := a.providerModelRef(args[1])
		if err != nil {
			return err
		}
		result, err := application.NewModelControlService(a.models, a.store).QualifyHarness(ctx,
			application.QualifyModelHarnessRequest{
				Version:  modelregistry.HarnessQualificationProtocolVersion,
				Provider: ref.Provider, Model: ref.Model, ConfirmQualification: true,
			})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "protocol: %s\nprovider: %s\nmodel: %s\nstatus: %s\noutcome: %s\nfailure_reason: %s\nqualification_status: %s\nretryable: %t\nnetwork_request_attempted: %t\nmodel_calls: %d\nsynthetic_tool_calls: %d\ntool_executed: %t\nresponse_content_returned: %t\ntransport_protocol: %s\ntool_strategy: %s\njson_strategy: %s\nharness_qualification_status: %s\nroot_eligible: %t\nduration_ms: %d\n",
			result.ProtocolVersion, result.Provider, result.Model, result.Status,
			result.Outcome, result.FailureReason, result.QualificationStatus, result.Retryable,
			result.NetworkRequestAttempted,
			result.ModelCalls, result.SyntheticToolCalls, result.ToolExecuted,
			result.ResponseContentReturned, result.Harness.TransportProtocol,
			result.Harness.ToolStrategy, result.Harness.JSONStrategy,
			result.Harness.QualificationStatus, result.Harness.RootEligible,
			result.DurationMillis)
		return nil
	case "price-import":
		if err := a.ensureStore(); err != nil {
			return err
		}
		fs := newFlagSet("provider price-import", a.errOut)
		file := fs.String("file", "", "path to the operator price table document")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
			return errors.New("usage: cyberagent provider price-import --file <path>")
		}
		raw, err := os.ReadFile(strings.TrimSpace(*file))
		if err != nil {
			return err
		}
		snapshot, err := pricing.ParseWire(raw, time.Now().UTC())
		if err != nil {
			return err
		}
		stored, replayed, err := a.store.ImportPriceSnapshot(ctx, snapshot)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "imported: %t\nid: %s\ncurrency: %s\nentries: %d\nfingerprint: %s\n",
			!replayed, stored.ID, stored.Currency, len(stored.Entries), stored.Fingerprint)
		return nil
	case "price-list":
		if err := a.ensureStore(); err != nil {
			return err
		}
		list, err := a.store.ListPriceSnapshots(ctx, 32)
		if err != nil {
			return err
		}
		for _, snapshot := range list {
			fmt.Fprintf(a.out, "%s\t%s\t%s\t%d\t%s\n", snapshot.ID, snapshot.Source,
				snapshot.ValidFrom.Format(time.RFC3339), len(snapshot.Entries), snapshot.Fingerprint)
		}
		return nil
	default:
		return fmt.Errorf("unknown provider subcommand %q", args[0])
	}
}

func (a *App) providerModelRef(route string) (llm.ModelRef, error) {
	route = strings.TrimSpace(route)
	if strings.Contains(route, "/") {
		return llm.ParseModelRef(route)
	}
	for _, candidate := range a.models.Snapshot().Routes {
		if candidate.Name == route && candidate.Available {
			return llm.ModelRef{Provider: candidate.Provider, Model: candidate.Model}, nil
		}
	}
	return llm.ModelRef{}, fmt.Errorf("model route %q is unavailable", route)
}

func (a *App) modelCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("model subcommand is required")
	}
	switch args[0] {
	case "list":
		if err := a.ensureStore(); err != nil {
			return err
		}
		models, err := a.router.ListModels(ctx)
		if err != nil {
			return err
		}
		for _, model := range models {
			fmt.Fprintf(a.out, "%s/%s\t%s\t%s\n", model.Provider, model.ID, model.DisplayName, strings.Join(model.Capabilities, ","))
		}
		routes := a.router.Routes()
		names := make([]string, 0, len(routes))
		for name := range routes {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintln(a.out, "routes:")
		for _, name := range names {
			ref := routes[name]
			fmt.Fprintf(a.out, "%s -> %s/%s\n", name, ref.Provider, ref.Model)
		}
		return nil
	case "set":
		if err := a.ensureStore(); err != nil {
			return err
		}
		fs := newFlagSet("model set", a.errOut)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 2 {
			return errors.New("usage: cyberagent model set <route> <provider/model>")
		}
		ref, err := llm.ParseModelRef(fs.Arg(1))
		if err != nil {
			return err
		}
		selected, err := application.NewModelControlService(a.models, a.store).SelectRoute(ctx,
			application.SelectModelRouteRequest{
				Version: modelregistry.RouteControlProtocolVersion, Route: fs.Arg(0),
				Provider: ref.Provider, Model: ref.Model,
			})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "route %s set to %s/%s\navailable: %t\n",
			selected.Name, selected.Provider, selected.Model, selected.Available)
		return nil
	default:
		return fmt.Errorf("unknown model subcommand %q", args[0])
	}
}

func newFlagSet(name string, errOut io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	return fs
}

func scriptExt(language string) string {
	switch strings.ToLower(language) {
	case "python", "py":
		return ".py"
	case "bash", "sh":
		return ".sh"
	case "go":
		return ".go"
	case "node", "javascript", "js":
		return ".js"
	default:
		return ".txt"
	}
}

func scriptTemplate(goal string, language string) string {
	switch strings.ToLower(language) {
	case "bash", "sh":
		return "#!/usr/bin/env bash\nset -euo pipefail\n\n# Template goal: " + sanitizeLine(goal) + "\necho \"local script template\"\n"
	case "go":
		return "package main\n\nimport \"fmt\"\n\nfunc main() {\n\t// Template goal: " + sanitizeLine(goal) + "\n\tfmt.Println(\"local script template\")\n}\n"
	case "node", "javascript", "js":
		return "// Template goal: " + sanitizeLine(goal) + "\nconsole.log(\"local script template\");\n"
	default:
		return "#!/usr/bin/env python3\n\"\"\"Local script template generated by Universal Code; no task has been performed.\"\"\"\n\nGOAL = " + fmt.Sprintf("%q", goal) + "\n\nif __name__ == \"__main__\":\n    print(\"local script template\")\n    print(f\"goal: {GOAL}\")\n"
	}
}

func ctfAnalysisTemplate(workspaceName string) string {
	return fmt.Sprintf("# %s Analysis Worksheet\n\nStatus: analysis not started. This local worksheet was created without a model call.\n\n## Scope\n\n- Authorized target:\n- Category:\n- Constraints:\n\n## Observations\n\n- Add evidence from attachments/ and logs/.\n\n## Hypotheses and checks\n\n- [ ] Record a hypothesis and the evidence that supports or rejects it.\n\n## Findings\n\n- Record validated findings here.\n", workspaceName)
}

func sanitizeLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func commandForScript(path string, rest []string) (string, []string) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".py":
		return "python", append([]string{path}, rest...)
	case ".js":
		return "node", append([]string{path}, rest...)
	case ".sh":
		return "bash", append([]string{path}, rest...)
	default:
		return path, rest
	}
}

func reorderFlags(args []string, takesValue map[string]bool) []string {
	flags := make([]string, 0, len(args))
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			name := strings.TrimLeft(arg, "-")
			if idx := strings.Index(name, "="); idx >= 0 {
				name = name[:idx]
			}
			takes, ok := takesValue[name]
			if !ok {
				rest = append(rest, arg)
				continue
			}
			flags = append(flags, arg)
			if takes && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		rest = append(rest, arg)
	}
	return append(flags, rest...)
}
