//go:build desktop

package main

import (
	"context"
	"fmt"

	"cyberagent-workbench/internal/application"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type nativeTerminalSettingsConfirmer struct{}

func (nativeTerminalSettingsConfirmer) ConfirmTerminalSettings(ctx context.Context, settings application.DesktopTerminalSettings) (bool, error) {
	mode := map[string]string{"disabled": "禁止 Agent 操作内置终端", "manual": "手动限时授权", "full": "完全允许：任务具有完全访问权限时自动允许 Agent 操作内置终端"}[settings.AgentControl]
	return confirmDesktopRiskDialog(ctx, runtime.MessageDialogOptions{Type: runtime.QuestionDialog, Title: "终端设置",
		Message: fmt.Sprintf("保存以下终端设置？\n\n%s\n手动授权时长：%d 秒\n新终端使用：%s\n\n完全允许时，Agent 可以读取此终端输出并提交命令；可随时撤销。此设置不授予 Windows 管理员权限。已有终端继续使用原来的 shell。", mode, settings.ManualTTLSeconds, settings.DefaultShell),
		Buttons: []string{"取消", "确认保存"}, DefaultButton: "取消", CancelButton: "取消"}, "确认保存")
}
