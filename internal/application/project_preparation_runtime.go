package application

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/projectprep"
)

func (s *CommandRuntimeService) projectPreparationReady(ctx context.Context, ecosystem ...string) error {
	if len(ecosystem) == 1 && ecosystem[0] == "yarn" {
		if executor, ok := s.sandbox.(*DockerSandboxCommandRuntimeExecutor); ok && s.adapter.Backend == CommandRuntimeDockerSandboxBackend {
			if probe, ok := executor.service.docker.readiness.(interface {
				YarnPreparationAvailable(context.Context, string) (bool, error)
			}); ok {
				ready, err := probe.YarnPreparationAvailable(ctx, executor.service.imageDigest)
				if err != nil {
					return apperror.New(apperror.CodeUnavailable, "Docker Yarn preparation image inspection failed")
				}
				if ready {
					return nil
				}
			}
		}
		return apperror.New(apperror.CodeFailedPrecondition, "Yarn preparation requires the Standard Code Docker image with Yarn "+projectprep.YarnVersion)
	}
	pnpm := len(ecosystem) == 1 && ecosystem[0] == "pnpm"
	if s.adapter.Backend != CommandRuntimeDockerSandboxBackend {
		if pnpm {
			return apperror.New(apperror.CodeFailedPrecondition, "pnpm workspace preparation requires the Standard Code Docker image with pnpm "+projectprep.PNPMVersion)
		}
		return nil
	}
	executor, ok := s.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "Docker project preparation is unavailable")
	}
	if pnpm {
		probe, ok := executor.service.docker.readiness.(interface {
			PNPMPreparationAvailable(context.Context, string) (bool, error)
		})
		if ok {
			ready, err := probe.PNPMPreparationAvailable(ctx, executor.service.imageDigest)
			if err != nil {
				return apperror.New(apperror.CodeUnavailable, "Docker pnpm preparation image inspection failed")
			}
			if ready {
				return nil
			}
		}
		return apperror.New(apperror.CodeFailedPrecondition, "rebuild the Standard Code Docker image with pnpm "+projectprep.PNPMVersion+" to enable pnpm workspace preparation")
	}
	probe, ok := executor.service.docker.readiness.(interface {
		ProjectPreparationAvailable(context.Context, string) (bool, error)
	})
	if ok {
		ready, err := probe.ProjectPreparationAvailable(ctx, executor.service.imageDigest)
		if err != nil {
			return apperror.New(apperror.CodeUnavailable, "Docker project preparation image inspection failed")
		}
		if ready {
			return nil
		}
	}
	return apperror.New(apperror.CodeFailedPrecondition, "rebuild the Standard Code Docker image to enable dependency preparation")
}
