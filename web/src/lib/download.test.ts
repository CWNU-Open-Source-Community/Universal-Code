import { downloadBlob, downloadTextFile } from "./download";

describe("download helpers", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function stubObjectURL() {
    const createObjectURL = vi.fn().mockReturnValue("blob:verified");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });
    return { createObjectURL, revokeObjectURL };
  }

  it("defers revocation of a text export until after the click", async () => {
    vi.useFakeTimers();
    const { createObjectURL, revokeObjectURL } = stubObjectURL();
    let observed: { href: string | null; download: string; rel: string } | null = null;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      observed = { href: this.getAttribute("href"), download: this.download, rel: this.rel };
    });

    downloadTextFile("report.json", "application/json", "{\"ok\":true}");

    const blob = createObjectURL.mock.calls[0]?.[0] as Blob;
    expect(await blob.text()).toBe("{\"ok\":true}");
    expect(blob.type).toBe("application/json");
    expect(observed).toEqual({ href: "blob:verified", download: "report.json", rel: "noopener" });
    expect(revokeObjectURL).not.toHaveBeenCalled();
    vi.runAllTimers();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:verified");
  });

  it("uses the deferred lifecycle for binary Blob downloads", () => {
    vi.useFakeTimers();
    const { createObjectURL, revokeObjectURL } = stubObjectURL();
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const content = new Blob(["png"], { type: "image/png" });

    downloadBlob("capture.png", content);

    expect(createObjectURL).toHaveBeenCalledWith(content);
    expect(click).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).not.toHaveBeenCalled();
    vi.runAllTimers();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:verified");
  });

  it("rejects unsafe filenames and empty content before creating an object URL", () => {
    const { createObjectURL } = stubObjectURL();
    expect(() => downloadTextFile("nested/report.json", "application/json", "{}"))
      .toThrow("A safe filename");
    expect(() => downloadTextFile("report.json", "application/json", ""))
      .toThrow("A safe filename");
    expect(() => downloadBlob("report.json", new Blob([]))).toThrow("A safe filename");
    expect(createObjectURL).not.toHaveBeenCalled();
  });
});
