import type { ThreadRunRecoveryView } from "../../api/types";

export type ContextDiagnostic = {
  sequence: number; occurred_at: string; phase: string; attempt_id: string; source_sha256: string;
  model_attempt?: number; summary_id?: number; generated?: boolean; removed_messages?: number; fallback_code?: string;
};
export type ContextDiagnostics = { records: ContextDiagnostic[]; truncated: boolean };

const phases: Record<string, { title: string; detail: string }> = {
  generation_started: { title: "已开始生成摘要", detail: "这条记录只确认生成已开始；摘要是否保存，以后续记录为准。" },
  generation_received: { title: "已收到生成结果", detail: "模型结果已记录，尚不能据此确认摘要已保存。" },
  generation_rejected: { title: "生成结果未通过检查", detail: "结果或用量记录未通过检查；后续是否采用规则摘要，见保存记录。" },
  generation_failed: { title: "生成请求未成功", detail: "生成调用已结束；是否采用规则摘要，见后续保存记录。" },
  summary_saved: { title: "压缩摘要已保存", detail: "摘要与消息归纳记录已持久保存，可在下方查看已保存摘要。" },
};
const fallbackLabels: Record<string, string> = {
  generation_cost_budget: "生成预算或费用记录不可用",
  generation_protocol_repair: "当前轮次正在修复模型响应格式",
  generation_provider_failure: "模型服务调用失败",
  generation_invalid_response: "生成结果格式不符合要求",
  generation_input_data: "无法准备摘要所需的输入记录",
  generation_input_window: "摘要输入超过当前模型可用的上下文窗口",
  generation_token_budget: "本次摘要生成可用的 token 预算不足",
  unclassified: "已记录回退，具体原因尚未分类",
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
  const latest = value?.records[0];
  const matchingRecovery = recovery?.run_id === runID ? recovery : undefined;
  return <section aria-labelledby="context-diagnostics-title">
    <h3 id="context-diagnostics-title">压缩与恢复进展</h3>
    {unavailable ? <p role="status">压缩过程记录暂不可读取；下方已保存摘要仍可查看，请稍后刷新重试。</p> : value === undefined ? <p>当前服务未提供压缩过程记录，仍可查看下方已保存摘要。</p> : !latest ?
      <p>此执行尚无自动生成压缩记录。手动压缩与历史摘要可在下方查看。</p> : <>
        <div className="v2-context-notice" role="status"><strong>最近记录：{phases[latest.phase].title}</strong><p>{phases[latest.phase].detail}</p>
          {latest.fallback_code && <p>已采用规则摘要回退：{fallbackLabels[latest.fallback_code]}。</p>}
          {latest.phase === "generation_started" && runStatus !== "running" && <p>当前执行未在运行，最近显示的是生成开始记录。请刷新查看后续记录。</p>}
        </div>
        <details><summary>查看压缩过程（最近 {value.records.length} 条）</summary>
          <ol className="v2-context-diagnostics-list">{value.records.map((item) => <li key={item.sequence}>
            <strong>{phases[item.phase].title}</strong><time dateTime={item.occurred_at}>{new Date(item.occurred_at).toLocaleString()}</time>
            {item.phase === "summary_saved" && <p>{item.generated ? "采用模型生成摘要" : item.fallback_code ? "采用规则摘要回退" : "采用规则摘要或复用已保存摘要"}，本次归纳 {item.removed_messages} 条消息。</p>}
            {item.fallback_code && <p>回退原因：{fallbackLabels[item.fallback_code]}</p>}
            <details><summary>记录来源</summary><dl><dt>事件序号</dt><dd>{item.sequence}</dd><dt>执行尝试</dt><dd>{item.attempt_id}</dd>
              <dt>来源 SHA256</dt><dd>{item.source_sha256}</dd>{item.summary_id && <><dt>摘要 ID</dt><dd>{item.summary_id}</dd></>}
              {item.model_attempt && <><dt>模型尝试</dt><dd>{item.model_attempt}</dd></>}</dl></details>
          </li>)}</ol>
          {value.truncated && <p>这里只展示最近 40 条压缩记录，较早记录仍保留在运行历史中。</p>}
        </details>
      </>}
    <h4>执行恢复</h4>
    {matchingRecovery ? <div className="v2-context-notice"><strong>{matchingRecovery.quiescent ? "上轮已停止，可继续对话" : "上轮正在释放资源"}</strong>
      <p>{matchingRecovery.detail}</p><p>{matchingRecovery.quiescent ? "返回对话发送“继续”或补充要求。上次提交若尚未确认，系统会先核对。" : "可以先编辑消息；资源释放后再继续。"}</p>
    </div> : <p>当前执行没有待恢复提示。</p>}
    <p className="v2-context-help">消息计数记录每次压缩的范围，已保存摘要可在下方查看。需要继续执行时，返回对话发送消息。</p>
  </section>;
}
