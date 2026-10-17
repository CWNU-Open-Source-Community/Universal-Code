import { useMutation, useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Square } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { ThreadExecutionView } from "../../api/types";
import { v2QueryKeys } from "../query-keys";

export function useV2ThreadExecution(client: APIClient, threadID: string) {
  return useQuery({
    queryKey: v2QueryKeys.execution(threadID),
    queryFn: ({ signal }) => client.threadExecution(threadID, signal),
    enabled: Boolean(threadID) && client.hasThreadExecutionRead === true,
    refetchInterval: (query) => query.state.error instanceof APIRequestError &&
      query.state.error.status === 404 ? false : query.state.data?.state === "idle" ? 2_500 : 600,
    retry: false,
  });
}

export function V2ThreadExecutionControl({ client, threadID, execution }: {
  client: APIClient;
  threadID: string;
  execution: ThreadExecutionView | undefined;
}) {
  const queryClient = useQueryClient();
  const stop = useMutation({
    mutationKey: ["v2", "thread-stop", threadID],
    mutationFn: (input: { threadID: string; executionID: string }) => client.interruptThread(
      input.threadID, input.executionID, `v2-thread-stop-${input.executionID}`),
    // Navigation can change the observer's props while a stop is pending.
    // Its response and refresh still belong to the submitted task.
    onSuccess: (state, input) => queryClient.setQueryData(v2QueryKeys.execution(input.threadID), state),
    onSettled: (_data, _error, input) => queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(input.threadID) }),
  });
  const stopping = stop.isPending || execution?.state === "stopping";
  return <>
    {execution && execution.state !== "idle" && client.hasThreadControl && client.hasRunExecution &&
      <button aria-label={stopping ? "正在停止" : execution.state === "stop_failed" ? "重试停止" : "停止当前执行"} className="v2-stop-button"
        disabled={stopping} onClick={() => execution.execution_id && stop.mutate({ threadID,
          executionID: execution.execution_id })}
        title="停止当前执行；已完成的修改和已受理的补充要求会保留" type="button">
        {stopping ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
          : <Square aria-hidden="true" size={13} />} {stopping ? "正在停止…" : execution.state === "stop_failed" ? "重试停止" : "停止"}
      </button>}
    {stop.isError && <span role="alert">停止请求未确认，请刷新执行状态后重试。</span>}
  </>;
}

const resumeMutationKey = ["v2", "thread-resume"] as const;
type ResumeInput = { threadID: string; runID: string; operationKey: string };

export function V2PausedThreadControl({ client, threadID, runID }: {
  client: APIClient; threadID: string; runID: string;
}) {
  const queryClient = useQueryClient();
  const mutationKey = [...resumeMutationKey, threadID, runID] as const;
  const attempts = useMutationState({
    filters: { mutationKey: resumeMutationKey },
    select: (mutation) => ({ status: mutation.state.status, error: mutation.state.error,
      input: mutation.state.variables as ResumeInput | undefined }),
  });
  // Select the current identity during render so navigation cannot briefly
  // display the previous observer's pending state or error.
  const attempt = attempts.filter(({ input }) => input?.threadID === threadID && input.runID === runID).at(-1);
  const resume = useMutation({
    mutationKey,
    // An unresolved request and its retry identity survive view changes for
    // the same lifetime as the QueryClient, independently for each Thread/Run.
    gcTime: Infinity,
    retry: false,
    mutationFn: (input: ResumeInput) => client.controlRunLifecycle(input.runID,
      { version: "run_lifecycle_control.v1", action: "resume" }, input.operationKey),
    onMutate: (input) => {
      // A retry carries the original key in its own variables. Keep only the
      // current attempt after it has taken ownership of that identity.
      for (const previous of queryClient.getMutationCache().findAll({
        mutationKey: [...resumeMutationKey, input.threadID, input.runID], exact: true,
      })) {
        if (previous.state.status !== "pending") queryClient.getMutationCache().remove(previous);
      }
    },
    onSettled: (_data, _error, input) => queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(input.threadID) }),
  });
  const pending = attempt?.status === "pending";
  const submit = () => {
    const current = queryClient.getMutationCache().findAll({ mutationKey, exact: true }).at(-1);
    if (!client.hasRunLifecycle || current?.state.status === "pending") return;
    const original = current?.state.variables as ResumeInput | undefined;
    resume.mutate({ threadID, runID, operationKey: current?.state.status === "error" && original
      ? original.operationKey : `v2-resume-${globalThis.crypto.randomUUID()}` });
  };
  return <div className="v2-paused-thread" role="status"><span>发送消息即可继续，也可以仅解除暂停。</span>
    {client.hasRunLifecycle && <button disabled={pending} onClick={submit}
      title="恢复运行状态；发送新消息后继续处理要求，失败的操作需另行发起" type="button">
      {pending ? "正在解除…" : "解除暂停"}</button>}
    {attempt?.status === "error" && <p role="alert">解除暂停未确认，可重试：{attempt.error?.message}</p>}
  </div>;
}
