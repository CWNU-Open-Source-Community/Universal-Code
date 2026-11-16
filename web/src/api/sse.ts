import { StreamProtocolError } from "./client/protocol";

export interface SSEMessage {
  data: string;
  event: string;
  id: string;
}

const defaultMaxEventBytes = 2 * 1024 * 1024;

export async function consumeSSE(
  stream: ReadableStream<Uint8Array>,
  onMessage: (message: SSEMessage) => void,
  maxEventBytes = defaultMaxEventBytes,
): Promise<void> {
  const reader = stream.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let buffer = "";
  let dataLines: string[] = [];
  let event = "message";
  let id = "";
  let eventBytes = 0;

  const dispatch = () => {
    if (dataLines.length > 0) {
      onMessage({ data: dataLines.join("\n"), event, id });
    }
    dataLines = [];
    event = "message";
    eventBytes = 0;
  };

  const processLine = (rawLine: string) => {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (line === "") {
      dispatch();
      return;
    }
    if (line.startsWith(":")) {
      return;
    }

    const separator = line.indexOf(":");
    const field = separator < 0 ? line : line.slice(0, separator);
    let value = separator < 0 ? "" : line.slice(separator + 1);
    if (value.startsWith(" ")) {
      value = value.slice(1);
    }

    eventBytes += new TextEncoder().encode(line).byteLength;
    if (eventBytes > maxEventBytes) {
      throw new StreamProtocolError("SSE event exceeded the configured client limit");
    }
    if (field === "data") {
      dataLines.push(value);
    } else if (field === "event") {
      event = value || "message";
    } else if (field === "id" && !value.includes("\0")) {
      id = value;
    }
  };

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      buffer += decoder.decode(value, { stream: true });
      let newline = buffer.indexOf("\n");
      while (newline >= 0) {
        processLine(buffer.slice(0, newline));
        buffer = buffer.slice(newline + 1);
        newline = buffer.indexOf("\n");
      }
      if (new TextEncoder().encode(buffer).byteLength > maxEventBytes) {
        throw new StreamProtocolError("SSE line exceeded the configured client limit");
      }
    }
    buffer += decoder.decode();
    if (buffer !== "") {
      processLine(buffer);
    }
  } catch (error) {
    try {
      await reader.cancel(error);
    } catch {
      // Preserve the parser or transport error that caused cancellation.
    }
    throw error;
  } finally {
    reader.releaseLock();
  }
}
