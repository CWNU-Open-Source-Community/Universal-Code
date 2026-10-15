package store

import (
	"crypto/ed25519"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/plugins"
)

// TestPluginPublisherTrustControlUnblocksRevokedPublisher covers the deadlock in
// issue #334: a revoked publisher can only be re-trusted through CLI today. The
// bounded control path must bind the exact fingerprint and generation, and the
// existing approve/enable refusal must clear once trust is granted again.
func TestPluginPublisherTrustControlUnblocksRevokedPublisher(t *testing.T) {
	ctx := t.Context()
	state, err := Open(t.TempDir() + "/publisher-trust-control.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	service, err := plugins.NewService(state)
	if err != nil {
		t.Fatal(err)
	}
	_, publisherKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	installation := stageHookPluginFixture(t, ctx, service, "trust-control", "1.0.0", "", publisherKey)

	granted, err := service.TrustInstallationPublisher(ctx, installation.ID,
		installation.PublisherFingerprint, 0, "control reviewer")
	if err != nil || granted.Generation != 1 || granted.State != plugins.PublisherTrusted ||
		granted.ReviewedBy != "control reviewer" ||
		granted.Fingerprint != installation.PublisherFingerprint {
		t.Fatalf("bounded trust = %#v err = %v", granted, err)
	}

	if _, err := service.TrustInstallationPublisher(ctx, installation.ID,
		installation.PublisherFingerprint, 0, "stale reviewer"); err == nil ||
		apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale generation trust accepted: %v", err)
	}
	if _, err := service.TrustInstallationPublisher(ctx, installation.ID,
		repeatingString('f', 64), 1, "wrong fingerprint"); err == nil ||
		apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("mismatched fingerprint trust accepted: %v", err)
	}
	if _, err := service.TrustInstallationPublisher(ctx, installation.ID,
		installation.PublisherFingerprint, 1, " "); err == nil ||
		apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("blank review actor accepted: %v", err)
	}

	revoked, err := service.RevokeInstallationPublisher(ctx, installation.ID,
		installation.PublisherFingerprint, 1, "revoking reviewer")
	if err != nil || revoked.State != plugins.PublisherRevoked || revoked.Generation != 2 {
		t.Fatalf("revocation precondition = %#v err = %v", revoked, err)
	}
	blocked := stageHookPluginFixture(t, ctx, service, "trust-control-second", "1.0.0", "", publisherKey)
	if _, err := service.Review(ctx, blocked.ID, plugins.ReviewRequest{
		Action: plugins.ReviewApprove, ExpectedPackageFingerprint: blocked.PackageFingerprint,
		ExpectedGeneration: blocked.Generation, ConfirmUntrusted: true, ReviewedBy: "operator",
	}); err == nil || apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("revoked publisher approval was not refused: %v", err)
	}

	retrusted, err := service.TrustInstallationPublisher(ctx, installation.ID,
		installation.PublisherFingerprint, 2, "retrust reviewer")
	if err != nil || retrusted.Generation != 3 || retrusted.State != plugins.PublisherTrusted {
		t.Fatalf("retrust after revocation = %#v err = %v", retrusted, err)
	}
	current, err := state.GetPluginInstallation(ctx, blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	approved := reviewHookPluginFixture(t, ctx, service, current, plugins.ReviewApprove, true)
	enabled := reviewHookPluginFixture(t, ctx, service, approved, plugins.ReviewEnable, true)
	if enabled.State != plugins.StateEnabled {
		t.Fatalf("publisher stayed blocked after re-trust: %#v", enabled)
	}
}

func repeatingString(character byte, length int) string {
	value := make([]byte, length)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
