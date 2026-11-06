// @vitest-environment node
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { expect, it, onTestFinished } from "vitest";

const { JSDOM } = createRequire(import.meta.url)("jsdom");

it("loads a document and sends XHR through jsdom's installed network dispatcher", async () => {
  const server = createServer((request, response) => {
    response.setHeader("Content-Type", request.url === "/data" ? "text/plain" : "text/html");
    response.end(request.url === "/data" ? "xhr-ok" : '<!doctype html><p id="result">loaded</p>');
  });
  let closeWindow: (() => void) | undefined;
  onTestFinished(async () => {
    closeWindow?.();
    server.closeAllConnections();
    await new Promise<void>(resolve => server.close(() => resolve()));
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("Expected a TCP listener");
  const url = `http://127.0.0.1:${address.port}`;

  const page = await JSDOM.fromURL(url);
  closeWindow = () => page.window.close();
  expect(page.window.document.querySelector("#result").textContent).toBe("loaded");
  const body = await new Promise<string>((resolve, reject) => {
    const xhr: XMLHttpRequest = new page.window.XMLHttpRequest();
    xhr.open("GET", `${url}/data`);
    xhr.timeout = 5_000;
    xhr.onload = () => resolve(xhr.responseText);
    xhr.onerror = xhr.ontimeout = () => reject(new Error("jsdom XHR failed"));
    xhr.send();
  });
  expect(body).toBe("xhr-ok");
}, 10_000);
