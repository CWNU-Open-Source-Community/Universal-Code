package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

// The test owns no provider credentials. An external runner can dispatch each
// exported request, then supply its exact ChatResponse bound to that file.
type qualityExchangeProvider struct {
	*generatedProtocolProvider
	ref  llm.ModelRef
	call func(llm.ChatRequest) (*llm.ChatResponse, error)
}

func (p *qualityExchangeProvider) Name() string { return p.ref.Provider }
func (p *qualityExchangeProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{Provider: p.Name(), ID: p.ref.Model, Capabilities: []string{"chat", "tools", "json"}}}, nil
}
func (p *qualityExchangeProvider) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return p.call(req)
}
func (p *qualityExchangeProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	answer, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if answer.Text != "" {
		chunks <- llm.ChatChunk{Text: answer.Text}
	}
	chunks <- llm.FinalChatChunk(answer)
	close(chunks)
	return chunks, nil
}

func writeQualityArtifact(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(raw, '\n'), 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func copyQualitySnapshot(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err == nil {
		err = os.WriteFile(to, raw, 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

type qualityRun struct {
	ID            string
	Session       string
	Route         string
	Window        int
	CaseHash      string
	ContextWindow *llm.ContextWindow `json:",omitempty"`
}

// Older manifests recorded only the total, so their other limits still come
// from the settings used to replay them.
func (run qualityRun) replayWindow(configured llm.ContextWindow, requested *int) (llm.ContextWindow, error) {
	window := configured
	if run.ContextWindow != nil {
		window = *run.ContextWindow
	}
	if err := window.Validate(); err != nil {
		return llm.ContextWindow{}, fmt.Errorf("invalid replay context window: %w", err)
	}
	if run.Window != window.WindowTokens || (requested != nil && *requested != run.Window) {
		return llm.ContextWindow{}, fmt.Errorf("saved context window of %d tokens conflicts with the replay settings", run.Window)
	}
	return window, nil
}

func TestQualityReplayRestoresRecordedContextWindow(t *testing.T) {
	fallback := llm.DefaultContextWindow()
	provider := llm.ContextWindow{ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: 1000000,
		SafetyMarginTokens: 2048, DefaultOutputTokens: 8192, MaxOutputTokens: 393216, Source: "provider_model_metadata"}
	run := qualityRun{Window: provider.WindowTokens, ContextWindow: &provider}
	for _, override := range []*int{nil, &provider.WindowTokens} {
		if got, err := run.replayWindow(fallback, override); err != nil || got != provider {
			t.Fatalf("expected the full saved provider settings, got %+v: %v", got, err)
		}
	}
	if _, err := run.replayWindow(fallback, &fallback.WindowTokens); err == nil {
		t.Fatal("accepted an explicit window that conflicts with the saved run")
	}
	legacy := qualityRun{Window: fallback.WindowTokens}
	if got, err := legacy.replayWindow(fallback, nil); got != fallback || err != nil {
		t.Fatalf("legacy replay limits: %+v, %v", got, err)
	}
	run.Window++
	if _, err := run.replayWindow(fallback, nil); err == nil {
		t.Fatal("accepted inconsistent total limits in the manifest")
	}
	run.Window = provider.WindowTokens
	provider.DefaultOutputTokens = provider.MaxOutputTokens + 1
	if _, err := run.replayWindow(fallback, nil); err == nil {
		t.Fatal("accepted invalid output limits from the manifest")
	}
}

func qualityDialog(t *testing.T, st *store.SQLiteStore, router *llm.Router, p *qualityExchangeProvider, runID string, tc compactionQualityCase, round int) {
	t.Helper()
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	for i, input := range tc.Rounds[round] {
		ordinal := i + 1
		if round == 1 {
			ordinal += len(tc.Rounds[0])
		}
		read := tc.ToolTurn != nil && tc.ToolTurn.UserMessage == ordinal
		issued, unplannedSummary := false, false
		p.call = func(req llm.ChatRequest) (*llm.ChatResponse, error) {
			if req.Metadata["purpose"] == llm.ModelPurposeContextCompaction {
				unplannedSummary = true
				return nil, errors.New("unexpected automatic compaction while seeding history")
			}
			reply := "收到。"
			if read {
				if !issued {
					issued = true
					a := boundaryRead("quality-read-historical-draft", 1)
					a.Provider, a.Model = p.ref.Provider, p.ref.Model
					return a, nil
				}
				reply = tc.ToolTurn.AssistantReply
			}
			a := textResponse(rootActionResponse(domain.RootActionFinish, reply, "已记录本轮。", ""))
			a.Provider, a.Model = p.ref.Provider, p.ref.Model
			return a, nil
		}
		submitHistoryRecall(t, turns, runID, fmt.Sprintf("quality-%s-input-%02d", tc.ID, ordinal), input)
		if unplannedSummary {
			t.Fatalf("user turn %d triggered an extra compaction; this case requires exactly two", ordinal)
		}
		if read && !issued {
			t.Fatal("historical draft was not requested")
		}
	}
}

func qualityExchange(t *testing.T, dir, stage string, req llm.ChatRequest) (*llm.ChatResponse, error) {
	t.Helper()
	path := filepath.Join(dir, stage+"-request.json")
	frozen, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		writeQualityArtifact(t, path, req)
		frozen, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	var saved llm.ChatRequest
	if err := json.Unmarshal(frozen, &saved); err != nil {
		return nil, err
	}
	// These IDs name a new local attempt when reopening a snapshot. Compare
	// the model, messages, tools, options and all other request metadata.
	for _, key := range []string{"attempt_id", "context_operation_id"} {
		delete(saved.Metadata, key)
	}
	encoded, _ := json.Marshal(req)
	var current llm.ChatRequest
	if err := json.Unmarshal(encoded, &current); err != nil {
		return nil, err
	}
	for _, key := range []string{"attempt_id", "context_operation_id"} {
		delete(current.Metadata, key)
	}
	a, _ := json.Marshal(saved)
	b, _ := json.Marshal(current)
	if !bytes.Equal(a, b) {
		return nil, fmt.Errorf("%s changed against its frozen request; use a fresh experiment directory", stage)
	}
	var receipt struct {
		RequestSHA256 string `json:"request_sha256"`
		Response      *llm.ChatResponse
	}
	data, err := os.ReadFile(filepath.Join(dir, stage+"-response.json"))
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &receipt); err != nil {
		return nil, err
	}
	if receipt.RequestSHA256 != fmt.Sprintf("%x", sha256.Sum256(frozen)) || receipt.Response == nil ||
		(receipt.Response.Text == "" && len(receipt.Response.ToolCalls) == 0) {
		return nil, fmt.Errorf("%s response is empty or belongs to another request", stage)
	}
	return receipt.Response, nil
}

func TestThreadCompactionQualityExchange(t *testing.T) {
	dir := os.Getenv("TRAVERSE_COMPACTION_QUALITY_DIR")
	if dir == "" {
		t.Skip("set TRAVERSE_COMPACTION_QUALITY_DIR to capture requests and replay real responses")
	}
	runThreadCompactionQuality(t, dir, qualityFromFiles)
}

type qualityMode int

const (
	qualityFromFiles qualityMode = iota
	qualityInspectSources
	qualityLive
)

func TestThreadCompactionQualitySources(t *testing.T) {
	runThreadCompactionQuality(t, t.TempDir(), qualityInspectSources)
}

func runThreadCompactionQuality(t *testing.T, dir string, mode qualityMode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	route := os.Getenv("TRAVERSE_COMPACTION_QUALITY_ROUTE")
	if route == "" {
		route = "deepseek/deepseek-v4-flash"
	}
	ref, err := llm.ParseModelRef(route)
	if err != nil {
		t.Fatal(err)
	}
	window := llm.DefaultContextWindow()
	var requestedWindow *int
	if option := os.Getenv("TRAVERSE_COMPACTION_QUALITY_WINDOW"); option != "" {
		tokens, err := strconv.Atoi(option)
		if err != nil {
			t.Fatal(err)
		}
		window.WindowTokens, requestedWindow = tokens, &tokens
	}
	var liveRouter *llm.Router
	var trace *liveCompactionTransport
	if mode == qualityLive {
		liveRouter, trace = qualityLiveRouter(t, ctx, dir, ref, requestedWindow)
		window = liveRouter.ContextWindow(ref)
	}
	for _, tc := range compactionQualityCases(t) {
		runCase := func(t *testing.T, parentDir string, archiveFits bool) {
			out := filepath.Join(parentDir, tc.ID)
			workspace := filepath.Join(out, "workspace")
			if err := os.MkdirAll(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			corpus, _ := json.Marshal(tc)
			caseHash := fmt.Sprintf("%x", sha256.Sum256(corpus))
			var run qualityRun
			manifest := filepath.Join(out, "run.json")
			dbPath := filepath.Join(out, "snapshot-0.sqlite")
			temporary := filepath.Join(t.TempDir(), "working.sqlite")
			caseWindow := window
			raw, err := os.ReadFile(manifest)
			newRun := errors.Is(err, os.ErrNotExist)
			if !newRun {
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &run); err != nil || run.CaseHash != caseHash || run.Route != route {
					t.Fatalf("saved experiment inputs differ: %+v, %v", run, err)
				}
				if mode == qualityFromFiles {
					caseWindow, err = run.replayWindow(window, requestedWindow)
					if err != nil {
						t.Fatal(err)
					}
				} else if run.Window != window.WindowTokens || (run.ContextWindow != nil && *run.ContextWindow != window) {
					t.Fatal("the saved experiment used a different context window")
				}
			}
			p := &qualityExchangeProvider{generatedProtocolProvider: newGeneratedProtocolProvider(), ref: ref}
			router := llm.NewRouter(ref)
			router.RegisterProvider(p)
			if err := router.SetContextWindow(ref, caseWindow); err != nil {
				t.Fatal(err)
			}
			stageRouter := router
			if liveRouter != nil {
				stageRouter = liveRouter
			}
			if newRun {
				run = qualityRun{CaseHash: caseHash, Route: route, Window: caseWindow.WindowTokens, ContextWindow: &caseWindow}
				if tc.ToolTurn != nil {
					if err := os.WriteFile(filepath.Join(workspace, tc.ToolTurn.Path), []byte(tc.ToolTurn.Content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				st := openHistoryRecallStore(t, temporary)
				defer st.Close()
				if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "quality-" + tc.ID, Name: tc.ID, RootPath: workspace, CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				_, created, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
					Goal: tc.Rounds[0][0], Profile: "code", WorkspaceID: "quality-" + tc.ID, ModelRoute: route, Interactive: true,
					NetworkMode: "disabled", Budget: domain.Budget{MaxTurns: 20, MaxToolCalls: 5, MaxTokens: 300000},
				})
				if err != nil {
					t.Fatal(err)
				}
				run.ID, run.Session = created.ID, created.SessionID
				qualityDialog(t, st, router, p, run.ID, tc, 0)
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				copyQualitySnapshot(t, temporary, dbPath)
				writeQualityArtifact(t, manifest, run)
			}

			var lastSummaryID int64
			for i, stage := range []string{"summary-1", "summary-2", "answer"} {
				next := filepath.Join(out, fmt.Sprintf("snapshot-%d.sqlite", i+1))
				if i < 2 {
					if _, err := os.Stat(next); err == nil {
						// Load its committed chain identity even on a resumed run.
						raw, err := os.ReadFile(filepath.Join(out, stage+"-committed.json"))
						if err != nil {
							t.Fatal(err)
						}
						var summary contextmgr.Summary
						if err := json.Unmarshal(raw, &summary); err != nil {
							t.Fatal(err)
						}
						lastSummaryID, dbPath = summary.ID, next
						continue
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				}
				copyQualitySnapshot(t, dbPath, temporary)
				st := openHistoryRecallStore(t, temporary)
				defer st.Close()
				calls, name := 0, stage
				var exchangeErr error
				var captured llm.ChatRequest
				if trace != nil {
					trace.mark(tc.ID+"-"+stage, i+1, "")
				}
				p.call = func(req llm.ChatRequest) (*llm.ChatResponse, error) {
					if exchangeErr != nil {
						return nil, exchangeErr
					}
					if (req.Metadata["purpose"] == llm.ModelPurposeContextCompaction) != (i < 2) {
						exchangeErr = fmt.Errorf("%s requested an unexpected model purpose %q; this case requires exactly two manual compactions", stage, req.Metadata["purpose"])
						return nil, exchangeErr
					}
					calls++
					if mode == qualityInspectSources {
						captured = req
						text := rootActionResponse(domain.RootActionFinish, "已捕获请求。", "已记录。", "")
						if i < 2 {
							report := qualityStaleHandoff(tc)
							if i == 1 && archiveFits {
								// Reserve enough for the thirteen archived rows and both user quotations.
								report += "\n\n用户还要求沿用渠道名称的原有拼写。助手已读取上周的 README.md，将草稿中写明的分组方式以及其中勾选的事项一并告知用户。" +
									"已有的工具回执仅涉及读取旧草稿。用户要求记录本周的待办项，针对本周清单的核对尚未执行。" +
									"草稿中勾选的是上周的记录，这次读取没有检查 staging-04 的 manifest.json。"
							}
							text = historyRecallJSON(map[string]string{"version": "generated_handoff.v1", "summary": report})
						}
						reply := textResponse(text)
						reply.Provider, reply.Model = ref.Provider, ref.Model
						return reply, nil
					}
					if calls > 1 {
						name = fmt.Sprintf("%s-%d", stage, calls)
					}
					var response *llm.ChatResponse
					response, exchangeErr = qualityExchange(t, out, name, req)
					return response, exchangeErr
				}
				checkReceipt := func() {
					t.Helper()
					if errors.Is(exchangeErr, os.ErrNotExist) {
						t.Skipf("request captured; waiting for %s-response.json", name)
					}
					if exchangeErr != nil {
						t.Fatal(exchangeErr)
					}
				}
				if i < 2 {
					req := domain.ThreadContextCompactionRequest{Version: domain.ThreadContextCompactionVersion, ThreadID: domain.InitialThreadID(run.ID), ExpectedRunID: run.ID, OperationKey: "quality-compact-" + tc.ID + "-" + stage, RequestedBy: "quality_operator"}
					compact, err := application.NewThreadContextCompactionService(st, stageRouter).Compact(ctx, req)
					checkReceipt()
					if err != nil || !compact.Generated || compact.Status != "completed" {
						t.Fatalf("%s: %+v error=%v", stage, compact, err)
					}
					if mode == qualityInspectSources && i == 1 && tc.ToolTurn != nil {
						assertQualityDecisionInSecondSummary(t, tc, st, run, captured)
					}
					summary, found, err := st.LatestContextSummary(t.Context(), run.Session)
					if err != nil || !found || summary.PreviousSummaryID != lastSummaryID {
						t.Fatalf("%s summary chain: %+v error=%v", stage, summary, err)
					}
					lastSummaryID = summary.ID
					writeQualityArtifact(t, filepath.Join(out, stage+"-committed.json"), summary)
					if i == 0 {
						qualityDialog(t, st, router, p, run.ID, tc, 1)
						if tc.ToolTurn != nil {
							call := historyRecallOriginalCall(t, st, run.ID)
							writeQualityArtifact(t, filepath.Join(out, "historical-tool.json"), call)
						}
					}
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
					copyQualitySnapshot(t, temporary, next)
					dbPath = next
					continue
				}
				permission, err := st.GetRunExecutionPermission(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				beforeTools := historyRecallCalls(t, st, run.ID)
				turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, stageRouter, policy.NewDefaultChecker()))
				result, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: tc.Question, OperationKey: "quality-final-question-" + tc.ID, RequestedBy: "quality_operator"})
				checkReceipt()
				afterTools := historyRecallCalls(t, st, run.ID)
				toolGrade := checkQualityFinalTools(tc, beforeTools, afterTools)
				if mode != qualityInspectSources {
					writeQualityArtifact(t, filepath.Join(out, "tool-records.json"), map[string]any{"before_final_question": beforeTools, "after_final_question": afterTools})
				}
				assertHistoryRecallSettled(t, result, err)
				saved, found, err := st.LatestContextSummary(ctx, run.Session)
				if err != nil || !found || saved.ID != lastSummaryID {
					t.Fatalf("expected exactly the two manual summaries, latest id=%d error=%v", saved.ID, err)
				}
				afterPermission, err := st.GetRunExecutionPermission(ctx, run.ID)
				if err != nil || afterPermission != permission {
					t.Fatalf("the final turn changed the run's execution permission: %v", err)
				}
				if mode == qualityInspectSources {
					if toolGrade != nil {
						t.Fatal(toolGrade)
					}
					assertQualityRequestSources(t, tc, st, run, captured, archiveFits)
					return
				}
				history, err := st.ListSessionMessages(t.Context(), run.Session, true)
				if err != nil || len(history) == 0 || history[len(history)-1].Role != "assistant" {
					t.Fatalf("missing public answer: %v", err)
				}
				reply := strings.TrimSpace(history[len(history)-1].Content)
				grade := errors.Join(checkCompactionQualityAnswer(tc.Expected, reply), toolGrade)
				if trace != nil {
					trace.mu.Lock()
					for _, call := range trace.calls {
						if call.Phase == tc.ID+"-"+stage {
							calls++
						}
					}
					trace.mu.Unlock()
					t.Logf("public answer: %s", reply)
				}
				outcome := map[string]any{"case": tc.ID, "reply": reply, "correct": grade == nil, "model_calls_for_answer": calls, "summary_id": lastSummaryID}
				if grade != nil {
					outcome["failure"] = grade.Error()
				}
				writeQualityArtifact(t, filepath.Join(out, "result.json"), outcome)
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				copyQualitySnapshot(t, temporary, next)
				if grade != nil {
					t.Fatal(grade)
				}
			}
		}
		t.Run(tc.ID, func(t *testing.T) {
			if mode == qualityInspectSources && tc.ToolTurn != nil {
				t.Run("archive_fits", func(t *testing.T) { runCase(t, t.TempDir(), true) })
				t.Run("short_handoff", func(t *testing.T) { runCase(t, t.TempDir(), false) })
				return
			}
			runCase(t, dir, false)
		})
	}
}
