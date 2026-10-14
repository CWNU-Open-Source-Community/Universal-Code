import { useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ClipboardCheck, Link2, LoaderCircle, RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import type { VerificationEvidenceRequestView } from "../api/types";
import { formatDate } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, LoadingState, StatusBadge } from "./common";

export function VerificationEvidence({ client, runID }: {
  client: APIClient;
  runID: string;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const operationKey = useRef("");
  const associationKeys = useRef(new Map<string, { intent: string; key: string }>());
  const [outcome, setOutcome] = useState<VerificationEvidenceRequestView["outcome"]>("unknown");
  const [title, setTitle] = useState("");
  const [summary, setSummary] = useState("");
  const [associationTargets, setAssociationTargets] = useState<Record<string, string>>({});
  const query = useQuery({
    queryKey: ["run", runID, "verification-evidence"],
    queryFn: ({ signal }) => client.verificationEvidence(runID, signal),
    enabled: Boolean(runID),
  });
  const plansQuery = useQuery({
    queryKey: ["run", runID, "verification-plan"],
    queryFn: ({ signal }) => client.verificationPlans(runID, signal),
    enabled: Boolean(runID),
  });
  const coverageQuery = useQuery({
    queryKey: ["run", runID, "verification-plan-coverage"],
    queryFn: ({ signal }) => client.verificationPlanCoverage(runID, signal),
    enabled: Boolean(runID),
  });
  const record = useMutation({
    mutationFn: (body: VerificationEvidenceRequestView) => {
      if (!operationKey.current) {
        operationKey.current = `web-verification-${globalThis.crypto.randomUUID()}`;
      }
      return client.recordVerificationEvidence(runID, body, operationKey.current);
    },
    onSuccess: () => {
      operationKey.current = "";
      setTitle("");
      setSummary("");
      setOutcome("unknown");
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "verification-evidence"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "verification-plan-coverage"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "code-handoff"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "events"] });
    },
  });
  const associate = useMutation({
    mutationFn: ({ evidenceID, planID, ordinal }: {
      evidenceID: string; planID: string; ordinal: number;
    }) => {
      const intent = `${evidenceID}:${planID}:${ordinal}`;
      let operation = associationKeys.current.get(evidenceID);
      if (!operation || operation.intent !== intent) {
        operation = { intent, key: `web-verification-association-${globalThis.crypto.randomUUID()}` };
        associationKeys.current.set(evidenceID, operation);
      }
      return client.associateVerificationEvidence(runID, {
        version: "operator_verification_plan_evidence_association.v1",
        plan_id: planID, plan_item_ordinal: ordinal, evidence_id: evidenceID,
      }, operation.key);
    },
    onSuccess: (_, input) => {
      associationKeys.current.delete(input.evidenceID);
      setAssociationTargets((current) => {
        const next = { ...current };
        delete next[input.evidenceID];
        return next;
      });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "verification-plan-coverage"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "code-handoff"] });
      void queryClient.invalidateQueries({ queryKey: ["run", runID, "events"] });
    },
  });
  const changeIntent = (change: () => void) => {
    operationKey.current = "";
    change();
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const normalizedTitle = title.trim();
    const normalizedSummary = summary.trim();
    if (!normalizedTitle || !normalizedSummary || record.isPending) return;
    record.mutate({ version: "operator_verification_evidence.v1", outcome,
      title: normalizedTitle, summary: normalizedSummary });
  };
  const planOptions = plansQuery.data?.items.flatMap((plan) => plan.items.map((item) => ({
    key: JSON.stringify([plan.id, item.ordinal]), planID: plan.id, ordinal: item.ordinal,
    label: `${plan.title} / ${t("检查项", "Check")} ${item.ordinal}: ${item.title}`,
  }))) ?? [];
  const associationsByEvidence = new Map(coverageQuery.data?.associations.map((association) =>
    [association.evidence_id, association]) ?? []);
  const planTitles = new Map(plansQuery.data?.items.map((plan) => [plan.id, plan.title]) ?? []);
  return <section aria-label={t("验证证据", "Verification evidence")} className="verification-evidence">
    <header className="operator-list-header">
      <div><ClipboardCheck aria-hidden="true" size={16} /><h2>{t("验证", "Verification")}</h2></div>
      <div>
        {query.data && <>
          <StatusBadge status={`${query.data.pass_count} pass`} />
          {query.data.fail_count > 0 && <StatusBadge status={`${query.data.fail_count} fail`} />}
          {coverageQuery.data && <StatusBadge status={`${coverageQuery.data.associated_evidence_count} linked`} />}
        </>}
        <button aria-label={t("刷新验证证据", "Refresh verification evidence")} className="icon-button"
          disabled={query.isFetching || plansQuery.isFetching || coverageQuery.isFetching}
          onClick={() => {
            void query.refetch();
            void plansQuery.refetch();
            void coverageQuery.refetch();
          }}
          title={t("刷新", "Refresh")} type="button">
          <RefreshCw aria-hidden="true"
            className={query.isFetching || plansQuery.isFetching || coverageQuery.isFetching ? "spin" : ""}
            size={15} />
        </button>
      </div>
    </header>
    {client.hasVerificationEvidence && <form className="verification-form" onSubmit={submit}>
      <label>{t("结果", "Result")}<select aria-label={t("验证结果", "Verification result")} value={outcome} disabled={record.isPending}
        onChange={(event) => changeIntent(() =>
          setOutcome(event.target.value as VerificationEvidenceRequestView["outcome"]))}>
        <option value="pass">{t("通过", "Pass")}</option><option value="fail">{t("失败", "Fail")}</option>
        <option value="unknown">{t("未知", "Unknown")}</option>
      </select></label>
      <label>{t("标题", "Title")}<input maxLength={160} required value={title} disabled={record.isPending}
        onChange={(event) => changeIntent(() => setTitle(event.target.value))} /></label>
      <label className="verification-summary">{t("摘要", "Summary")}<textarea maxLength={2048} required rows={3}
        value={summary} disabled={record.isPending}
        onChange={(event) => changeIntent(() => setSummary(event.target.value))} /></label>
      <button className="command-button" disabled={record.isPending || !title.trim() || !summary.trim()}
        type="submit">{record.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} /> :
          <ClipboardCheck aria-hidden="true" size={15} />}{t("记录", "Record")}</button>
    </form>}
    {record.error && <ErrorState error={record.error} />}
    {associate.error && <ErrorState error={associate.error} />}
    {query.isLoading && <LoadingState label={t("正在加载验证证据", "Loading verification evidence")} />}
    {query.isError && <ErrorState error={query.error} />}
    {plansQuery.isError && <ErrorState error={plansQuery.error} />}
    {coverageQuery.isError && <ErrorState error={coverageQuery.error} />}
    {query.data?.items.length === 0 && <EmptyState>{t("尚未记录验证证据", "No verification evidence recorded")}</EmptyState>}
    {query.data && query.data.items.length > 0 && <div className="verification-list">
      {query.data.items.map((item) => {
        const linked = associationsByEvidence.get(item.id);
        const selected = associationTargets[item.id] ?? "";
        const target = planOptions.find((option) => option.key === selected);
        return <article key={item.id}>
        <header><StatusBadge status={item.outcome} /><strong>{item.title}</strong>
          <time dateTime={item.recorded_at}>{formatDate(item.recorded_at)}</time></header>
        <p>{item.summary}</p>
        {linked ? <div className="verification-association-state">
          <Link2 aria-hidden="true" size={13} /><span>{planTitles.get(linked.plan_id) ?? linked.plan_id}</span>
          <StatusBadge status={`check ${linked.plan_item_ordinal}`} />
        </div> : client.hasVerificationEvidence && planOptions.length > 0 &&
        <div className="verification-association-controls">
          <select aria-label={t(`${item.title} 的计划检查项`, `Plan item for ${item.title}`)} disabled={associate.isPending} value={selected}
            onChange={(event) => {
              associationKeys.current.delete(item.id);
              setAssociationTargets((current) => ({ ...current, [item.id]: event.target.value }));
            }}>
            <option value="">{t("选择计划检查项", "Select plan check")}</option>
            {planOptions.map((option) => <option key={option.key} value={option.key}>{option.label}</option>)}
          </select>
          <button aria-label={t(`关联 ${item.title}`, `Associate ${item.title}`)} className="compact-command"
            disabled={!target || associate.isPending} onClick={() => target && associate.mutate({
              evidenceID: item.id, planID: target.planID, ordinal: target.ordinal,
            })} type="button">
            {associate.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> :
              <Link2 aria-hidden="true" size={14} />}{t("关联", "Associate")}</button>
        </div>}
      </article>;
      })}
    </div>}
  </section>;
}
