package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/desktopbrowser"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/store"
)

// SharedBrowserOptions is supplied only by the native shell, never by a page.
type SharedBrowserOptions struct {
	HomePath      string
	CreateHost    func(context.Context, string, func(desktopbrowser.Event)) (*desktopbrowser.Host, error)
	SetFullscreen func(context.Context, bool) error
}

type sharedBrowserSession struct {
	action         sync.Mutex
	mu             sync.Mutex
	state          SharedBrowserState
	host           *desktopbrowser.Host
	pages          []desktopbrowser.Page
	profile        string
	layoutRevision int64
	visible        bool
	bounds         desktopbrowser.Bounds
	lastProjection string
	receipts       map[string]SharedBrowserCommandRequest
	receiptOrder   []string
	agentLease     *sharedBrowserLease
}

type SharedBrowserService struct {
	mu        sync.Mutex
	surfaceMu sync.Mutex
	options   SharedBrowserOptions
	store     *store.SQLiteStore
	agent     *application.AgentBrowserService
	sessions  map[string]*sharedBrowserSession
	owners    map[string]string
	profiles  map[string]string
	closed    bool
}

func (c *ControlPlane) SharedBrowser() *SharedBrowserService {
	if c == nil {
		return nil
	}
	return c.sharedBrowser
}

func newSharedBrowserService(st *store.SQLiteStore, options SharedBrowserOptions) (*SharedBrowserService, error) {
	if st == nil || options.CreateHost == nil || !filepath.IsAbs(options.HomePath) {
		return nil, errors.New("native browser dependencies are unavailable")
	}
	s := &SharedBrowserService{store: st, options: options, sessions: map[string]*sharedBrowserSession{}, owners: map[string]string{}, profiles: map[string]string{}}
	data, err := os.ReadFile(s.profileIndex())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.profiles); err != nil {
			return nil, err
		}
		for _, profile := range s.profiles {
			if !validWorkspaceIdentity(profile) || !strings.HasPrefix(profile, "profile-") || filepath.Base(profile) != profile || strings.ContainsAny(profile, `/\.`) {
				return nil, errors.New("invalid shared browser profile index")
			}
		}
	}
	if s.profiles == nil {
		s.profiles = map[string]string{}
	}
	return s, nil
}

func (s *SharedBrowserService) profileIndex() string {
	return filepath.Join(s.options.HomePath, "shared-browser", "profiles.json")
}
func (s *SharedBrowserService) saveProfiles() error {
	path := s.profileIndex()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(s.profiles)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "profiles-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s *SharedBrowserService) scope(ctx context.Context, request SharedBrowserOpenRequest) (SharedBrowserOpenRequest, error) {
	if !validWorkspaceIdentity(request.WorkspaceID) {
		return request, apperror.New(apperror.CodeInvalidArgument, "workspace is required")
	}
	if _, err := s.store.GetWorkspaceByID(ctx, request.WorkspaceID); err != nil {
		return request, apperror.New(apperror.CodeNotFound, "browser workspace was not found")
	}
	if request.RunID != "" {
		thread, err := s.store.GetThreadByRun(ctx, request.RunID)
		if err != nil {
			return request, apperror.New(apperror.CodeNotFound, "browser run was not found")
		}
		if request.ThreadID != "" && request.ThreadID != thread.ID {
			return request, apperror.New(apperror.CodeInvalidArgument, "browser run belongs to another chat")
		}
		request.ThreadID = thread.ID
	}
	if request.ThreadID != "" {
		thread, err := s.store.GetThread(ctx, request.ThreadID)
		if err != nil || thread.Status == domain.ThreadDeleted {
			return request, apperror.New(apperror.CodeNotFound, "browser chat was not found")
		}
		if request.WorkspaceID != thread.WorkspaceID && request.WorkspaceID != thread.ProjectWorkspaceID {
			return request, apperror.New(apperror.CodeInvalidArgument, "browser chat belongs to another workspace")
		}
		// Derive the current run from durable chat state; a renderer cannot bind an old run.
		request.RunID = thread.ActiveRunID
	}
	return request, nil
}

func (s *SharedBrowserService) Open(ctx context.Context, request SharedBrowserOpenRequest) (SharedBrowserState, error) {
	request, err := s.scope(ctx, request)
	if err != nil {
		return SharedBrowserState{}, err
	}
	key := "draft:" + request.WorkspaceID
	if request.ThreadID != "" {
		key = "thread:" + request.ThreadID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return SharedBrowserState{}, desktopbrowser.ErrClosed
	}
	if request.AdoptSessionID != "" {
		old := s.sessions[request.AdoptSessionID]
		if old == nil || request.ThreadID == "" {
			return SharedBrowserState{}, apperror.New(apperror.CodeInvalidArgument, "browser draft is unavailable")
		}
		old.mu.Lock()
		if old.state.WorkspaceID != request.WorkspaceID || (old.state.ThreadID != "" && old.state.ThreadID != request.ThreadID) {
			old.mu.Unlock()
			return SharedBrowserState{}, apperror.New(apperror.CodeInvalidArgument, "browser draft belongs to another chat")
		}
		if existing := s.owners[key]; existing != "" && existing != request.AdoptSessionID {
			old.mu.Unlock()
			return SharedBrowserState{}, apperror.New(apperror.CodeConflict, "chat already has a browser")
		}
		previousProfiles := s.profiles
		s.profiles = make(map[string]string, len(previousProfiles)+1)
		for owner, profile := range previousProfiles {
			s.profiles[owner] = profile
		}
		s.profiles[key] = old.profile
		draftKey := "draft:" + request.WorkspaceID
		adoptsDraft := s.owners[draftKey] == request.AdoptSessionID
		if adoptsDraft {
			delete(s.profiles, draftKey)
		}
		if err := s.saveProfiles(); err != nil {
			s.profiles = previousProfiles
			old.mu.Unlock()
			return SharedBrowserState{}, err
		}
		if adoptsDraft {
			delete(s.owners, draftKey)
		}
		old.state.ThreadID = request.ThreadID
		old.state.RunID = request.RunID
		s.owners[key] = old.state.SessionID
		old.mu.Unlock()
		return s.project(old), nil
	}
	if session := s.sessions[s.owners[key]]; session != nil {
		session.mu.Lock()
		session.state.RunID = request.RunID
		session.mu.Unlock()
		return s.project(session), nil
	}
	profile := s.profiles[key]
	if profile == "" {
		profile = idgen.New("profile")
		s.profiles[key] = profile
		if err := s.saveProfiles(); err != nil {
			delete(s.profiles, key)
			return SharedBrowserState{}, err
		}
	}
	sessionID := idgen.New("browser")
	session := &sharedBrowserSession{profile: profile, receipts: map[string]SharedBrowserCommandRequest{}, state: SharedBrowserState{ProtocolVersion: SharedBrowserProtocolVersion, SessionID: sessionID, WorkspaceID: request.WorkspaceID, ThreadID: request.ThreadID, RunID: request.RunID, Revision: 1, State: "starting", ControlOwner: "user", Tabs: []SharedBrowserTab{}}}
	host, err := s.options.CreateHost(ctx, filepath.Join(s.options.HomePath, "shared-browser", "profiles", profile), func(event desktopbrowser.Event) { s.onEvent(sessionID, event) })
	if err != nil {
		return SharedBrowserState{}, err
	}
	session.host = host
	page, err := host.CreateTab(ctx, desktopbrowser.TabOptions{ID: idgen.New("tab"), URL: "about:blank"})
	if err != nil {
		_ = host.Close(ctx)
		return SharedBrowserState{}, err
	}
	session.pages = []desktopbrowser.Page{page}
	session.state.ActiveTabID = page.ID()
	session.state.State = "ready"
	s.sessions[sessionID] = session
	s.owners[key] = sessionID
	return s.project(session), nil
}

func (s *SharedBrowserService) session(id string) (*sharedBrowserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, desktopbrowser.ErrClosed
	}
	item := s.sessions[id]
	if item == nil {
		return nil, apperror.New(apperror.CodeNotFound, "browser session was not found")
	}
	return item, nil
}

func (s *SharedBrowserService) project(session *sharedBrowserSession) SharedBrowserState {
	session.mu.Lock()
	defer session.mu.Unlock()
	view := session.state
	view.History = append([]SharedBrowserHistoryEntry(nil), session.state.History...)
	view.Tabs = make([]SharedBrowserTab, 0, len(session.pages))
	for _, page := range session.pages {
		p := page.State()
		if !p.Closed {
			view.Tabs = append(view.Tabs, SharedBrowserTab{TabID: p.ID, URL: p.URL, Title: p.Title, Loading: p.Loading, CanGoBack: p.CanGoBack, CanGoForward: p.CanGoForward})
		}
	}
	view.Revision = 0
	data, _ := json.Marshal(view)
	sum := sha256.Sum256(data)
	projection := hex.EncodeToString(sum[:])
	if projection != session.lastProjection {
		session.state.Revision++
		session.lastProjection = projection
	}
	view.Revision = session.state.Revision
	return view
}

func (s *SharedBrowserService) Get(ctx context.Context, request SharedBrowserGetRequest) (SharedBrowserState, error) {
	if err := ctx.Err(); err != nil {
		return SharedBrowserState{}, err
	}
	session, err := s.session(request.SessionID)
	if err != nil {
		return SharedBrowserState{}, err
	}
	return s.project(session), nil
}

func (s *SharedBrowserService) detach(ctx context.Context, session *sharedBrowserSession) error {
	if s.agent == nil {
		return nil
	}
	session.mu.Lock()
	runID := session.state.RunID
	session.mu.Unlock()
	if runID == "" {
		return nil
	}
	view, err := s.agent.GetStatus(ctx, runID)
	if err != nil {
		return err
	}
	if view.SessionID != "" {
		if _, err = s.agent.DetachForUser(ctx, runID, view.SessionID); err != nil {
			return err
		}
	}
	session.mu.Lock()
	session.state.ControlOwner = "user"
	session.mu.Unlock()
	return nil
}

func sharedBrowserURL(raw string) (string, error) {
	if raw == "about:blank" {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(raw, "\r\n\x00") {
		return "", apperror.New(apperror.CodeInvalidArgument, "browser URL must use HTTP or HTTPS")
	}
	return u.String(), nil
}

func (s *SharedBrowserService) Command(ctx context.Context, request SharedBrowserCommandRequest) (SharedBrowserState, error) {
	if !validWorkspaceIdentity(request.OperationKey) {
		return SharedBrowserState{}, apperror.New(apperror.CodeInvalidArgument, "browser operation key is required")
	}
	session, err := s.session(request.SessionID)
	if err != nil {
		return SharedBrowserState{}, err
	}
	session.action.Lock()
	defer session.action.Unlock()
	if prior, ok := session.receipts[request.OperationKey]; ok {
		if prior != request {
			return SharedBrowserState{}, apperror.New(apperror.CodeConflict, "browser operation key was reused")
		}
		return s.project(session), nil
	}
	session.mu.Lock()
	tabID := request.TabID
	if tabID == "" {
		tabID = session.state.ActiveTabID
	}
	page, found := session.host.Tab(tabID)
	runID := session.state.RunID
	session.mu.Unlock()
	if !found && request.Command != "new_tab" {
		return SharedBrowserState{}, apperror.New(apperror.CodeNotFound, "browser tab was not found")
	}
	switch request.Command {
	case "take_control":
		if err = s.detach(ctx, session); err != nil {
			return SharedBrowserState{}, err
		}
	case "new_tab", "select_tab", "close_tab", "navigate", "back", "forward", "reload", "stop", "focus", "release_control", "enter_fullscreen", "exit_fullscreen":
	default:
		return SharedBrowserState{}, apperror.New(apperror.CodeInvalidArgument, "unsupported browser command")
	}
	switch request.Command {
	case "new_tab":
		destination := "about:blank"
		if request.URL != "" {
			destination, err = sharedBrowserURL(request.URL)
		}
		if err == nil {
			var created desktopbrowser.Page
			created, err = session.host.CreateTab(ctx, desktopbrowser.TabOptions{ID: idgen.New("tab"), URL: destination})
			if err == nil {
				session.mu.Lock()
				session.pages = append(session.pages, created)
				session.state.ActiveTabID = created.ID()
				session.mu.Unlock()
				if page != nil {
					err = page.SetSurface(ctx, desktopbrowser.Bounds{}, false)
				}
			}
		}
	case "select_tab":
		session.mu.Lock()
		previous, _ := session.host.Tab(session.state.ActiveTabID)
		session.state.ActiveTabID = tabID
		session.mu.Unlock()
		if previous != nil && previous.ID() != tabID {
			err = previous.SetSurface(ctx, desktopbrowser.Bounds{}, false)
		}
	case "close_tab":
		err = page.Close(ctx)
		if err == nil {
			session.mu.Lock()
			pages := session.pages[:0]
			for _, p := range session.pages {
				if p.ID() != tabID {
					pages = append(pages, p)
				}
			}
			session.pages = pages
			if session.state.ActiveTabID == tabID {
				session.state.ActiveTabID = ""
				if len(pages) > 0 {
					session.state.ActiveTabID = pages[len(pages)-1].ID()
				}
			}
			session.mu.Unlock()
		}
	case "navigate":
		var destination string
		destination, err = sharedBrowserURL(request.URL)
		if err == nil {
			err = page.Navigate(ctx, destination)
		}
	case "back":
		err = page.Back(ctx)
	case "forward":
		err = page.Forward(ctx)
	case "reload":
		err = page.Reload(ctx)
	case "stop":
		err = page.Stop(ctx)
	case "focus":
		err = page.Focus(ctx)
	case "release_control":
		if s.agent == nil || runID == "" {
			err = apperror.New(apperror.CodeFailedPrecondition, "no active Agent task")
		} else {
			_, err = s.agent.AttachSharedPage(ctx, runID)
		}
	case "enter_fullscreen", "exit_fullscreen":
		if s.options.SetFullscreen == nil {
			err = desktopbrowser.ErrUnsupported
		} else {
			fullscreen := request.Command == "enter_fullscreen"
			err = s.options.SetFullscreen(ctx, fullscreen)
			if err == nil {
				session.mu.Lock()
				session.state.Fullscreen = fullscreen
				session.mu.Unlock()
			}
		}
	}
	if err != nil {
		return SharedBrowserState{}, err
	}
	session.receipts[request.OperationKey] = request
	session.receiptOrder = append(session.receiptOrder, request.OperationKey)
	if len(session.receiptOrder) > 128 {
		delete(session.receipts, session.receiptOrder[0])
		session.receiptOrder = session.receiptOrder[1:]
	}
	return s.project(session), nil
}

func (s *SharedBrowserService) SetSurface(ctx context.Context, request SharedBrowserSurfaceRequest) (SharedBrowserSurfaceResult, error) {
	result := SharedBrowserSurfaceResult{ProtocolVersion: SharedBrowserProtocolVersion, SessionID: request.SessionID}
	session, err := s.session(request.SessionID)
	if err != nil {
		return result, err
	}
	if request.LayoutRevision <= 0 {
		return result, apperror.New(apperror.CodeInvalidArgument, "browser layout revision is required")
	}
	scale := request.DeviceScaleFactor
	if scale == 0 {
		scale = 1
	}
	for _, v := range []float64{request.Bounds.X, request.Bounds.Y, request.Bounds.Width, request.Bounds.Height, scale} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 32768 {
			return result, apperror.New(apperror.CodeInvalidArgument, "invalid browser bounds")
		}
	}
	if scale < 0.25 || scale > 8 {
		return result, apperror.New(apperror.CodeInvalidArgument, "invalid browser scale")
	}
	s.surfaceMu.Lock()
	defer s.surfaceMu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if request.LayoutRevision <= session.layoutRevision {
		result.LayoutRevision = session.layoutRevision
		result.Visible = session.visible
		return result, nil
	}
	page, ok := session.host.Tab(request.TabID)
	if !ok {
		return result, apperror.New(apperror.CodeNotFound, "browser tab was not found")
	}
	visible := request.Visible && request.TabID == session.state.ActiveTabID && request.Bounds.Width > 0 && request.Bounds.Height > 0
	b := request.Bounds
	bounds := desktopbrowser.Bounds{X: int32(math.Round(b.X * scale)), Y: int32(math.Round(b.Y * scale)), Width: int32(math.Round(b.Width * scale)), Height: int32(math.Round(b.Height * scale))}
	if err = page.SetSurface(ctx, bounds, visible); err != nil {
		return result, err
	}
	session.layoutRevision = request.LayoutRevision
	session.visible = visible
	session.bounds = bounds
	result.LayoutRevision = request.LayoutRevision
	result.Visible = visible
	return result, nil
}

func (s *SharedBrowserService) onEvent(sessionID string, event desktopbrowser.Event) {
	if event.Kind != "popup" && event.Kind != "closed" && event.Kind != "navigated" && event.Kind != "state" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := s.session(sessionID)
	if err != nil {
		return
	}
	if event.Kind == "navigated" || event.Kind == "state" {
		session.mu.Lock()
		defer session.mu.Unlock()
		if event.Kind == "navigated" {
			session.recordNavigation(event.State, time.Now().UTC())
		} else {
			for index := range session.state.History {
				if session.state.History[index].URL == event.State.URL && event.State.Title != "" {
					session.state.History[index].Title = event.State.Title
					break
				}
			}
		}
		return
	}
	if event.Kind == "closed" {
		session.action.Lock()
		defer session.action.Unlock()
		session.mu.Lock()
		defer session.mu.Unlock()
		pages := session.pages[:0]
		for _, page := range session.pages {
			if page.ID() != event.State.ID {
				pages = append(pages, page)
			}
		}
		session.pages = pages
		if session.state.ActiveTabID == event.State.ID {
			session.state.ActiveTabID = ""
			if len(pages) > 0 {
				session.state.ActiveTabID = pages[len(pages)-1].ID()
			}
		}
		return
	}
	// Native NewWindowRequested creates the exact child WebView before reporting
	// this event. Reusing it preserves window.opener and same-profile policy.
	page, ok := session.host.Tab(event.State.ID)
	if !ok {
		return
	}
	session.action.Lock()
	defer session.action.Unlock()
	session.mu.Lock()
	for _, existing := range session.pages {
		if existing.ID() == page.ID() {
			session.mu.Unlock()
			return
		}
	}
	previous, _ := session.host.Tab(session.state.ActiveTabID)
	session.pages = append(session.pages, page)
	session.state.ActiveTabID = page.ID()
	session.mu.Unlock()
	if previous != nil {
		_ = previous.SetSurface(ctx, desktopbrowser.Bounds{}, false)
	}
}

// History is a short, session-local list derived only from successful native
// navigations. The start page never invents recommendations or visits.
func (s *sharedBrowserSession) recordNavigation(page desktopbrowser.State, at time.Time) {
	if page.Closed || page.Error != "" || page.Loading || page.URL == "about:blank" || len(page.URL) > 8192 {
		return
	}
	if _, err := sharedBrowserURL(page.URL); err != nil {
		return
	}
	history := []SharedBrowserHistoryEntry{{URL: page.URL, Title: page.Title, VisitedAt: at}}
	for _, entry := range s.state.History {
		if entry.URL != page.URL {
			history = append(history, entry)
			if len(history) == 50 {
				break
			}
		}
	}
	s.state.History = history
}

func (s *SharedBrowserService) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sessions := make([]*sharedBrowserSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	var result error
	for _, session := range sessions {
		result = errors.Join(result, s.detach(ctx, session), session.host.Close(ctx))
	}
	return result
}
