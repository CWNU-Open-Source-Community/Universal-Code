import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { render as renderComponent, screen } from "@testing-library/react";
import type { ThreadRunRecoveryView } from "../../api/types";
import { V2ThreadRunRecovery } from "./thread-run-recovery";

const recovery: ThreadRunRecoveryView = {
  version: "thread_run_recovery.v1",
  run_id: "run-old",
  handoff_operation_id: "run-handoff-failed",
  error_code: "failed_precondition",
  stop_reason: "failed_precondition",
  detail: "上一次执行因执行条件未满足而停止，具体原因请查看错误或执行记录。确认问题已处理后，可发送下一条消息继续。",
  quiescent: true,
  failed_at: "2026-09-01T00:43:30Z",
};

describe("V2ThreadRunRecovery", () => {
  it("invites the next user message without exposing Run recovery choices", () => {
    render(<V2ThreadRunRecovery recovery={recovery} />);

    expect(screen.getByText("本轮执行已停止，对话仍可继续")).toBeInTheDocument();
    expect(screen.getByText(recovery.detail)).toBeInTheDocument();
    expect(screen.getByText(/可以直接发送“继续”或补充要求/)).toHaveTextContent(/系统会先核对，再发送新要求/);
    expect(screen.queryByText(/新的执行上下文|收束上一次执行|请先点击/)).not.toBeInTheDocument();
    expect(screen.queryByText(/固定权限|固定模型|模型或运行配置已不再适用/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("keeps the composer guidance non-destructive while resources are releasing", () => {
    render(<V2ThreadRunRecovery recovery={{ ...recovery, quiescent: false }} />);
    expect(screen.getByText(/你可以先编辑消息/)).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
