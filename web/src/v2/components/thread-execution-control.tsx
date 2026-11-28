import { useMutation, useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { ThreadExecutionView } from "../../api/types";
import { v2QueryKeys } from "../query-keys";
import { useLocale } from "../../lib/locale";

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

export function useV2ThreadStop(client: APIClient, threadID: string, execution: ThreadExecutionView | undefined) {
  const queryClient = useQueryClient();
  const executionID = execution?.execution_id;
  const mutationKey = ["v2", "thread-stop", threadID, executionID];
  const status = useMutationState({ filters: { mutationKey, exact: true },
    select: (mutation) => mutation.state.status }).at(-1);
  const stop = useMutation({
    mutationKey,
    mutationFn: (input: { threadID: string; executionID: string }) => client.interruptThread(
      input.threadID, input.executionID, `v2-thread-stop-${input.executionID}`),
    // Navigation can change the observer's props while a stop is pending.
    // Its response and refresh still belong to the submitted task.
    onSuccess: (state, input) => queryClient.setQueryData(v2QueryKeys.execution(input.threadID), state),
    onSettled: (_data, _error, input) => queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(input.threadID) }),
  });
  if (!executionID || execution?.thread_id !== threadID || execution.state === "idle" ||
      !client.hasThreadControl || !client.hasRunExecution) return;
  const state = status === "pending" ? "stopping" : execution.state;
  return {
    state,
    unconfirmed: status === "error",
    request: () => {
      if (state === "stopping" || queryClient.isMutating({ mutationKey, exact: true })) return false;
      stop.mutate({ threadID, executionID });
      return true;
    },
  } as const;
}

const resumeMutationKey = ["v2", "thread-resume"] as const;
type ResumeInput = { threadID: string; runID: string; operationKey: string };

export function V2PausedThreadControl({ client, threadID, runID }: {
  client: APIClient; threadID: string; runID: string;
}) {
  const { t } = useLocale();
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
  return <div className="v2-paused-thread" role="status"><span>{t("发送消息即可继续，也可以仅解除暂停。", "Send a message to continue, or simply unpause the run.")}</span>
    {client.hasRunLifecycle && <button disabled={pending} onClick={submit}
      title={t("恢复运行状态；发送新消息后继续处理要求，失败的操作需另行发起", "Unpause the run; send a message to continue. Failed actions must be requested again")} type="button">
      {pending ? t("正在解除…", "Unpausing…") : t("解除暂停", "Unpause")}</button>}
    {attempt?.status === "error" && <p role="alert">{t("解除暂停未确认，可重试：", "Unpause is unconfirmed. You can retry: ")}{attempt.error?.message}</p>}
  </div>;
}
