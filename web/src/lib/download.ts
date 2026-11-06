const unsafeFilename = /[\\/:*?"<>|\u0000-\u001f]/u;

function triggerDownload(filename: string, blob: Blob): void {
  const objectURL = URL.createObjectURL(blob);
  try {
    const link = document.createElement("a");
    link.href = objectURL;
    link.download = filename;
    link.rel = "noopener";
    link.click();
  } finally {
    // Revoking in the click task can cancel a download that has not started yet.
    globalThis.setTimeout(() => URL.revokeObjectURL(objectURL), 0);
  }
}

export function downloadBlob(filename: string, blob: Blob): void {
  if (!filename || unsafeFilename.test(filename) || !(blob instanceof Blob) || blob.size === 0) {
    throw new Error("A safe filename and non-empty Blob are required");
  }
  triggerDownload(filename, blob);
}

export function downloadTextFile(filename: string, mimeType: string, content: string): void {
  if (!filename || unsafeFilename.test(filename) || !mimeType || !content) {
    throw new Error("A safe filename, MIME type, and non-empty content are required");
  }
  triggerDownload(filename, new Blob([content], { type: mimeType }));
}
