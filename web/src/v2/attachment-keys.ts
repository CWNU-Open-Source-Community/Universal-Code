export const v2AttachmentReferenceKey = (workspaceID: string, threadID: string) =>
  ["v2", "draft-attachments", workspaceID, threadID] as const;
export const v2AttachmentImportKey = (workspaceID: string, threadID: string) =>
  ["v2", "attachment-import", workspaceID, threadID] as const;
export const recoveryAttachmentsKey = (workspaceID: string, threadID: string) =>
  `attachments:${JSON.stringify([workspaceID, threadID])}`;
