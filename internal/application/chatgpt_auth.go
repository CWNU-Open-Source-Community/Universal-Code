package application

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/chatgptauth"
	"cyberagent-workbench/internal/modelregistry"
	"github.com/google/uuid"
)

const ChatGPTAuthProtocolVersion = "chatgpt_auth.v1"

type ChatGPTAccountStatus struct {
	ID       string               `json:"id"`
	Label    string               `json:"label"`
	Email    string               `json:"email"`
	Provider string               `json:"provider"`
	State    string               `json:"state"`
	Models   []ChatGPTModelStatus `json:"models"`
}

type ChatGPTModelStatus struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ChatGPTLoginStatus struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	StartURL  string `json:"start_url,omitempty"`
	Message   string `json:"message,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// This is deliberately a projection: Auth and its pending token response must
// never cross the renderer boundary, including on failure or cancellation.
type ChatGPTAuthStatus struct {
	ProtocolVersion string                 `json:"protocol_version"`
	Available       bool                   `json:"available"`
	StoreKind       string                 `json:"store_kind"`
	Accounts        []ChatGPTAccountStatus `json:"accounts"`
	Login           *ChatGPTLoginStatus    `json:"login,omitempty"`
	UsageURL        string                 `json:"usage_url"`
}

type ChatGPTAuthRequest struct {
	Version   string `json:"version"`
	AccountID string `json:"account_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	Confirm   bool   `json:"confirm"`
}

type ChatGPTAuthClient interface {
	Login(context.Context, chatgptauth.Store, string, bool, func(string)) (*chatgptauth.Auth, error)
	Credentials(context.Context, chatgptauth.Store, string) (*chatgptauth.Auth, error)
	Models(context.Context, *chatgptauth.Auth) ([]chatgptauth.Model, error)
	Logout(context.Context, chatgptauth.Store, string) error
}

type chatGPTLogin struct {
	status ChatGPTLoginStatus
	cancel context.CancelFunc
	ready  chan struct{}
	done   chan struct{}
}

type ChatGPTAuthService struct {
	client      ChatGPTAuthClient
	store       chatgptauth.Store
	registry    providerCredentialMutationRegistry
	settings    modelregistry.RouteSettingStore
	openBrowser func(string) error
	mu          sync.Mutex
	login       *chatGPTLogin
	closed      bool
}

func NewChatGPTAuthService(client ChatGPTAuthClient, store chatgptauth.Store,
	registry providerCredentialMutationRegistry, settings modelregistry.RouteSettingStore,
	openBrowser func(string) error,
) *ChatGPTAuthService {
	return &ChatGPTAuthService{client: client, store: store, registry: registry,
		settings: settings, openBrowser: openBrowser}
}

func (s *ChatGPTAuthService) Status(context.Context) (ChatGPTAuthStatus, error) {
	result := ChatGPTAuthStatus{ProtocolVersion: ChatGPTAuthProtocolVersion,
		Accounts: []ChatGPTAccountStatus{}, StoreKind: "unavailable", UsageURL: chatgptauth.UsageURL}
	if s.store.KeyStore != nil {
		result.StoreKind = s.store.KeyStore.Kind()
		result.Available = s.store.KeyStore.Available()
	}
	if result.Available {
		db, err := s.store.Snapshot()
		if err != nil {
			return result, apperror.New(apperror.CodeUnavailable, "无法读取受保护的 ChatGPT 登录信息")
		}
		for _, auth := range db.Accounts {
			state := "connected"
			switch {
			case auth.PendingRefresh != nil && auth.PendingRefresh.IdentityError != "":
				state = "reauthorization_required"
			case auth.PendingRefresh != nil:
				state = "verification_pending"
			case auth.AccessToken == "":
				state = "signed_out"
			case !auth.PlanEnabled():
				state = "reauthorization_required"
			}
			provider := modelregistry.ChatGPTProviderID(auth.ClientID)
			label := auth.Email
			if label == "" {
				label = "ChatGPT"
			}
			label += " · " + provider[len(provider)-8:]
			account := ChatGPTAccountStatus{ID: auth.ClientID, Label: label, Email: auth.Email,
				Provider: provider, State: state, Models: []ChatGPTModelStatus{}}
			for _, model := range auth.Models {
				account.Models = append(account.Models, ChatGPTModelStatus{ID: model.Slug, Name: model.DisplayName})
			}
			result.Accounts = append(result.Accounts, account)
		}
	}
	s.mu.Lock()
	if s.login != nil {
		copy := s.login.status
		result.Login = &copy
	}
	s.mu.Unlock()
	return result, nil
}

func (s *ChatGPTAuthService) Change(ctx context.Context, action string, request ChatGPTAuthRequest) (ChatGPTAuthStatus, error) {
	if request.Version != ChatGPTAuthProtocolVersion || !request.Confirm {
		return ChatGPTAuthStatus{}, apperror.New(apperror.CodeInvalidArgument, "ChatGPT 登录操作无效")
	}
	if s.store.KeyStore == nil || !s.store.KeyStore.Available() {
		return ChatGPTAuthStatus{}, apperror.New(apperror.CodeFailedPrecondition, "系统凭据存储不可用，无法保存 ChatGPT 登录")
	}
	if err := ctx.Err(); err != nil {
		return ChatGPTAuthStatus{}, err
	}
	if (action != "login" && action != "logout" && action != "models") && request.AccountID != "" ||
		(action != "cancel" && request.AttemptID != "") {
		return ChatGPTAuthStatus{}, apperror.New(apperror.CodeInvalidArgument, "ChatGPT 登录操作参数不匹配")
	}
	if action == "logout" || action == "models" || action == "login" && request.AccountID != "" {
		if request.AccountID == "" {
			return ChatGPTAuthStatus{}, apperror.New(apperror.CodeInvalidArgument, "请选择 ChatGPT 账号")
		}
		if _, err := s.store.Selected(request.AccountID); err != nil {
			if errors.Is(err, chatgptauth.ErrNotFound) {
				return ChatGPTAuthStatus{}, apperror.New(apperror.CodeNotFound, "ChatGPT 账号不存在")
			}
			return ChatGPTAuthStatus{}, apperror.New(apperror.CodeUnavailable, "无法读取受保护的 ChatGPT 登录信息")
		}
	}
	switch action {
	case "login":
		return s.start(ctx, request.AccountID)
	case "cancel":
		s.mu.Lock()
		current := s.login
		if current == nil || request.AttemptID == "" || current.status.ID != request.AttemptID {
			s.mu.Unlock()
			return ChatGPTAuthStatus{}, apperror.New(apperror.CodeConflict, "登录请求已改变，请刷新后重试")
		}
		current.cancel()
		s.mu.Unlock()
		select {
		case <-current.done:
			return s.Status(ctx)
		case <-ctx.Done():
			return ChatGPTAuthStatus{}, ctx.Err()
		}
	case "models":
		auth, err := s.client.Credentials(ctx, s.store, request.AccountID)
		if err == nil {
			err = s.fetchModels(ctx, auth)
		}
		if err != nil {
			return ChatGPTAuthStatus{}, apperror.New(apperror.CodeUnavailable, chatGPTErrorMessage(err, "无法读取 ChatGPT 模型，请重试或重新登录"))
		}
		if _, err := s.registry.Reload(ctx, s.settings); err != nil {
			return ChatGPTAuthStatus{}, apperror.New(apperror.CodeUnavailable, "模型已保存，但模型列表尚未更新，请重试")
		}
	case "logout":
		_, err := s.registry.MutateProviderCredential(ctx, s.settings, modelregistry.ChatGPTProviderID(request.AccountID), func() error {
			return s.client.Logout(ctx, s.store, request.AccountID)
		})
		if err != nil {
			message := "ChatGPT 退出未完成，请重试"
			if auth, readErr := s.store.Selected(request.AccountID); readErr == nil && auth.AccessToken == "" && auth.RefreshToken == "" && auth.PendingRefresh == nil {
				message = "本地已退出；远程撤销未确认，请在 ChatGPT 设置中断开 Universal-Code"
			}
			return ChatGPTAuthStatus{}, apperror.New(apperror.CodeUnavailable, message)
		}
	case "usage":
		if s.openBrowser == nil || s.openBrowser(chatgptauth.UsageURL) != nil {
			return ChatGPTAuthStatus{}, apperror.New(apperror.CodeUnavailable, "无法打开系统浏览器，请访问 https://chatgpt.com/settings/usage")
		}
	default:
		return ChatGPTAuthStatus{}, apperror.New(apperror.CodeInvalidArgument, "ChatGPT 登录操作不存在")
	}
	return s.Status(ctx)
}

func (s *ChatGPTAuthService) start(ctx context.Context, account string) (ChatGPTAuthStatus, error) {
	s.mu.Lock()
	if s.closed || s.login != nil && s.login.status.State == "waiting" {
		s.mu.Unlock()
		return ChatGPTAuthStatus{}, apperror.New(apperror.CodeConflict, "已有 ChatGPT 登录正在进行或应用正在关闭")
	}
	// A browser callback must survive the initiating HTTP request and page changes.
	loginCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	current := &chatGPTLogin{status: ChatGPTLoginStatus{ID: uuid.NewString(), State: "waiting", AccountID: account},
		cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	s.login = current
	s.mu.Unlock()
	go s.runLogin(loginCtx, current, account)
	select {
	case <-current.ready:
		return s.Status(ctx)
	case <-current.done:
		return s.Status(ctx)
	case <-ctx.Done():
		return ChatGPTAuthStatus{}, ctx.Err()
	}
}

var chatGPTStartPath = regexp.MustCompile(`^/auth/start/[A-Za-z0-9_-]{43}$`)

// Only Go's one-time loopback entry point may be exposed to the renderer.
func ValidChatGPTStartURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && chatGPTStartPath.MatchString(u.Path)
}

func (s *ChatGPTAuthService) runLogin(ctx context.Context, current *chatGPTLogin, account string) {
	defer close(current.done)
	defer current.cancel()
	var ready sync.Once
	auth, err := s.client.Login(ctx, s.store, account, account == "", func(localURL string) {
		if !ValidChatGPTStartURL(localURL) {
			current.cancel()
			return
		}
		s.mu.Lock()
		current.status.StartURL = localURL
		s.mu.Unlock()
		ready.Do(func() { close(current.ready) })
		go func() {
			if ctx.Err() != nil {
				return
			}
			if s.openBrowser == nil || s.openBrowser(localURL) != nil {
				s.mu.Lock()
				if current.status.State == "waiting" {
					current.status.Message = "系统浏览器未打开，请复制登录链接到浏览器继续"
				}
				s.mu.Unlock()
			}
		}()
	})
	state, message, accountID := "failed", "", account
	if err == nil {
		accountID = auth.ClientID
		state = "succeeded"
		if modelErr := s.fetchModels(ctx, auth); modelErr != nil {
			message = chatGPTErrorMessage(modelErr, "已登录，模型列表读取失败，请点击刷新模型重试")
		}
		// Login already saved the verified credential. Finish invalidation even
		// when the user cancelled while model discovery was in flight.
		finishCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, reloadErr := s.registry.MutateProviderCredential(finishCtx, s.settings,
			modelregistry.ChatGPTProviderID(auth.ClientID), func() error { return nil })
		cancel()
		if reloadErr != nil {
			message = "登录已保存，但模型列表尚未更新，请点击刷新模型重试"
		}
	} else if errors.Is(err, context.Canceled) {
		state = "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		message = "登录等待超时，请重新登录"
	} else {
		message = chatGPTErrorMessage(err, "ChatGPT 登录未完成，请重试")
	}
	s.mu.Lock()
	current.status.State, current.status.Message = state, message
	current.status.AccountID, current.status.StartURL = accountID, ""
	s.mu.Unlock()
}

func (s *ChatGPTAuthService) fetchModels(ctx context.Context, auth *chatgptauth.Auth) error {
	models, err := s.client.Models(ctx, auth)
	if err != nil {
		return err
	}
	return s.store.SaveModels(ctx, auth, models)
}

func chatGPTErrorMessage(err error, fallback string) string {
	var oauth *chatgptauth.OAuthError
	if errors.As(err, &oauth) {
		switch oauth.Code {
		case "access_denied":
			return "ChatGPT 授权已取消，请重新登录并允许使用订阅额度"
		case "invalid_grant", "invalid_client", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
			return "ChatGPT 授权已失效，请重新登录"
		}
	}
	var api *chatgptauth.APIError
	if errors.As(err, &api) && api.Status == 429 {
		return "ChatGPT 当前额度或请求频率受限，请查看用量后重试"
	}
	return fallback
}

func (s *ChatGPTAuthService) Close() {
	s.mu.Lock()
	s.closed = true
	current := s.login
	if current != nil {
		current.cancel()
	}
	s.mu.Unlock()
	if current != nil {
		<-current.done
	}
}
