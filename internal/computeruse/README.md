# Built-in Windows computer use

`Execute` handles the reserved `--internal-computer-use-mcp-v1` invocation before normal application startup. The SDK server uses the existing executable, stdio JSON-RPC, and the fixed `Catalog()` definitions. Host approval must bind a run to the complete `app_id`; `ParseAppID` provides only a display path, never authorization.

The normal sequence is list windows, obtain application approval, explicitly activate a selected window, then use the returned observation ID for input. Each successful action returns structured metadata and a PNG. Observation IDs expire after 60 seconds and after any executor's input or activation. The Windows session shares a named mutex and generation mapping across helper processes; a busy call fails immediately.

Screenshots use **GDI visible-desktop capture**, with DWM visible-frame bounds and physical pixels. Capture requires the target to be foreground and fully on screen. Occluding windows remain visible in the screenshot; clicks and scrolling reject points owned by another top-level window. Scroll moves the pointer to the window center and sends wheel detents (`dy` positive up, `dx` positive right). The executor validates live HWND, PID, process creation time, executable path/file identity, bounds and foreground before input. It excludes its own executable and ancestor process executables. Windows may refuse foreground activation or input into elevated applications; these failures are reported.

The package does not launch applications. `testfixture` is a separate, opt-in native Go window for live verification:

```powershell
go build -o "$env:TEMP\computer-use-fixture.exe" ./internal/computeruse/testfixture
```

Launch that executable with `-state <temporary-json-path>`. Its multiline edit box, click counter, and scroll position are recorded in the JSON together with PID, HWND and control rectangles in screen coordinates. Only this dedicated fixture should be manipulated during automated live checks. The ordinary package tests use a fake backend, plus Win32 ABI, executable identity and coordination-object tests; they do not capture or send input to desktop windows.
