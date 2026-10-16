package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeBackend struct {
	w                   nativeWindow
	epoch               uint64
	inputs, activations int
	inputErr            error
	unavailable         bool
	afterInput          func()
	captureErr          error
	captureBytes        []byte
	captures            int
}

func fake() *fakeBackend {
	return &fakeBackend{w: nativeWindow{Window: Window{AppID: "app-1", PID: 42, Bounds: Bounds{X: 10, Y: 20, Width: 100, Height: 80}, Foreground: true}, handle: 123, birth: 456}}
}
func (f *fakeBackend) enter(ctx context.Context) (func(), error) { return func() {}, ctx.Err() }
func (f *fakeBackend) generation(b bool) uint64 {
	if b {
		f.epoch++
	}
	return f.epoch
}
func (f *fakeBackend) list(context.Context) ([]nativeWindow, error) { return []nativeWindow{f.w}, nil }
func (f *fakeBackend) inspect(uintptr) (nativeWindow, error) {
	if f.unavailable {
		return nativeWindow{}, errors.New("closed")
	}
	return f.w, nil
}
func (f *fakeBackend) capture(context.Context, nativeWindow) ([]byte, error) {
	f.captures++
	if f.captureErr != nil {
		return nil, f.captureErr
	}
	if f.captureBytes != nil {
		return f.captureBytes, nil
	}
	var b bytes.Buffer
	err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, f.w.Bounds.Width, f.w.Bounds.Height)))
	return b.Bytes(), err
}

func TestCaptureDimensionsMatchAttachmentLimits(t *testing.T) {
	for _, b := range []Bounds{{Width: 1, Height: 1}, {Width: 8192, Height: 2048}, {Width: 4096, Height: 4096}} {
		if err := captureBoundsAllowed(b); err != nil {
			t.Fatalf("allowed dimensions rejected: %#v, %v", b, err)
		}
	}
	for _, b := range []Bounds{{Width: 8193, Height: 1}, {Width: 1, Height: 8193}, {Width: 8192, Height: 2049}, {Width: 4097, Height: 4096}, {Width: 0, Height: 1}} {
		if err := captureBoundsAllowed(b); err == nil {
			t.Fatalf("oversized dimensions accepted: %#v", b)
		}
		if b.Width == 0 {
			continue
		}
		e, f, a := ready(t)
		f.w.Bounds = b
		before := f.captures
		value, png, err := e.run(context.Background(), "computer_observe", a)
		if err != nil {
			t.Fatal(err)
		}
		out, ok := value.(ObservationUnavailable)
		if !ok || out.ObservationAvailable || out.ActionSent || len(png) != 0 || !strings.Contains(out.Reason, "8192") {
			t.Fatalf("missing dimension-limit result: %#v", value)
		}
		if f.captures != before {
			t.Fatal("oversized window was captured")
		}
		if _, ok := e.observations[a.WindowID]; ok {
			t.Fatal("oversized image retained an observation token")
		}
	}
}
func (f *fakeBackend) activate(context.Context, nativeWindow) error {
	f.activations++
	f.w.Foreground = true
	return nil
}
func (f *fakeBackend) input(context.Context, nativeWindow, string, Arguments) error {
	f.inputs++
	if f.afterInput != nil {
		f.afterInput()
	}
	return f.inputErr
}
func ready(t *testing.T) (*executor, *fakeBackend, Arguments) {
	t.Helper()
	f := fake()
	e := newExecutor(f)
	if _, _, err := e.run(context.Background(), "computer_list_windows", Arguments{}); err != nil {
		t.Fatal(err)
	}
	var a Arguments
	for id := range e.windows {
		a = Arguments{WindowID: id, AppID: f.w.AppID, X: 1, Y: 1}
	}
	v, _, err := e.run(context.Background(), "computer_observe", a)
	if err != nil {
		t.Fatal(err)
	}
	a.ObservationID = v.(Observation).ObservationID
	return e, f, a
}

func TestInputGuards(t *testing.T) {
	cases := map[string]func(*executor, *fakeBackend, *Arguments){
		"wrong app":          func(e *executor, f *fakeBackend, a *Arguments) { a.AppID = "other" },
		"unknown window":     func(e *executor, f *fakeBackend, a *Arguments) { a.WindowID = "other" },
		"stale token":        func(e *executor, f *fakeBackend, a *Arguments) { a.ObservationID = "old" },
		"changed pid":        func(e *executor, f *fakeBackend, a *Arguments) { f.w.PID++ },
		"reused pid":         func(e *executor, f *fakeBackend, a *Arguments) { f.w.birth++ },
		"changed executable": func(e *executor, f *fakeBackend, a *Arguments) { f.w.AppID = "new executable" },
		"moved window":       func(e *executor, f *fakeBackend, a *Arguments) { f.w.Bounds.X++ },
		"lost foreground":    func(e *executor, f *fakeBackend, a *Arguments) { f.w.Foreground = false },
		"closed window":      func(e *executor, f *fakeBackend, a *Arguments) { f.unavailable = true },
		"other run input":    func(e *executor, f *fakeBackend, a *Arguments) { f.generation(true) },
		"expired": func(e *executor, f *fakeBackend, a *Arguments) {
			o := e.observations[a.WindowID]
			o.at = time.Now().Add(-61 * time.Second)
			e.observations[a.WindowID] = o
		},
		"out of bounds": func(e *executor, f *fakeBackend, a *Arguments) { a.X = 100 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e, f, a := ready(t)
			mutate(e, f, &a)
			if _, _, err := e.run(context.Background(), "computer_click", a); err == nil {
				t.Fatal("unsafe input accepted")
			}
			if f.inputs != 0 {
				t.Fatal("input reached backend")
			}
		})
	}
}
func TestActionConsumesObservationAndReturnsFreshImage(t *testing.T) {
	e, f, a := ready(t)
	v, data, err := e.run(context.Background(), "computer_click", a)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(Observation)
	if o.ObservationID == a.ObservationID || f.inputs != 1 {
		t.Fatal("observation not advanced")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != o.ImageWidth || img.Bounds().Dy() != o.ImageHeight {
		t.Fatal("incorrect image dimensions")
	}
	if _, _, err = e.run(context.Background(), "computer_click", a); err == nil {
		t.Fatal("old observation reused")
	}
}
func TestFailedInputConsumesObservation(t *testing.T) {
	e, f, a := ready(t)
	f.inputErr = errors.New("partial input")
	value, image, err := e.run(context.Background(), "computer_click", a)
	if err != nil {
		t.Fatal(err)
	}
	failed, ok := value.(ObservationUnavailable)
	if !ok || failed.ActionStatus != "unknown" || failed.ActionSent || len(image) != 0 || !strings.Contains(failed.Reason, "partially sent") {
		t.Fatalf("partial-input status lost: %#v", value)
	}
	f.inputErr = nil
	if _, _, err := e.run(context.Background(), "computer_click", a); err == nil {
		t.Fatal("failed action token reused")
	}
	if f.inputs != 1 {
		t.Fatal("input repeated")
	}
}
func TestObserveDoesNotActivate(t *testing.T) {
	e, f, a := ready(t)
	f.w.Foreground = false
	if value, image, err := e.run(context.Background(), "computer_observe", a); err != nil || len(image) != 0 {
		t.Fatalf("unexpected background result: %T, %v", value, err)
	} else if unavailable, ok := value.(ObservationUnavailable); !ok || unavailable.ActionSent || unavailable.ActionStatus != "not_sent" {
		t.Fatalf("background capture status missing: %#v", value)
	}
	if f.activations != 0 {
		t.Fatal("observe changed focus")
	}
	if _, _, err := e.run(context.Background(), "computer_activate", a); err != nil {
		t.Fatal(err)
	}
	if f.activations != 1 {
		t.Fatal("explicit activation not performed")
	}
}
func TestCancelledInput(t *testing.T) {
	e, f, a := ready(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := e.run(ctx, "computer_click", a); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if f.inputs != 0 {
		t.Fatal("cancelled input performed")
	}
}

func TestSentActionSurvivesObservationFailure(t *testing.T) {
	for _, failure := range []string{"closed", "focus changed", "moved", "capture failed", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			e, f, a := ready(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.afterInput = func() {
				switch failure {
				case "closed":
					f.unavailable = true
				case "focus changed":
					f.w.Foreground = false
				case "moved":
					f.w.Bounds.X++
				case "capture failed":
					f.captureErr = errors.New("capture unavailable")
				case "cancelled":
					cancel()
				}
			}
			value, data, err := e.run(ctx, "computer_click", a)
			if err != nil {
				t.Fatal(err)
			}
			out, ok := value.(ObservationUnavailable)
			if !ok || !out.ActionSent || out.ActionStatus != "sent" || out.Action != "computer_click" || out.ObservationAvailable || len(data) != 0 || out.LastKnownWindow.WindowID != a.WindowID {
				t.Fatalf("sent action was lost: %#v", value)
			}
			if _, exists := e.observations[a.WindowID]; exists {
				t.Fatal("unavailable observation was authorized")
			}
			r, err := toolResult(value, data)
			if err != nil {
				t.Fatal(err)
			}
			if !r.IsError || r.StructuredContent == nil || len(r.Content) != 1 {
				t.Fatal("MCP lost the structured action result")
			}
			if text := r.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"action_sent":true`) || !strings.Contains(text, "Do not automatically replay") {
				t.Fatal("MCP did not explain the sent action and next step")
			}
		})
	}
}

func TestOversizedScreenshotPreservesActionAndInvalidatesObservation(t *testing.T) {
	// Use a real, poorly compressible PNG smaller than the host's raw 4 MiB
	// limit: base64 plus MCP's content envelope is what makes it too large.
	img := image.NewRGBA(image.Rect(0, 0, 1100, 1000))
	seed := uint32(1)
	for i := 0; i < len(img.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			img.Pix[i+c] = byte(seed)
		}
		img.Pix[i+3] = 255
	}
	var pngBuffer bytes.Buffer
	if err := png.Encode(&pngBuffer, img); err != nil {
		t.Fatal(err)
	}
	pngData := pngBuffer.Bytes()
	if len(pngData) >= 4*1024*1024 {
		t.Fatal("test PNG must fit before base64 encoding")
	}
	for _, action := range []bool{false, true} {
		t.Run(fmt.Sprintf("action=%t", action), func(t *testing.T) {
			e, f, a := ready(t)
			f.captureBytes = pngData
			name := "computer_observe"
			if action {
				name = "computer_click"
			}
			value, data, err := e.run(context.Background(), name, a)
			if err != nil {
				t.Fatal(err)
			}
			out, ok := value.(ObservationUnavailable)
			if !ok || out.ActionSent != action || out.ObservationAvailable || len(data) != 0 || !strings.Contains(out.Reason, "4 MiB") {
				t.Fatalf("unexpected oversized result: %#v", value)
			}
			if _, exists := e.observations[a.WindowID]; exists {
				t.Fatal("undelivered image left a usable observation token")
			}
			r, err := toolResult(value, data)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if !r.IsError || len(encoded) > maxResultBytes || len(r.Content) != 1 {
				t.Fatal("oversized result was not replaced with a bounded status")
			}
			if _, _, err = e.run(context.Background(), "computer_click", a); err == nil {
				t.Fatal("old observation accepted after oversized capture")
			}
		})
	}
	if fits, err := resultFits(Observation{}, pngData); err != nil || fits {
		t.Fatalf("encoded MCP size not checked: fits=%v err=%v", fits, err)
	}
}
func TestCatalogArgumentValidation(t *testing.T) {
	var click ToolDefinition
	for _, d := range Catalog() {
		if d.Name == "computer_click" {
			click = d
		}
	}
	valid := `{"window_id":"w","app_id":"a","observation_id":"o","x":0,"y":0}`
	if _, err := decodeArguments(click, []byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `{}`, `{"window_id":"w","app_id":"a","observation_id":"o","y":0}`, strings.Replace(valid, `"x":0`, `"x":null`, 1), strings.Replace(valid, `"x":0`, `"x":0,"x":1`, 1), strings.Replace(valid, `"x":0`, `"x":1.5`, 1), strings.Replace(valid, `"x":0`, `"x":0,"text":"extra"`, 1), valid + `{}`} {
		if _, err := decodeArguments(click, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestAppIDParser(t *testing.T) {
	p := `c:\apps\fixture.exe`
	id := "winapp:" + base64.RawURLEncoding.EncodeToString([]byte(p)) + ":" + strings.Repeat("ab", 32)
	if got, err := ParseAppID(id); err != nil || got != p {
		t.Fatalf("%s %v", got, err)
	}
	for _, p := range []string{`c:relative.exe`, `C:\apps\fixture.exe`, `c:\apps\..\fixture.exe`, `c:\apps\fixture.exe` + "\n", `c:\apps\\fixture.exe`} {
		id := "winapp:" + base64.RawURLEncoding.EncodeToString([]byte(p)) + ":" + strings.Repeat("ab", 32)
		if _, err := ParseAppID(id); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}
func TestKeys(t *testing.T) {
	for _, s := range []string{"CTRL+A", "SHIFT+TAB", "CTRL+ALT+DELETE", "F10", "F12", "ENTER", "WIN"} {
		if _, err := parseChord(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"A+B", "CTRL+CONTROL+A", "F13", "", "CTRL++A", "F:"} {
		if _, err := parseChord(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestExecuteReservedArgument(t *testing.T) {
	for _, args := range [][]string{{Arg, "extra"}, {Arg + "=x"}, {"x", Arg}} {
		if handled, code := Execute(args, strings.NewReader(""), &bytes.Buffer{}); !handled || code != 2 {
			t.Fatalf("%v: %v %d", args, handled, code)
		}
	}
	if handled, _ := Execute([]string{"normal"}, nil, nil); handled {
		t.Fatal("normal startup intercepted")
	}
}

func TestSDKCatalogAndImageContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := fake()
	server := newServer(newExecutor(f))
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if got := cs.InitializeResult(); got.ProtocolVersion != ProtocolVersion || got.ServerInfo.Name != ServerName || got.ServerInfo.Version != ServerVersion {
		t.Fatalf("catalog constants differ from SDK negotiation: %#v", got)
	}
	catalog, err := cs.ListTools(ctx, nil)
	if err != nil || len(catalog.Tools) != len(Catalog()) {
		t.Fatalf("catalog: %v %v", catalog, err)
	}
	listed, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "computer_list_windows", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var windows struct {
		Windows []Window `json:"windows"`
	}
	if err = json.Unmarshal([]byte(listed.Content[0].(*mcp.TextContent).Text), &windows); err != nil {
		t.Fatal(err)
	}
	w := windows.Windows[0]
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "computer_observe", Arguments: map[string]any{"window_id": w.WindowID, "app_id": w.AppID}})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError || r.StructuredContent == nil || len(r.Content) != 2 {
		t.Fatalf("unexpected result %#v", r)
	}
	img, ok := r.Content[1].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" {
		t.Fatalf("missing PNG %#v", r.Content)
	}
	if _, err = png.Decode(bytes.NewReader(img.Data)); err != nil {
		t.Fatal(err)
	}
	var o Observation
	if err = json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &o); err != nil {
		t.Fatal(err)
	}
	f.afterInput = func() { f.unavailable = true }
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "computer_key", Arguments: map[string]any{"window_id": w.WindowID, "app_id": w.AppID, "observation_id": o.ObservationID, "key": "ALT+F4"}})
	if err != nil {
		t.Fatal(err)
	}
	var unavailable ObservationUnavailable
	if err = json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &unavailable); err != nil {
		t.Fatal(err)
	}
	if !r.IsError || r.StructuredContent == nil || len(r.Content) != 1 || !unavailable.ActionSent || unavailable.ActionStatus != "sent" {
		t.Fatalf("closed-window action status lost across SDK: %#v", r)
	}
	if _, err = cs.ListTools(ctx, nil); err != nil {
		t.Fatalf("observation failure disconnected MCP: %v", err)
	}
}
