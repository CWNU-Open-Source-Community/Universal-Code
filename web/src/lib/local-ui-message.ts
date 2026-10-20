import type { LocaleText } from "./locale";

// Local recovery records retain their original messages. Translate these known
// interface messages only when displayed; unfamiliar server errors remain intact.
const messages: Record<string, string> = {
  "原创建请求无法可靠核对；已保留原记录，未创建或发送新消息。": "The original creation request could not be checked reliably. Its record is preserved; no new conversation or message was created.",
  "当前连接尚未提供数据存储身份，草稿仅保留在本窗口。": "This connection has no storage identity. Drafts are kept only in this window.",
  "无法读取本机恢复数据；原记录保持不变，当前编辑可能仅保留在本窗口。": "Could not read local recovery data. Original records are unchanged; current edits may exist only in this window.",
  "无法保存本机恢复数据；当前编辑可能仅保留在本窗口，刷新或退出后会丢失。": "Could not save local recovery data. Current edits may exist only in this window and be lost on reload or exit.",
  "本机恢复记录格式异常；原记录已保留，当前修改无法覆盖它。": "The local recovery record has an invalid format. It is preserved and cannot be overwritten by current changes.",
  "无法移除本机恢复记录；原记录可能仍然存在。": "Could not remove the local recovery record. The original may still exist.",
  "草稿版本记录异常；原记录已保留，未覆盖其他窗口的内容。": "The draft version record is invalid. Original records are preserved; other windows' content has not been overwritten.",
  "这版草稿尚未保存到本机；内容仍在本窗口，刷新或退出可能丢失。": "This draft has not been saved locally. It remains in this window and may be lost on reload or exit.",
  "其他窗口的草稿版本已经变化，请重新查看后选择。": "The draft version in another window has changed. Review it again before choosing.",
  "当前连接无法持久保存草稿；内容仅保留在本窗口。": "This connection cannot persist drafts. Content is kept only in this window.",
  "旧草稿暂时无法读取，请保留当前页面。": "The previous draft could not be read. Keep this page open.",
  "旧草稿记录格式异常，已保留原记录。请保留当前页面和正文，暂不能发送。": "The previous draft record has an invalid format and is preserved. Keep this page and text; sending is unavailable until resolved.",
  "草稿已在其他窗口更新，请查看当前版本后再发送。": "The draft was updated in another window. Review the current version before sending.",
  "草稿尚未可靠保存，请保留当前页面后重试。": "The draft has not been saved reliably. Keep this page open and retry.",
  "草稿在提交前发生变化，请查看当前版本后再发送。原消息标识和新草稿均已保留。": "The draft changed before submission. Review the current version before sending. The original message identity and new draft are kept.",
  "编辑稿尚未保存，请保留本页。": "The edit has not been saved. Keep this page open.",
  "排队消息的本机恢复记录异常，原记录已保留；请保留当前编辑内容。": "The queued message's local recovery record is invalid and is preserved. Keep your current edit.",
  "原操作内容已变化，不能覆盖后重发。": "The original operation content has changed. It cannot be overwritten and resent.",
  "提交结果已确认，但草稿清理尚未保存。原请求标识已保留。": "The submission result is confirmed, but draft cleanup has not been saved. The original request identity is kept.",
  "原草稿清理尚未保存。": "Cleanup of the original draft has not been saved.",
  "消息发送失败": "Message could not be sent",
  "上次退出时提交结果尚待确认。请使用“重试核对”查看原提交记录。": "The submission result was unconfirmed when you last exited. Use Retry check to read the original submission record.",
  "文件附件的项目或保存身份无效，未发送消息。": "The attachment's project or saved identity is invalid. No message was sent.",
  "MCP 引用身份无效，请重新选择。": "The MCP reference identity is invalid. Select it again.",
  "原提交记录内容异常，未发送新消息。原数据已保留。": "The original submission record is invalid. No new message was sent; original data is preserved.",
  "提交内容无法可靠保存，请检查原消息与文件引用。": "The submission cannot be saved reliably. Check the original message and file references.",
  "原提交标识已经绑定其他内容，未发送消息。": "The original submission identity is already bound to other content. No message was sent.",
  "更新当前任务仅支持文字；附件和引用仍保留在草稿中。": "Updating the current task supports text only. Attachments and references remain in the draft.",
  "工作目录请求尚未保存，请保留当前页面后重试。": "The working directory request has not been saved. Keep this page open and retry.",
  "无法准备聊天工作目录。": "Could not prepare the conversation's working directory.",
  "保存的工作目录请求无法读取，原记录已保留。": "The saved working directory request could not be read. The original record is preserved.",
  "最新工作记录存在未读取的间隔，请重试同步。": "There is an unread gap in the latest work records. Retry syncing.",
  "工作记录分页游标未推进，已有记录已保留。": "Work record pagination did not advance. Existing records are kept.",
};

export function localUIMessage(message: string, t: LocaleText): string {
  return Object.hasOwn(messages, message) ? t(message, messages[message]) : message;
}
