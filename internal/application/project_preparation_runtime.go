package application

import (
	"context"
	"cyberagent-workbench/internal/apperror"
)

func (s *CommandRuntimeService) projectPreparationReady(ctx context.Context) error {
	if s.adapter.Backend != CommandRuntimeDockerSandboxBackend {
		return nil
	}
	executor, ok := s.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "Docker project preparation is unavailable")
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
