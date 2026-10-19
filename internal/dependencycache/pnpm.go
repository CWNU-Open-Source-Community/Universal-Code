package dependencycache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"

	"cyberagent-workbench/internal/projectprep"
)

const pnpmSeedManifest = "{\"private\":true}\n"
const pnpmSeedWorkspace = "packages: []\n"

// pnpmSeedBytes reserves seed bytes before the shared preparation byte limit is
// applied to tarballs. Invalid seeds must fail before any staging mutation.
func pnpmSeedBytes(plan projectprep.Plan) (int64, error) {
	if plan.Ecosystem != "pnpm" {
		if len(plan.PNPMSeedLock) != 0 {
			return 0, errors.New("pnpm seed requires a pnpm preparation plan")
		}
		for _, pkg := range plan.Packages {
			if pkg.Kind == projectprep.KindPNPMTarball {
				return 0, errors.New("pnpm artifact requires a pnpm preparation plan")
			}
		}
		return 0, nil
	}
	if len(plan.PNPMSeedLock) > projectprep.MaxFileBytes || len(bytes.TrimSpace(plan.PNPMSeedLock)) == 0 {
		return 0, errors.New("pnpm preparation requires a nonempty seed lockfile within its byte limit")
	}
	for _, pkg := range plan.Packages {
		if pkg.Kind != projectprep.KindPNPMTarball {
			return 0, errors.New("pnpm preparation contains a different artifact kind")
		}
	}
	return int64(len(plan.PNPMSeedLock) + len(pnpmSeedManifest) + len(pnpmSeedWorkspace)), nil
}

// The seed is separate from the project's original lockfile and manifests. The
// sandbox's pnpm consumes this lock only to populate its private package store.
func writePNPMSeed(ctx context.Context, root *os.Root, plan projectprep.Plan, authorityCheck func(context.Context) error) error {
	for _, file := range []struct {
		name string
		body []byte
	}{
		{"package.json", []byte(pnpmSeedManifest)},
		{"pnpm-workspace.yaml", []byte(pnpmSeedWorkspace)},
		{"pnpm-lock.yaml", plan.PNPMSeedLock},
	} {
		if err := authorityCheck(ctx); err != nil {
			return err
		}
		if err := writeAtomic(root, path.Join(projectprep.PNPMSeedDirectory(plan.Target), file.name), file.body, 0600); err != nil {
			return err
		}
	}
	return nil
}
