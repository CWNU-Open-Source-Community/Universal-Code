package application

import (
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
)

func checkRunProjectPaths(run domain.Run, paths ...string) error {
	policy, err := projectconfig.PathPolicy(run.Config)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path != "" {
			if err := policy.Check(path); err != nil {
				return err
			}
		}
	}
	return nil
}
