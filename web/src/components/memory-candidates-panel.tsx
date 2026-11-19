import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import type { MemoryCandidateReviewView, MemoryCandidateView } from "../api/types";
import { formatDate, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { ErrorState, LoadingState } from "./common";

export function MemoryCandidatesPanel({ client, workspaceID, onOpenRun }: {
  client: APIClient;
  workspaceID: string;
  onOpenRun?: (runID: string) => void;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const queryKey = ["memory-candidates", workspaceID] as const;
  const candidates = useQuery({
    queryKey,
    queryFn: ({ signal }) => client.get<MemoryCandidateView[]>("/memory-candidates", {
      workspace_id: workspaceID, include_resolved: false, limit: 50,
    }, signal),
    enabled: Boolean(workspaceID),
  });
  const review = useMutation({
    mutationFn: ({ candidate, confirm }: { candidate: MemoryCandidateView; confirm: boolean }) =>
      client.postControl<MemoryCandidateReviewView>(
        `/memory-candidates/${encodeURIComponent(candidate.id)}/${confirm ? "confirm" : "reject"}`, {
          workspace_id: workspaceID,
          expected_version: candidate.version,
          expected_fingerprint: candidate.fingerprint,
          expected_source_version: candidate.source.note_version,
          ...(confirm ? { confirm: true } : {}),
        }, `web-memory-candidate-${globalThis.crypto.randomUUID()}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey });
      void queryClient.invalidateQueries({ queryKey: ["context-memories", "project", workspaceID] });
    },
    onError: () => {
      // A source may have changed since it was displayed. Refresh for another
      // explicit review; never resubmit the operator's old confirmation.
      void queryClient.invalidateQueries({ queryKey });
    },
  });

  const pending = (candidates.data ?? []).filter((candidate) =>
    candidate.status === "pending" && candidate.workspace_id === workspaceID);
  return <section className="context-memory-candidates" aria-label={t("待确认经验", "Experience candidates")}>
    <header className="context-section-header">
      <div><span><strong>{t("待确认经验", "Experience candidates")}</strong>
      </span></div>
      <button type="button" className="compact-command" disabled={candidates.isFetching || review.isPending}
        onClick={() => { review.reset(); void candidates.refetch(); }}>
        <RefreshCw aria-hidden="true" size={13} />{t("刷新候选", "Refresh candidates")}
      </button>
    </header>
    {review.isError && <ErrorState error={review.error} />}
    {candidates.isLoading ? <LoadingState label={t("加载候选经验", "Loading experience candidates")} /> :
      candidates.isError ? <ErrorState error={candidates.error} /> :
        pending.length === 0 ? <p className="context-empty">{t("当前没有待确认经验。", "No pending experience candidates.")}</p> :
          <div className="context-memory-list">
            {pending.map((candidate) => <article key={candidate.id}>
              <details>
                <summary><strong>{candidate.title}</strong>
                  <span>{t("待核实", "Needs review")} · {formatDate(candidate.created_at)}</span></summary>
                <p className="context-candidate-content">{candidate.content}</p>
                <dl className="context-candidate-source">
                  <dt>{t("来源任务", "Source run")}</dt><dd>{onOpenRun ?
                    <button className="compact-command" type="button" onClick={() => onOpenRun(candidate.source.run_id)}>
                      {shortID(candidate.source.run_id)}</button> : <code>{candidate.source.run_id}</code>}</dd>
                  <dt>{t("来源笔记", "Source note")}</dt><dd><code>{candidate.source.note_id}</code> · v{candidate.source.note_version}</dd>
                  <dt>{t("完成记录", "Completion record")}</dt><dd><code>{candidate.source.completion_ref}</code></dd>
                  {(candidate.source.references?.length ?? 0) > 0 && <>
                    <dt>{t("引用", "References")}</dt><dd>{candidate.source.references?.join(", ")}</dd>
                  </>}
                  {(candidate.source.evidence_ids?.length ?? 0) > 0 && <>
                    <dt>{t("工具证据", "Tool evidence")}</dt><dd>{candidate.source.evidence_ids?.join(", ")}</dd>
                  </>}
                </dl>
                {candidate.redacted && <p className="context-empty">{t("敏感值已脱敏。", "Sensitive values have been redacted.")}</p>}
                <div className="context-row-actions">
                  <button className="compact-command" type="button" disabled={!client.hasControl || review.isPending}
                    onClick={() => review.mutate({ candidate, confirm: true })}>
                    {t("确认并保存为项目记忆", "Save as project memory")}</button>
                  <button className="compact-command" type="button" disabled={!client.hasControl || review.isPending}
                    onClick={() => review.mutate({ candidate, confirm: false })}>
                    {t("忽略此候选", "Dismiss candidate")}</button>
                </div>
              </details>
            </article>)}
          </div>}
    {pending.length === 50 && <p className="context-empty">{t("当前展示最多 50 条；处理后刷新以查看其余候选。", "Showing up to 50 candidates. Review these and refresh to see more.")}</p>}
  </section>;
}
