package desktop

import "testing"

func TestChatGPTBrowserRejectsGeneralURLs(t *testing.T) {
	for _, target := range []string{
		"https://example.com", "file:///C:/Windows/System32/cmd.exe", "javascript:alert(1)",
		"https://chatgpt.com/settings/usage?redirect=other", "http://127.0.0.1:8080/admin",
		"http://127.0.0.1:8080/auth/start/short", "https://auth.openai.com/api/accounts/authorize?state=x",
	} {
		if err := openChatGPTBrowser(target); err == nil {
			t.Fatalf("opened unexpected browser URL %q", target)
		}
	}
}
