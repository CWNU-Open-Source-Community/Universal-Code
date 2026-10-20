import { useLocale } from "../../lib/locale";
import type { ThreadRunRecoveryView } from "../../api/types";

export type ContextDiagnostic = {
  sequence: number; occurred_at: string; phase: string; attempt_id: string; source_sha256: string;
  model_attempt?: number; summary_id?: number; generated?: boolean; removed_messages?: number; fallback_code?: string;
};
export type ContextDiagnostics = { records: ContextDiagnostic[]; truncated: boolean };

const phases: Record<string, { title: [string, string]; detail: [string, string] }> = {
  generation_started: { title: ["已开始生成摘要", "Summary generation started"], detail: ["这条记录只确认生成已开始；摘要是否保存，以后续记录为准。", "This record confirms generation started. Later records show whether the summary was saved."] },
  generation_received: { title: ["已收到生成结果", "Generation result received"], detail: ["模型结果已记录，尚不能据此确认摘要已保存。", "The model result is recorded. This does not yet confirm a saved summary."] },
  generation_rejected: { title: ["生成结果未通过检查", "Generation result failed validation"], detail: ["结果或用量记录未通过检查；后续是否采用规则摘要，见保存记录。", "The result or usage record failed validation. Check saved records for a rule-based summary fallback."] },
  generation_failed: { title: ["生成请求未成功", "Generation request failed"], detail: ["生成调用已结束；是否采用规则摘要，见后续保存记录。", "The generation call ended. Later saved records show whether a rule-based summary was used."] },
  summary_saved: { title: ["压缩摘要已保存", "Compaction summary saved"], detail: ["摘要与消息归纳记录已持久保存，可在下方查看已保存摘要。", "The summary and compaction record are saved. View the saved summary below."] },
};
const fallbackLabels: Record<string, [string, string]> = {
  generation_cost_budget: ["生成预算或费用记录不可用", "Generation budget or cost record unavailable"],
  generation_protocol_repair: ["当前轮次正在修复模型响应格式", "The current turn is repairing the model response format"],
  generation_provider_failure: ["模型服务调用失败", "Model provider request failed"],
  generation_invalid_response: ["生成结果格式不符合要求", "Generated result has an invalid format"],
  generation_input_data: ["无法准备摘要所需的输入记录", "Could not prepare the summary input"],
  generation_input_window: ["摘要输入超过当前模型可用的上下文窗口", "Summary input exceeds the model's available context window"],
  generation_token_budget: ["本次摘要生成可用的 token 预算不足", "Insufficient token budget for this summary"],
  unclassified: ["已记录回退，具体原因尚未分类", "Fallback recorded; reason not classified yet"],
};

export function validContextDiagnostics(value: unknown): value is ContextDiagnostics {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const view = value as Partial<ContextDiagnostics>;
  if (typeof view.truncated !== "boolean" || !Array.isArray(view.records) || view.records.length > 40) return false;
  let previous = Number.POSITIVE_INFINITY;
  return view.records.every((item) => {
    if (!item || typeof item !== "object" || Array.isArray(item) ||
      !Number.isSafeInteger(item.sequence) || item.sequence <= 0 || item.sequence >= previous ||
      typeof item.phase !== "string" || !Object.hasOwn(phases, item.phase) ||
      typeof item.attempt_id !== "string" || !item.attempt_id.trim() ||
      typeof item.source_sha256 !== "string" || !/^[a-f0-9]{64}$/u.test(item.source_sha256) ||
      typeof item.occurred_at !== "string" || !Number.isFinite(Date.parse(item.occurred_at)) ||
      (item.fallback_code !== undefined && (typeof item.fallback_code !== "string" || !Object.hasOwn(fallbackLabels, item.fallback_code)))) return false;
    previous = item.sequence;
    if (item.phase === "summary_saved") return Number.isSafeInteger(item.summary_id) && Number(item.summary_id) > 0 &&
      typeof item.generated === "boolean" && Number.isSafeInteger(item.removed_messages) && Number(item.removed_messages) > 0;
    return Number.isSafeInteger(item.model_attempt) && Number(item.model_attempt) > 0 &&
      item.generated === undefined && item.summary_id === undefined && item.fallback_code === undefined;
  });
}

export function ContextDiagnosticsPanel({ value, unavailable, recovery, runID, runStatus }: {
  value?: ContextDiagnostics; unavailable?: boolean; recovery?: ThreadRunRecoveryView; runID: string; runStatus: string;
}) {
  const { t, locale } = useLocale();
  const latest = value?.records[0];
  const matchingRecovery = recovery?.run_id === runID ? recovery : undefined;
  return <section aria-labelledby="context-diagnostics-title">
    <h3 id="context-diagnostics-title">{t("压缩与恢复进展", "Compaction and recovery progress")}</h3>
    {unavailable ? <p role="status">{t("压缩过程记录暂不可读取；下方已保存摘要仍可查看，请稍后刷新重试。", "Compaction records are temporarily unavailable. Saved summaries below remain available; refresh to retry.")}</p> : value === undefined ? <p>{t("当前服务未提供压缩过程记录，仍可查看下方已保存摘要。", "This service does not provide compaction progress records. Saved summaries are available below.")}</p> : !latest ?
      <p>{t("此执行尚无自动生成压缩记录。手动压缩与历史摘要可在下方查看。", "No automatic compaction records for this run. Manual compactions and past summaries are shown below.")}</p> : <>
        <div className="v2-context-notice" role="status"><strong>{t("最近记录：", "Latest record: ")}{t(...phases[latest.phase].title)}</strong><p>{t(...phases[latest.phase].detail)}</p>
          {latest.fallback_code && <p>{t("已采用规则摘要回退：", "Rule-based summary fallback used: ")}{t(...fallbackLabels[latest.fallback_code])}。</p>}
          {latest.phase === "generation_started" && runStatus !== "running" && <p>{t("当前执行未在运行，最近显示的是生成开始记录。请刷新查看后续记录。", "This run is no longer running; the latest record says generation started. Refresh to check later records.")}</p>}
        </div>
        <details><summary>{t(`查看压缩过程（最近 ${value.records.length} 条）`, `View compaction progress (latest ${value.records.length} records)`)}</summary>
          <ol className="v2-context-diagnostics-list">{value.records.map((item) => <li key={item.sequence}>
            <strong>{t(...phases[item.phase].title)}</strong><time dateTime={item.occurred_at}>{new Date(item.occurred_at).toLocaleString(locale)}</time>
            {item.phase === "summary_saved" && <p>{item.generated ? t("采用模型生成摘要", "Applied a model-generated summary") : item.fallback_code ? t("采用规则摘要回退", "Applied a rule-based fallback summary") : t("采用规则摘要或复用已保存摘要", "Applied a rule-based or previously saved summary")}{t(`，本次归纳 ${item.removed_messages} 条消息。`, `; compacted ${item.removed_messages} messages.`)}</p>}
            {item.fallback_code && <p>{t("回退原因：", "Fallback reason: ")}{t(...fallbackLabels[item.fallback_code])}</p>}
            <details><summary>{t("记录来源", "Record source")}</summary><dl><dt>{t("事件序号", "Event sequence")}</dt><dd>{item.sequence}</dd><dt>{t("执行尝试", "Run attempt")}</dt><dd>{item.attempt_id}</dd>
              <dt>{t("来源 SHA256", "Source SHA256")}</dt><dd>{item.source_sha256}</dd>{item.summary_id && <><dt>{t("摘要 ID", "Summary ID")}</dt><dd>{item.summary_id}</dd></>}
              {item.model_attempt && <><dt>{t("模型尝试", "Model attempt")}</dt><dd>{item.model_attempt}</dd></>}</dl></details>
          </li>)}</ol>
          {value.truncated && <p>{t("这里只展示最近 40 条压缩记录，较早记录仍保留在运行历史中。", "Only the latest 40 compaction records are shown. Earlier records remain in run history.")}</p>}
        </details>
      </>}
    <h4>{t("执行恢复", "Run recovery")}</h4>
    {matchingRecovery ? <div className="v2-context-notice"><strong>{matchingRecovery.quiescent ? t("上轮已停止，可继续对话", "The previous turn stopped; you can continue") : t("上轮正在释放资源", "The previous turn is releasing resources")}</strong>
      <p>{matchingRecovery.detail}</p><p>{matchingRecovery.quiescent ? t("返回对话发送“继续”或补充要求。上次提交若尚未确认，系统会先核对。", "Return to the conversation and send “continue” or further instructions. Any unconfirmed previous submission is checked first.") : t("可以先编辑消息；资源释放后再继续。", "You can edit your message now and continue after resources are released.")}</p>
    </div> : <p>{t("当前执行没有待恢复提示。", "There is no pending recovery notice for this run.")}</p>}
    <p className="v2-context-help">{t("消息计数记录每次压缩的范围，已保存摘要可在下方查看。需要继续执行时，返回对话发送消息。", "Message counts describe each compaction. View saved summaries below; return to the conversation to continue.")}</p>
  </section>;
}
