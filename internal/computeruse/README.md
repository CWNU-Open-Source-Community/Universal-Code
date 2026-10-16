# Built-in Windows computer use

`Execute` handles the reserved `--internal-computer-use-mcp-v1` invocation before normal application startup. The SDK server uses the existing executable, stdio JSON-RPC, and the fixed `Catalog()` definitions. Host approval must bind a run to the complete `app_id`; `ParseAppID` provides only a display path, never authorization.

The normal sequence is list windows, obtain application approval, explicitly activate a selected window, then use the returned observation ID for input. Each successful action returns structured metadata and a PNG. Observation IDs expire after 60 seconds and after any executor's input or activation. The Windows session shares a named mutex and generation mapping across helper processes; a busy call fails immediately.

Screenshots use **GDI visible-desktop capture**, with DWM visible-frame bounds and physical pixels. Capture requires the target to be foreground and fully on screen. Occluding windows remain visible in the screenshot; clicks and scrolling reject points owned by another top-level window. Scroll moves the pointer to the window center and sends wheel detents (`dy` positive up, `dx` positive right). The executor validates live HWND, PID, process creation time, executable path/file identity, bounds and foreground before input. It excludes its own executable and ancestor process executables. Windows may refuse foreground activation or input into elevated applications; these failures are reported.

For `ApplicationFrameHost.exe` frames, the application identity comes from the unique non-host child-window process. The frame owner's PID, creation time and file identity remain separately bound and are rechecked together with the app. Missing or multiple child application processes are rejected instead of authorizing the shared host executable. Screenshots also obey the shared image attachment limits (8192 pixels per side and 16 MiPixels), before allocation and observation-token delivery. Accepted foreground activation is allowed up to two seconds to settle asynchronously; refusal remains explicit and no input queues are attached.

If input was sent but a fresh image cannot be obtained (for example, `ALT+F4` closed the window), the error result retains `action_sent: true`, the last verified window identity, and instructions to list/observe before further input. Input errors report `action_status: "unknown"` because some events may already have been sent. Neither case supplies an observation ID. Screenshots are checked using the SDK result's actual JSON/base64 encoding against the host's 4 MiB message budget, reserving space for the request ID and SDK envelope. An oversized image returns a bounded status without scaling or a usable observation token; the action's sent/unknown state is retained.

Unicode text input normalizes LF and CRLF to a single typed carriage return for Windows controls. Other characters, including UTF-16 surrogate pairs, are preserved.

The package does not launch applications. `testfixture` is a separate, opt-in native Go window for live verification:

```powershell
go build -o "$env:TEMP\computer-use-fixture.exe" ./internal/computeruse/testfixture
```

Launch that executable with `-state <temporary-json-path>`. Its multiline edit box, click counter, and scroll position are recorded in the JSON together with PID, HWND and control rectangles in screen coordinates. Only this dedicated fixture should be manipulated during automated live checks. The ordinary package tests use a fake backend, plus Win32 ABI, executable identity and coordination-object tests; they do not capture or send input to desktop windows.

The opt-in scripted test launches its own fixture and the supplied production executable's reserved MCP endpoint. It drives fixed actions without a model. Set all three absolute paths, then run only this test:

```powershell
$env:UNIVERSAL_CODE_COMPUTER_USE_LIVE_EXE = 'D:\path\to\universal-code.exe'
$env:UNIVERSAL_CODE_COMPUTER_USE_LIVE_FIXTURE = "$env:TEMP\computer-use-fixture.exe"
$env:UNIVERSAL_CODE_COMPUTER_USE_LIVE_OUTPUT = 'D:\path\to\test-artifacts'
go test ./internal/computeruse -run '^TestLiveWindowsFixture$' -count=1 -v -timeout 2m
```

It verifies Chinese/English text, multiline scrolling, a button click, consumed-observation rejection, and each PNG's actual dimensions. Original PNGs and fixture-only metadata are saved in a new subdirectory. It selects only its own fixture PID/title/HWND, calculates control coordinates from current rectangles, and cleans up only the two child processes it created. Leave the fixture unobstructed during this explicitly enabled desktop test. Missing any variable skips the test before launching processes.

If Windows refuses foreground activation, the test fails before input. To verify the documented manual-selection path, also set `UNIVERSAL_CODE_COMPUTER_USE_LIVE_MANUAL_FOCUS=1` and use `-timeout 5m`. After refusal, the runner waits at most three minutes for the user to select its own fixture; it sends no input during that wait. The result records `activation_method: "manual_selection"` instead of claiming automatic activation succeeded. If the fixture is not selected, the test fails and closes only its own children.

For real-model acceptance, start the fixture with `-visual-challenge`. It displays a fresh random task code, target click count, and a different marker on line 80; the button can appear on either side. Ask a vision-capable production Agent to read those values from screenshots, scroll to the last line, preserve the original text while appending `验收完成：<task code>` and `Desktop vision OK`, click to the target count, and inspect a final screenshot. Keep the fixture in the foreground throughout the task.

Keep `-state` output outside the Agent's workspace and never include its answers or control rectangles in model input. Use that state only to independently check the result. Acceptance requires preserved original text, the exact two appended lines, actual scrolling, the exact click count, and a completion report containing both random values. Retain evidence that actual PNGs entered the provider's image channel and drove later tool calls. A script passing, a successful tool dispatch, or the model choosing `finish` alone does not establish completion of this task.
