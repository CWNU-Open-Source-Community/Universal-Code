package dependencycache

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"cyberagent-workbench/internal/projectprep"
)

func pythonSeedBytes(plan projectprep.Plan) (int64, error) {
	if plan.Ecosystem != "python" {
		if len(plan.PythonSeed) != 0 {
			return 0, errors.New("Python installation input requires a Python plan")
		}
		for _, pkg := range plan.Packages {
			if pkg.Kind == projectprep.KindPythonWheel {
				return 0, errors.New("Python wheel requires a Python plan")
			}
		}
		return 0, nil
	}
	if len(plan.PythonSeed) == 0 || len(plan.PythonSeed) > projectprep.MaxFileBytes || !json.Valid(plan.PythonSeed) {
		return 0, errors.New("Python preparation needs bounded installation input")
	}
	for _, pkg := range plan.Packages {
		if pkg.Kind != projectprep.KindPythonWheel {
			return 0, errors.New("Python preparation contains a different artifact kind")
		}
	}
	return int64(len(plan.PythonSeed)), nil
}

func writePythonSeed(ctx context.Context, root *os.Root, plan projectprep.Plan, check func(context.Context) error) error {
	if err := check(ctx); err != nil {
		return err
	}
	return writeAtomic(root, projectprep.PythonPlanFile(plan.Target), plan.PythonSeed, 0o600)
}
