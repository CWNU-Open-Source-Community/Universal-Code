//go:build desktop

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/app"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/desktop"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	internalRiskRestartFlag       = "--internal-risk-restart"
	internalRiskRestartProfile    = "--internal-risk-profile="
	internalRiskRestartParentPID  = "--internal-risk-parent-pid="
	internalRiskRestartReady      = "--internal-risk-ready="
	internalRiskRestartReadyToken = "--internal-risk-ready-token="
	riskRestartReadyProtocol      = "desktop_risk_restart.ready.v1"
)

var riskRestartReadyTimeout = 10 * time.Second

type riskRestartHelperOptions struct {
	profile         desktop.DesktopRiskProfile
	parentPID       int
	readyDescriptor string
	readyToken      string
}

func parseRiskRestartHelperOptions(args []string) (riskRestartHelperOptions, bool, error) {
	internal := false
	for _, argument := range args {
		if argument == internalRiskRestartFlag ||
			strings.HasPrefix(argument, internalRiskRestartProfile) ||
			strings.HasPrefix(argument, internalRiskRestartParentPID) ||
			strings.HasPrefix(argument, internalRiskRestartReady) ||
			strings.HasPrefix(argument, internalRiskRestartReadyToken) {
			internal = true
			break
		}
	}
	if !internal {
		return riskRestartHelperOptions{}, false, nil
	}
	if len(args) != 5 {
		return riskRestartHelperOptions{}, true,
			errors.New("internal risk restart requires exactly its fixed arguments")
	}
	var restartSeen, profileSeen, parentSeen, readySeen, readyTokenSeen bool
	var options riskRestartHelperOptions
	for _, argument := range args {
		switch {
		case argument == internalRiskRestartFlag:
			if restartSeen {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart arguments contain a duplicate")
			}
			restartSeen = true
		case strings.HasPrefix(argument, internalRiskRestartProfile):
			if profileSeen {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart arguments contain a duplicate")
			}
			profileSeen = true
			options.profile = desktop.DesktopRiskProfile(
				strings.TrimPrefix(argument, internalRiskRestartProfile))
		case strings.HasPrefix(argument, internalRiskRestartParentPID):
			if parentSeen {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart arguments contain a duplicate")
			}
			parentSeen = true
			value := strings.TrimPrefix(argument, internalRiskRestartParentPID)
			parsed, err := strconv.ParseInt(value, 10, 32)
			if err != nil || parsed <= 0 || int(parsed) == os.Getpid() {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart parent process is invalid")
			}
			options.parentPID = int(parsed)
		case strings.HasPrefix(argument, internalRiskRestartReady):
			if readySeen {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart arguments contain a duplicate")
			}
			readySeen = true
			options.readyDescriptor = strings.TrimPrefix(argument, internalRiskRestartReady)
			if !validRiskRestartReadyDescriptor(options.readyDescriptor) {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart ready channel is invalid")
			}
		case strings.HasPrefix(argument, internalRiskRestartReadyToken):
			if readyTokenSeen {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart arguments contain a duplicate")
			}
			readyTokenSeen = true
			options.readyToken = strings.TrimPrefix(argument, internalRiskRestartReadyToken)
			if !validRiskRestartReadyToken(options.readyToken) {
				return riskRestartHelperOptions{}, true,
					errors.New("internal risk restart ready token is invalid")
			}
		default:
			return riskRestartHelperOptions{}, true,
				errors.New("internal risk restart rejects additional arguments")
		}
	}
	if !restartSeen || !profileSeen || !parentSeen || !readySeen || !readyTokenSeen ||
		!options.profile.Valid() {
		return riskRestartHelperOptions{}, true,
			errors.New("internal risk restart arguments are invalid")
	}
	return options, true, nil
}

func riskRestartHelperArguments(profile desktop.DesktopRiskProfile,
	parentPID int,
	readyDescriptor string,
	readyToken string,
) ([]string, error) {
	if !profile.Valid() || parentPID <= 0 ||
		!validRiskRestartReadyDescriptor(readyDescriptor) ||
		!validRiskRestartReadyToken(readyToken) {
		return nil, errors.New("desktop risk restart target is invalid")
	}
	return []string{
		internalRiskRestartFlag,
		internalRiskRestartProfile + string(profile),
		internalRiskRestartParentPID + strconv.Itoa(parentPID),
		internalRiskRestartReady + readyDescriptor,
		internalRiskRestartReadyToken + readyToken,
	}, nil
}

func desktopOptionsForRiskProfile(profile desktop.DesktopRiskProfile) (desktopOptions, error) {
	if !profile.Valid() {
		return desktopOptions{}, errors.New("desktop risk profile is invalid")
	}
	config := desktopOptions{riskProfileRestart: true, sandboxSettings: true}
	enableSafeDesktopProductBundle(&config)
	if profile == desktop.DesktopRiskProfileDebug || profile == desktop.DesktopRiskProfileSandboxDebug || profile == desktop.DesktopRiskProfileVerificationDebug {
		config.userTerminal = true
	}
	return config, nil
}

func completeRiskRestartHelperHandshake(options riskRestartHelperOptions) error {
	parentWaiter, err := prepareRiskRestartParent(options.parentPID)
	if err != nil {
		return err
	}
	defer parentWaiter.Close()
	// The old process will not quit until this helper has authenticated the
	// parent and acquired a durable OS wait capability. The inherited ready
	// channel is created internally and is never accepted from the renderer.
	if err := signalRiskRestartReady(options.readyDescriptor, options.readyToken); err != nil {
		return err
	}
	return parentWaiter.Wait()
}

type nativeRiskProfileRestarter struct {
	confirm func(context.Context, desktop.DesktopRiskProfile) (bool, error)
	start   func(desktop.DesktopRiskProfile, int) error
	quit    func(context.Context)
}

func newNativeRiskProfileRestarter() *nativeRiskProfileRestarter {
	return &nativeRiskProfileRestarter{
		confirm: confirmDesktopRiskRestart,
		start:   startDesktopRiskRestartHelper,
		quit:    runtime.Quit,
	}
}

func (r *nativeRiskProfileRestarter) ConfirmAndRestart(ctx context.Context,
	profile desktop.DesktopRiskProfile,
) (bool, error) {
	if r == nil || r.confirm == nil || r.start == nil || r.quit == nil {
		return false, errors.New("native desktop risk restart is unavailable")
	}
	if ctx == nil || ctx.Err() != nil || !profile.Valid() {
		return false, errors.New("native desktop risk restart context is invalid")
	}
	confirmed, err := r.confirm(ctx, profile)
	if err != nil {
		return false, err
	}
	if !confirmed {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := r.start(profile, os.Getpid()); err != nil {
		return false, err
	}
	// start returns only after the helper has validated this exact parent and
	// acquired its OS wait capability. Quit must never run on a timeout or a
	// failed handshake, so the current safe process remains usable.
	r.quit(ctx)
	return true, nil
}

func confirmDesktopRiskRestart(ctx context.Context,
	profile desktop.DesktopRiskProfile,
) (bool, error) {
	options, err := desktopRiskRestartDialogOptions(profile)
	if err != nil {
		return false, err
	}
	return confirmDesktopRiskDialog(ctx, options, "确认并重启")
}

func desktopRiskRestartDialogOptions(profile desktop.DesktopRiskProfile) (
	runtime.MessageDialogOptions, error,
) {
	message := ""
	switch profile {
	case desktop.DesktopRiskProfileSandbox, desktop.DesktopRiskProfileSandboxDebug:
		message = "重启以应用已保存的 Local、Docker Engine 和 Docker Sandboxes 环境设置。\n\n" +
			"正在运行的任务会先停止并清理。重启后，选择任务的执行环境并按审批偏好继续工作。"
	case desktop.DesktopRiskProfileDebug:
		message = "重启以启用用户终端。终端由用户所有，Agent 输入默认关闭。\n\n" +
			"Agent 输入仍需要当前任务的 Full 激活、Debug 交互和单独的限时终端授权。" +
			"重启不会恢复已保存任务的执行权；终端撤销、取消和退出清理继续生效。"
	case desktop.DesktopRiskProfileVerification, desktop.DesktopRiskProfileVerificationDebug:
		message = "重启以应用已保存的浏览器验证和批量验证偏好。正在运行的任务会先停止并清理。\n\n" +
			"重启保留当前用户终端的启用状态。具体执行仍需当前任务权限、执行环境及各项检查；重启不会恢复旧执行权。"
	default:
		return runtime.MessageDialogOptions{}, errors.New("desktop risk profile is invalid")
	}
	return runtime.MessageDialogOptions{
		Type:          runtime.WarningDialog,
		Title:         app.Name + " 高风险能力",
		Message:       message,
		Buttons:       []string{"确认并重启", "取消"},
		DefaultButton: "取消",
		CancelButton:  "取消",
	}, nil
}

type nativeVerificationSettingsConfirmer struct{}

func (nativeVerificationSettingsConfirmer) ConfirmVerificationSettings(ctx context.Context, settings application.DesktopVerificationSettings) (bool, error) {
	if ctx == nil || ctx.Err() != nil {
		return false, errors.New("native verification settings context is invalid")
	}
	return confirmDesktopRiskDialog(ctx, desktopVerificationSettingsDialogOptions(settings), "确认保存")
}

func desktopVerificationSettingsDialogOptions(settings application.DesktopVerificationSettings) runtime.MessageDialogOptions {
	choice := func(enabled bool) string {
		if enabled {
			return "启用"
		}
		return "关闭"
	}
	return runtime.MessageDialogOptions{Type: runtime.WarningDialog, Title: app.Name + " 验证能力设置",
		Message: "保存以下偏好，用于以后普通启动和受管重启：\n\n" +
			"浏览器验证：" + choice(settings.UIEvidenceEnabled) + "\n批量验证：" + choice(settings.BatchValidationEnabled) +
			"\n\n保存后当前应用继续运行；可准备完成后再重启应用以生效。" +
			"启用后，浏览器验证可启动项目服务和受管浏览器，批量验证可在任务所选环境运行 Go/npm 检查。" +
			"具体执行仍需当前任务权限及各项检查；保存偏好和重启都不会恢复旧执行权。",
		Buttons: []string{"确认保存", "取消"}, DefaultButton: "取消", CancelButton: "取消"}
}

func startDesktopRiskRestartHelper(profile desktop.DesktopRiskProfile, parentPID int) error {
	ready, err := newRiskRestartReadyChannel()
	if err != nil {
		return errors.New("desktop restart ready channel is unavailable")
	}
	defer ready.close()
	arguments, err := riskRestartHelperArguments(profile, parentPID,
		ready.descriptor(), ready.token)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("desktop executable is unavailable")
	}
	executable, err = filepath.Abs(filepath.Clean(executable))
	if err != nil || !filepath.IsAbs(executable) {
		return errors.New("desktop executable is invalid")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("desktop executable is unavailable")
	}
	command := exec.Command(executable, arguments...)
	command.Dir = filepath.Dir(executable)
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	configureRiskRestartHelperCommand(command, ready)
	if err := command.Start(); err != nil {
		return fmt.Errorf("desktop restart helper could not start: %w", err)
	}
	abort := func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	if err := ready.parentStarted(); err != nil {
		abort()
		return errors.New("desktop restart ready channel could not be transferred")
	}
	if err := ready.wait(riskRestartReadyTimeout); err != nil {
		abort()
		return fmt.Errorf("desktop restart helper did not become ready: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		abort()
		return errors.New("desktop restart helper could not be released")
	}
	return nil
}

type riskRestartReadyChannel struct {
	reader *os.File
	writer *os.File
	value  string
	token  string
}

func (c *riskRestartReadyChannel) descriptor() string {
	if c == nil {
		return ""
	}
	return c.value
}

func (c *riskRestartReadyChannel) parentStarted() error {
	if c == nil || c.writer == nil {
		return errors.New("desktop restart ready writer is unavailable")
	}
	err := c.writer.Close()
	c.writer = nil
	return err
}

func (c *riskRestartReadyChannel) wait(timeout time.Duration) error {
	if c == nil || c.reader == nil || timeout <= 0 {
		return errors.New("desktop restart ready reader is unavailable")
	}
	result := make(chan error, 1)
	readyMessage := riskRestartReadyMessage(c.token)
	go func() {
		buffer := make([]byte, len(readyMessage))
		_, err := io.ReadFull(c.reader, buffer)
		if err == nil && string(buffer) != readyMessage {
			err = errors.New("desktop restart helper sent an invalid ready message")
		}
		result <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		_ = c.reader.Close()
		c.reader = nil
		<-result
		return errors.New("desktop restart helper ready handshake timed out")
	}
}

func (c *riskRestartReadyChannel) close() {
	if c == nil {
		return
	}
	if c.reader != nil {
		_ = c.reader.Close()
		c.reader = nil
	}
	if c.writer != nil {
		_ = c.writer.Close()
		c.writer = nil
	}
}

func newRiskRestartReadyToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func validRiskRestartReadyToken(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func riskRestartReadyMessage(token string) string {
	return riskRestartReadyProtocol + " " + token + "\n"
}

func writeRiskRestartReady(writer *os.File, token string) error {
	if writer == nil || !validRiskRestartReadyToken(token) {
		return errors.New("desktop restart ready writer is unavailable")
	}
	defer writer.Close()
	message := riskRestartReadyMessage(token)
	written, err := io.WriteString(writer, message)
	if err != nil || written != len(message) {
		return errors.New("desktop restart helper could not signal readiness")
	}
	return nil
}
