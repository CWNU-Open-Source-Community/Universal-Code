package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	var b bytes.Buffer
	err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, f.w.Bounds.Width, f.w.Bounds.Height)))
	return b.Bytes(), err
}
func (f *fakeBackend) activate(context.Context, nativeWindow) error {
	f.activations++
	f.w.Foreground = true
	return nil
}
func (f *fakeBackend) input(context.Context, nativeWindow, string, Arguments) error {
	f.inputs++
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
	if _, _, err := e.run(context.Background(), "computer_click", a); err == nil {
		t.Fatal("expected input failure")
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
	if _, _, err := e.run(context.Background(), "computer_observe", a); err == nil {
		t.Fatal("background captured")
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
	server := newServer(newExecutor(fake()))
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
}
