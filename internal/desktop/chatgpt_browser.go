package desktop

import (
	"errors"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/chatgptauth"
)

func openChatGPTBrowser(target string) error {
	if target != chatgptauth.UsageURL && !application.ValidChatGPTStartURL(target) {
		return errors.New("invalid ChatGPT browser target")
	}
	return launchChatGPTBrowser(target)
}
