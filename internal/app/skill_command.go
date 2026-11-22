package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/toolgateway"
)

func (a *App) skillCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("skill subcommand is required")
	}
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		return err
	}
	switch args[0] {
	case "discover":
		return a.skillDiscoverCommand(ctx, args[1:])
	case "list":
		flags := newFlagSet("skill list", a.errOut)
		profileValue := flags.String("profile", "", "filter by compatible Profile")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: cyberagent skill list [--profile code|review|learn|script]")
		}
		var profile domain.Profile
		if strings.TrimSpace(*profileValue) != "" {
			profile, err = domain.ParseProfile(*profileValue)
			if err != nil {
				return err
			}
		}
		for _, manifest := range registry.List(profile) {
			userInvocable, modelInvocable, explicitOnly := manifest.InvocationPolicy()
			fmt.Fprintf(a.out, "%s@%s\tprofiles=%s\tsurfaces=%s\tphases=%s\troles=%s\tuser_invocable=%t\tmodel_invocable=%t\texplicit_only=%t\ttools=%s\tcontent=%s\tbytes=%d\ttoken_upper_bound=%d\n",
				manifest.Name, manifest.Version, joinProfiles(manifest.Profiles),
				joinSurfaces(manifest.Surfaces), joinPhases(manifest.Phases), joinRoles(manifest.Roles),
				userInvocable, modelInvocable, explicitOnly,
				joinToolDependencies(manifest.ToolDependencies),
				manifest.ContentPath, manifest.ContentBytes, manifest.ContentTokenUpperBound)
		}
		printSkillBoundary(a)
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: cyberagent skill show <name>")
		}
		manifest, ok := registry.Get(args[1])
		if !ok {
			return fmt.Errorf("skill %q not found", args[1])
		}
		userInvocable, modelInvocable, explicitOnly := manifest.InvocationPolicy()
		fmt.Fprintf(a.out, "protocol: %s\nname: %s\nversion: %s\ndescription: %s\nprofiles: %s\nsurfaces: %s\nphases: %s\nroles: %s\nuser_invocable: %t\nmodel_invocable: %t\nexplicit_only: %t\ntool_dependencies: %s\ncontent_path: %s\ncontent_sha256: %s\ncontent_bytes: %d\ncontent_token_upper_bound: %d\n",
			manifest.Protocol, manifest.Name, manifest.Version, manifest.Description, joinProfiles(manifest.Profiles),
			joinSurfaces(manifest.Surfaces), joinPhases(manifest.Phases), joinRoles(manifest.Roles),
			userInvocable, modelInvocable, explicitOnly, joinToolDependencies(manifest.ToolDependencies),
			manifest.ContentPath, manifest.ContentSHA256,
			manifest.ContentBytes, manifest.ContentTokenUpperBound)
		printSkillBoundary(a)
		return nil
	case "validate":
		if len(args) != 1 {
			return errors.New("usage: cyberagent skill validate")
		}
		if err := registry.Validate(); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "validated %d built-in %s manifests\n", len(registry.List("")), skills.ProtocolVersion)
		printSkillBoundary(a)
		return nil
	case "package":
		if len(args) != 3 || args[1] != "validate" {
			return errors.New("usage: cyberagent skill package validate <package.zip>")
		}
		preview, err := skills.ValidatePackageFile(ctx, args[2])
		if err != nil {
			return err
		}
		printSkillPackagePreview(a, preview)
		return nil
	case "import":
		flags := newFlagSet("skill import", a.errOut)
		surfaceValue := flags.String("surface", "", "target catalog surface")
		operationKey := flags.String("operation-key", "", "stable idempotency key")
		installedBy := flags.String("operator", "cli_operator", "operator identity")
		confirmed := flags.Bool("confirm-untrusted-skill", false,
			"explicitly confirm installation of untrusted instructions")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{
			"surface": true, "operation-key": true, "operator": true,
			"confirm-untrusted-skill": false,
		})); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: cyberagent skill import <package.zip> --surface code|cyber --operation-key <stable-key> --confirm-untrusted-skill [--operator cli_operator]")
		}
		surface, err := domain.ParseExecutionSurface(*surfaceValue)
		if err != nil {
			return err
		}
		raw, err := skills.ReadPackageFile(ctx, flags.Arg(0))
		if err != nil {
			return err
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		objects, err := skills.NewLocalPackageObjectStore(a.home)
		if err != nil {
			return err
		}
		result, err := application.NewSkillPackageRegistryService(a.store, objects, registry).
			Import(ctx, application.ImportSkillPackageRequest{
				Raw: raw, Surface: surface, OperationKey: *operationKey,
				InstalledBy: *installedBy, ConfirmUntrusted: *confirmed,
			})
		if err != nil {
			return err
		}
		if result.Installation != nil {
			return a.writeStagedPlugin(*result.Installation, result.Replayed)
		}
		printInstalledSkillPackage(a, result.Package)
		fmt.Fprintf(a.out, "replayed: %t\nrecovered_pending: %t\n",
			result.Replayed, result.RecoveredPending)
		return nil
	case "installed":
		if len(args) >= 2 && args[1] == "show" {
			if len(args) != 3 {
				return errors.New("usage: cyberagent skill installed show <name>@<version>")
			}
			name, version, err := skills.ParseInstalledPackageRef(args[2])
			if err != nil {
				return err
			}
			if err := a.ensureStore(); err != nil {
				return err
			}
			objects, err := skills.NewLocalPackageObjectStore(a.home)
			if err != nil {
				return err
			}
			value, err := application.NewSkillPackageRegistryService(a.store, objects, registry).
				Get(ctx, name, version)
			if err != nil {
				return err
			}
			printInstalledSkillPackage(a, value)
			return nil
		}
		flags := newFlagSet("skill installed", a.errOut)
		surfaceValue := flags.String("surface", "", "filter by code or cyber catalog")
		profileValue := flags.String("profile", "", "filter by compatible Profile")
		includeRemoved := flags.Bool("include-removed", false, "include removal tombstones")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: cyberagent skill installed [--surface code|cyber] [--profile code|review|learn|script] [--include-removed]")
		}
		var surface domain.ExecutionSurface
		if strings.TrimSpace(*surfaceValue) != "" {
			surface, err = domain.ParseExecutionSurface(*surfaceValue)
			if err != nil {
				return err
			}
		}
		var profile domain.Profile
		if strings.TrimSpace(*profileValue) != "" {
			profile, err = domain.ParseProfile(*profileValue)
			if err != nil {
				return err
			}
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		objects, err := skills.NewLocalPackageObjectStore(a.home)
		if err != nil {
			return err
		}
		values, err := application.NewSkillPackageRegistryService(a.store, objects, registry).
			List(ctx, application.ListInstalledSkillPackagesRequest{
				Surface: surface, Profile: profile, IncludeRemoved: *includeRemoved,
			})
		if err != nil {
			return err
		}
		for _, value := range values {
			status := "installed"
			if value.Removal != nil {
				status = "removed"
			}
			fmt.Fprintf(a.out, "%s\tsurface=%s\tprofiles=%s\tstatus=%s\ttrust=%s\tarchive_sha256=%s\n",
				skills.FormatInstalledPackageRef(value.Installation.Name, value.Installation.Version),
				value.Installation.Surface, joinProfiles(value.Installation.Manifest.Profiles), status,
				value.Installation.TrustClass, value.Installation.ArchiveSHA256)
		}
		fmt.Fprintf(a.out, "installed_count: %d\n", len(values))
		printExternalSkillBoundary(a)
		return nil
	case "candidates":
		return a.skillCandidateCommand(ctx, append([]string{"list"}, args[1:]...), registry)
	case "candidate":
		return a.skillCandidateCommand(ctx, args[1:], registry)
	case "remove":
		flags := newFlagSet("skill remove", a.errOut)
		operationKey := flags.String("operation-key", "", "stable idempotency key")
		removedBy := flags.String("operator", "cli_operator", "operator identity")
		confirmed := flags.Bool("confirm-remove", false,
			"confirm immutable removal tombstone")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{
			"operation-key": true, "operator": true, "confirm-remove": false,
		})); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: cyberagent skill remove <name>@<version> --operation-key <stable-key> --confirm-remove [--operator cli_operator]")
		}
		name, version, err := skills.ParseInstalledPackageRef(flags.Arg(0))
		if err != nil {
			return err
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		objects, err := skills.NewLocalPackageObjectStore(a.home)
		if err != nil {
			return err
		}
		result, err := application.NewSkillPackageRegistryService(a.store, objects, registry).
			Remove(ctx, application.RemoveSkillPackageRequest{
				Name: name, Version: version, OperationKey: *operationKey,
				RemovedBy: *removedBy, ConfirmRemove: *confirmed,
			})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "removal_id: %s\nskill: %s\nsurface: %s\ntrust_class: %s\npackage_object_retained: %t\nhistorical_recovery_preserved: %t\nfuture_selection_enabled: %t\nrun_selection_authorized: %t\ncontext_injection_authorized: %t\ntool_capability_grant: %t\nremoved_by: %s\ncreated_at: %s\nreplayed: %t\n",
			result.Removal.ID, skills.FormatInstalledPackageRef(result.Removal.Name, result.Removal.Version),
			result.Removal.Surface, skills.PackageTrustOperatorInstalledUntrusted,
			result.Removal.PackageObjectRetained, result.Removal.HistoricalRecoveryPreserved,
			result.Removal.FutureSelectionEnabled, result.Removal.RunSelectionAuthorized,
			result.Removal.ContextInjectionAuthorized, result.Removal.ToolCapabilityGrant,
			result.Removal.RemovedBy, result.Removal.CreatedAt.Format(time.RFC3339Nano),
			result.Replayed)
		return nil
	case "select-external":
		flags := newFlagSet("skill select-external", a.errOut)
		tokenBudget := flags.Int("token-budget", skills.DefaultExternalSelectionTokenBudget,
			"separate conservative external context token budget")
		operationKey := flags.String("operation-key", "", "stable idempotency key")
		requestedBy := flags.String("operator", "cli_operator", "operator identity")
		specialistRef := flags.String("specialist", "",
			"at most one selected package allowed in Specialist context")
		confirmed := flags.Bool("confirm-untrusted-skill-context", false,
			"confirm Run-scoped delivery of untrusted Skill instructions")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{
			"token-budget": true, "operation-key": true, "operator": true,
			"specialist": true, "confirm-untrusted-skill-context": false,
		})); err != nil {
			return err
		}
		if flags.NArg() < 2 {
			return errors.New("usage: cyberagent skill select-external <run-id> <name>@<version> [name@version...] --operation-key <stable-key> --confirm-untrusted-skill-context [--specialist name@version] [--token-budget 2048] [--operator cli_operator]")
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		result, err := application.NewExternalSkillSelectionService(a.store).Select(ctx,
			application.SelectExternalSkillsRequest{
				RunID: flags.Arg(0), PackageRefs: flags.Args()[1:],
				SpecialistRef: *specialistRef, TokenBudget: *tokenBudget,
				OperationKey: *operationKey, RequestedBy: *requestedBy,
				ConfirmUntrustedContext: *confirmed,
			})
		if err != nil {
			return err
		}
		printExternalSkillSelection(a, result.Selection)
		fmt.Fprintf(a.out, "replayed: %t\n", result.Replayed)
		return nil
	case "external-selection":
		if len(args) != 2 {
			return errors.New("usage: cyberagent skill external-selection <run-id>")
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		selection, err := application.NewExternalSkillSelectionService(a.store).
			GetForRun(ctx, args[1])
		if err != nil {
			return err
		}
		printExternalSkillSelection(a, selection)
		return nil
	case "select":
		flags := newFlagSet("skill select", a.errOut)
		tokenBudget := flags.Int("token-budget", skills.DefaultSelectionTokenBudget,
			"conservative aggregate token budget")
		operationKey := flags.String("operation-key", "", "stable idempotency key")
		requestedBy := flags.String("operator", "cli_operator", "operator identity")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{
			"token-budget": true, "operation-key": true, "operator": true,
		})); err != nil {
			return err
		}
		if flags.NArg() < 2 {
			return errors.New("usage: cyberagent skill select <run-id> <name> [name...] --operation-key <stable-key> [--token-budget 4096] [--operator cli_operator]")
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		result, err := application.NewSkillSelectionService(a.store, registry).Select(ctx,
			application.SelectSkillsRequest{
				RunID: flags.Arg(0), Names: flags.Args()[1:], TokenBudget: *tokenBudget,
				OperationKey: *operationKey, RequestedBy: *requestedBy,
			})
		if err != nil {
			return err
		}
		printSkillSelection(a, result.Selection)
		fmt.Fprintf(a.out, "replayed: %t\n", result.Replayed)
		printSkillBoundary(a)
		return nil
	case "selection":
		if len(args) != 2 {
			return errors.New("usage: cyberagent skill selection <run-id>")
		}
		if err := a.ensureStore(); err != nil {
			return err
		}
		selection, err := application.NewSkillSelectionService(a.store, registry).
			GetForRun(ctx, args[1])
		if err != nil {
			return err
		}
		printSkillSelection(a, selection)
		printSkillBoundary(a)
		return nil
	case "catalog":
		return a.skillCatalogCommand(ctx, args[1:], registry)
	case "import-url":
		return a.skillImportSourceCommand(ctx, args[1:], "url", registry)
	case "import-git":
		return a.skillImportSourceCommand(ctx, args[1:], "git", registry)
	case "import-dir":
		return a.skillImportSourceCommand(ctx, args[1:], "dir", registry)
	default:
		return fmt.Errorf("unknown skill subcommand %q", args[0])
	}
}

func (a *App) skillCatalogService(registry *skills.Registry) (*application.SkillCatalogService, *skills.LocalPackageObjectStore, error) {
	if err := a.ensureStore(); err != nil {
		return nil, nil, err
	}
	objects, err := skills.NewLocalPackageObjectStore(a.home)
	if err != nil {
		return nil, nil, err
	}
	registryService := application.NewSkillPackageRegistryService(a.store, objects, registry)
	return application.NewSkillCatalogService(a.store, registryService), objects, nil
}

func (a *App) skillCatalogCommand(ctx context.Context, args []string, registry *skills.Registry) error {
	if len(args) == 0 {
		return errors.New("usage: cyberagent skill catalog list|trust|revoke|pin|enable|disable|audit")
	}
	service, _, err := a.skillCatalogService(registry)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: cyberagent skill catalog list")
		}
		publishers, err := service.ListPublishers(ctx)
		if err != nil {
			return err
		}
		for _, publisher := range publishers {
			fmt.Fprintf(a.out, "publisher\t%s\tteam=%s\ttrust=%s\tfingerprint=%s\ttrusted_by=%s\n",
				publisher.Name, publisher.Team, publisher.TrustClass, publisher.Fingerprint, publisher.TrustedBy)
		}
		pins, err := service.ListPins(ctx)
		if err != nil {
			return err
		}
		for _, pin := range pins {
			fmt.Fprintf(a.out, "pin\t%s@%s\tsurface=%s\tenabled=%t\tpinned_by=%s\n",
				pin.SkillName, pin.Version, pin.Surface, pin.Enabled, pin.PinnedBy)
		}
		fmt.Fprintf(a.out, "publisher_count: %d\npin_count: %d\n", len(publishers), len(pins))
		return nil
	case "trust":
		flags := newFlagSet("skill catalog trust", a.errOut)
		team := flags.String("team", "", "publisher team label")
		operator := flags.String("operator", "cli_operator", "operator identity")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{"team": true, "operator": true})); err != nil {
			return err
		}
		if flags.NArg() != 2 {
			return errors.New("usage: cyberagent skill catalog trust <publisher-name> <base64-public-key> [--team label] [--operator cli_operator]")
		}
		publisher, err := service.TrustPublisher(ctx, application.TrustSkillPublisherRequest{
			Name: flags.Arg(0), PublicKey: flags.Arg(1), Team: *team, Actor: *operator,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "trusted_publisher: %s\nfingerprint: %s\nteam: %s\n", publisher.Name, publisher.Fingerprint, publisher.Team)
		return nil
	case "revoke":
		flags := newFlagSet("skill catalog revoke", a.errOut)
		operator := flags.String("operator", "cli_operator", "operator identity")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{"operator": true})); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: cyberagent skill catalog revoke <fingerprint> [--operator cli_operator]")
		}
		if err := service.RevokePublisher(ctx, application.RevokeSkillPublisherRequest{
			Fingerprint: flags.Arg(0), Actor: *operator,
		}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "revoked: %s\n", flags.Arg(0))
		return nil
	case "pin":
		flags := newFlagSet("skill catalog pin", a.errOut)
		surfaceValue := flags.String("surface", "", "target catalog surface")
		operator := flags.String("operator", "cli_operator", "operator identity")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{"surface": true, "operator": true})); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: cyberagent skill catalog pin <name>@<version> --surface code|cyber [--operator cli_operator]")
		}
		name, version, err := skills.ParseInstalledPackageRef(flags.Arg(0))
		if err != nil {
			return err
		}
		surface, err := domain.ParseExecutionSurface(*surfaceValue)
		if err != nil {
			return err
		}
		if err := service.PinVersion(ctx, application.PinSkillVersionRequest{
			SkillName: name, Version: version, Surface: surface, Actor: *operator,
		}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "pinned: %s@%s\tsurface=%s\n", name, version, surface)
		return nil
	case "enable", "disable":
		flags := newFlagSet("skill catalog "+args[0], a.errOut)
		surfaceValue := flags.String("surface", "", "target catalog surface")
		operator := flags.String("operator", "cli_operator", "operator identity")
		if err := flags.Parse(reorderFlags(args[1:], map[string]bool{"surface": true, "operator": true})); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return fmt.Errorf("usage: cyberagent skill catalog %s <name> --surface code|cyber [--operator cli_operator]", args[0])
		}
		surface, err := domain.ParseExecutionSurface(*surfaceValue)
		if err != nil {
			return err
		}
		if err := service.SetEnabled(ctx, application.SetSkillEnabledRequest{
			SkillName: flags.Arg(0), Surface: surface, Enabled: args[0] == "enable", Actor: *operator,
		}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%sd: %s\tsurface=%s\n", args[0], flags.Arg(0), surface)
		return nil
	case "audit":
		if len(args) != 1 {
			return errors.New("usage: cyberagent skill catalog audit")
		}
		events, err := service.ListAudit(ctx, 100)
		if err != nil {
			return err
		}
		for _, event := range events {
			fmt.Fprintf(a.out, "%d\t%s\tsubject=%s\tactor=%s\t%s\n",
				event.Sequence, event.EventType, event.Subject, event.Actor, event.CreatedAt.Format(time.RFC3339Nano))
		}
		fmt.Fprintf(a.out, "audit_count: %d\n", len(events))
		return nil
	default:
		return fmt.Errorf("unknown skill catalog subcommand %q", args[0])
	}
}

func (a *App) skillImportSourceCommand(ctx context.Context, args []string, kind string, registry *skills.Registry) error {
	service, _, err := a.skillCatalogService(registry)
	if err != nil {
		return err
	}
	flags := newFlagSet("skill import-"+kind, a.errOut)
	surfaceValue := flags.String("surface", "", "target catalog surface")
	operationKey := flags.String("operation-key", "", "stable idempotency key")
	operator := flags.String("operator", "cli_operator", "operator identity")
	confirmed := flags.Bool("confirm-untrusted-skill", false,
		"explicitly confirm installation of untrusted instructions")
	var sha256Value, commitValue *string
	if kind == "url" {
		sha256Value = flags.String("sha256", "", "required expected SHA-256 of the pinned URL bytes")
	} else if kind == "git" {
		commitValue = flags.String("commit", "", "required pinned commit SHA")
	}
	if err := flags.Parse(reorderFlags(args, map[string]bool{
		"surface": true, "operation-key": true, "operator": true,
		"confirm-untrusted-skill": false, "sha256": true, "commit": true,
	})); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("usage: cyberagent skill import-%s <source> --surface code|cyber --operation-key <stable-key> --confirm-untrusted-skill %s",
			kind, map[string]string{"url": "--sha256 <hex>", "git": "--commit <sha>", "dir": ""}[kind])
	}
	surface, err := domain.ParseExecutionSurface(*surfaceValue)
	if err != nil {
		return err
	}
	var result application.ImportSkillFromSourceResult
	switch kind {
	case "url":
		result, err = service.ImportFromURL(ctx, application.ImportSkillFromURLRequest{
			URL: flags.Arg(0), SHA256: *sha256Value, Surface: surface,
			OperationKey: *operationKey, InstalledBy: *operator, ConfirmUntrusted: *confirmed,
		})
	case "git":
		result, err = service.ImportFromGit(ctx, application.ImportSkillFromGitRequest{
			RepoURL: flags.Arg(0), CommitSHA: *commitValue, Surface: surface,
			OperationKey: *operationKey, InstalledBy: *operator, ConfirmUntrusted: *confirmed,
			StagingRoot: filepath.Join(os.TempDir(), "cyberagent-skill-staging"),
		})
	case "dir":
		result, err = service.ImportFromDirectory(ctx, application.ImportSkillFromDirectoryRequest{
			Directory: flags.Arg(0), Surface: surface,
			OperationKey: *operationKey, InstalledBy: *operator, ConfirmUntrusted: *confirmed,
		})
	}
	if err != nil {
		return err
	}
	if result.Portable != nil {
		value := result.Portable
		fmt.Fprintf(a.out, "installation_id: %s\nformat: %s\npackage: %s\nrevision: %s\nstate: %s\nskills: %d\n", value.ID, value.Snapshot.Format, value.DisplayName(), value.Revision(), value.State, len(value.Snapshot.Skills))
		return nil
	}
	printInstalledSkillPackage(a, result.Installed)
	fmt.Fprintf(a.out, "signed: %t\nimport_id: %s\nsource_kind: %s\nsource: %s\npin: %s\npublisher_fingerprint: %s\n",
		result.Signed, result.Import.ID, result.Import.SourceKind, result.Import.Source,
		result.Import.Pin, result.Import.PublisherFingerprint)
	return nil
}

func printSkillPackagePreview(a *App, preview skills.PackagePreview) {
	userInvocable, modelInvocable, explicitOnly := preview.Manifest.InvocationPolicy()
	fmt.Fprintf(a.out, "package_protocol: %s\nskill_protocol: %s\nskill: %s@%s\nprofiles: %s\nsurfaces: %s\nphases: %s\nroles: %s\nuser_invocable: %t\nmodel_invocable: %t\nexplicit_only: %t\ntool_dependencies: %s\ncontent_sha256: %s\ncontent_bytes: %d\ncontent_token_upper_bound: %d\narchive_sha256: %s\npackage_fingerprint: %s\narchive_bytes: %d\nuncompressed_bytes: %d\nentry_count: %d\ntrust_class: %s\nrisk_codes: %s\nexecutable_assets: %d\ninstall_hooks: %d\nimport_command_execution: %t\nimport_network_access: %t\nimport_provider_calls: %t\ntool_capability_grant: %t\ninstallation_authorized: %t\nvalidated: true\n",
		preview.ProtocolVersion, preview.Manifest.Protocol, preview.Manifest.Name,
		preview.Manifest.Version, joinProfiles(preview.Manifest.Profiles),
		joinSurfaces(preview.Manifest.Surfaces), joinPhases(preview.Manifest.Phases),
		joinRoles(preview.Manifest.Roles), userInvocable, modelInvocable, explicitOnly,
		joinToolDependencies(preview.Manifest.ToolDependencies), preview.Manifest.ContentSHA256,
		preview.Manifest.ContentBytes, preview.Manifest.ContentTokenUpperBound,
		preview.ArchiveSHA256, preview.PackageFingerprint, preview.ArchiveBytes,
		preview.UncompressedBytes, preview.EntryCount, preview.TrustClass,
		joinPackageRiskCodes(preview.RiskCodes), preview.ExecutableAssetCount,
		preview.InstallHookCount, preview.ImportCommandExecution, preview.ImportNetworkAccess,
		preview.ImportProviderCalls, preview.ToolCapabilityGrant, preview.InstallationAuthorized)
}

func joinPackageRiskCodes(codes []skills.PackageRiskCode) string {
	values := make([]string, len(codes))
	for index, code := range codes {
		values[index] = string(code)
	}
	return strings.Join(values, ",")
}

func printInstalledSkillPackage(a *App, value skills.InstalledPackage) {
	status := "installed"
	if value.Removal != nil {
		status = "removed"
	}
	installation := value.Installation
	userInvocable, modelInvocable, explicitOnly := installation.Manifest.InvocationPolicy()
	fmt.Fprintf(a.out, "installation_id: %s\nprotocol: %s\nskill: %s\nsurface: %s\nprofiles: %s\nsurfaces: %s\nphases: %s\nroles: %s\nuser_invocable: %t\nmodel_invocable: %t\nexplicit_only: %t\ntool_dependencies: %s\ncontent_sha256: %s\ncontent_bytes: %d\ncontent_token_upper_bound: %d\narchive_sha256: %s\npackage_fingerprint: %s\narchive_bytes: %d\nuncompressed_bytes: %d\nentry_count: %d\ntrust_class: %s\nrisk_codes: %s\nobject_key: %s\nobject_verified: %t\nstatus: %s\noperator_confirmed: %t\nimport_command_execution: %t\nimport_network_access: %t\nimport_provider_calls: %t\nrun_selection_authorized: %t\ncontext_injection_authorized: %t\ntool_capability_grant: %t\ncontent_body_exposed: false\ninstalled_by: %s\ncreated_at: %s\ncompleted_at: %s\n",
		installation.ID, installation.ProtocolVersion,
		skills.FormatInstalledPackageRef(installation.Name, installation.Version),
		installation.Surface, joinProfiles(installation.Manifest.Profiles),
		joinSurfaces(installation.Manifest.Surfaces), joinPhases(installation.Manifest.Phases),
		joinRoles(installation.Manifest.Roles), userInvocable, modelInvocable, explicitOnly,
		joinToolDependencies(installation.Manifest.ToolDependencies),
		installation.Manifest.ContentSHA256, installation.Manifest.ContentBytes,
		installation.Manifest.ContentTokenUpperBound, installation.ArchiveSHA256,
		installation.PackageFingerprint, installation.ArchiveBytes,
		installation.UncompressedBytes, installation.EntryCount, installation.TrustClass,
		joinPackageRiskCodes(installation.RiskCodes), value.Result.ObjectKey,
		value.Result.ObjectVerified, status, installation.OperatorConfirmed,
		installation.ImportCommandExecution, installation.ImportNetworkAccess,
		installation.ImportProviderCalls, installation.RunSelectionAuthorized,
		installation.ContextInjectionAuthorized, installation.ToolCapabilityGrant,
		installation.InstalledBy, installation.CreatedAt.Format(time.RFC3339Nano),
		value.Result.CompletedAt.Format(time.RFC3339Nano))
	if value.Removal != nil {
		fmt.Fprintf(a.out, "removal_id: %s\npackage_object_retained: %t\nhistorical_recovery_preserved: %t\nfuture_selection_enabled: %t\nremoved_by: %s\nremoved_at: %s\n",
			value.Removal.ID, value.Removal.PackageObjectRetained,
			value.Removal.HistoricalRecoveryPreserved,
			value.Removal.FutureSelectionEnabled, value.Removal.RemovedBy,
			value.Removal.CreatedAt.Format(time.RFC3339Nano))
	}
}

func printExternalSkillBoundary(a *App) {
	fmt.Fprintln(a.out, "external_run_selection: explicit_run_confirmation_required")
	fmt.Fprintln(a.out, "context_injection_authorized: false")
	fmt.Fprintln(a.out, "tool_capability_grant: false")
	fmt.Fprintln(a.out, "import_command_execution: false")
	fmt.Fprintln(a.out, "import_network_access: false")
	fmt.Fprintln(a.out, "import_provider_calls: false")
}

func printExternalSkillSelection(a *App, selection skills.ExternalSelection) {
	fmt.Fprintf(a.out, "selection_id: %s\nrun_id: %s\nmission_id: %s\nmode_snapshot_id: %s\nmode_revision: %d\nprotocol: %s\nsurface: %s\nprofile: %s\ntoken_budget: %d\ntoken_upper_bound: %d\nitem_count: %d\nselection_fingerprint: %s\ntrust_class: %s\noperator_confirmed: %t\ncontext_delivery_authorized: %t\ntool_capability_grant: %t\nrequested_by: %s\ncreated_at: %s\n",
		selection.ID, selection.RunID, selection.MissionID, selection.ModeSnapshotID,
		selection.ModeRevision, selection.ProtocolVersion, selection.Surface,
		selection.Profile, selection.TokenBudget, selection.TokenUpperBound,
		selection.ItemCount, selection.Fingerprint,
		skills.PackageTrustOperatorInstalledUntrusted, selection.OperatorConfirmed,
		selection.ContextDeliveryAuthorized, selection.ToolCapabilityGrant,
		selection.RequestedBy, selection.CreatedAt.Format(time.RFC3339Nano))
	for _, item := range selection.Items {
		fmt.Fprintf(a.out, "skill[%d]: %s installation_id=%s content_sha256=%s archive_sha256=%s object_key=%s bytes=%d token_upper_bound=%d declared_tools=%d specialist_eligible=%t\n",
			item.Ordinal, skills.FormatInstalledPackageRef(item.Name, item.Version),
			item.InstallationID, item.ContentSHA256, item.ArchiveSHA256, item.ObjectKey,
			item.ContentBytes, item.TokenUpperBound, item.ToolDependencyCount,
			item.SpecialistEligible)
	}
	fmt.Fprintln(a.out, "content_role: untrusted_workflow_guidance")
	fmt.Fprintln(a.out, "shell_network_file_or_tool_grant: false")
}

func printSkillSelection(a *App, selection skills.Selection) {
	fmt.Fprintf(a.out, "selection_id: %s\nrun_id: %s\nmission_id: %s\nprotocol: %s\nprofile: %s\ntoken_budget: %d\ntoken_upper_bound: %d\nitem_count: %d\nselection_fingerprint: %s\nrequested_by: %s\ncreated_at: %s\n",
		selection.ID, selection.RunID, selection.MissionID, selection.ProtocolVersion,
		selection.Profile, selection.TokenBudget, selection.TokenUpperBound,
		selection.ItemCount, selection.Fingerprint, selection.RequestedBy,
		selection.CreatedAt.Format("2006-01-02T15:04:05.000000000Z"))
	for _, item := range selection.Items {
		fmt.Fprintf(a.out, "skill[%d]: %s@%s sha256=%s bytes=%d token_upper_bound=%d\n",
			item.Ordinal, item.Name, item.Version, item.ContentSHA256,
			item.ContentBytes, item.TokenUpperBound)
	}
}

func joinProfiles(profiles []domain.Profile) string {
	values := make([]string, len(profiles))
	for index, profile := range profiles {
		values[index] = string(profile)
	}
	return strings.Join(values, ",")
}

func joinSurfaces(surfaces []domain.ExecutionSurface) string {
	if len(surfaces) == 0 {
		return "legacy"
	}
	values := make([]string, len(surfaces))
	for index, surface := range surfaces {
		values[index] = string(surface)
	}
	return strings.Join(values, ",")
}

func joinPhases(phases []domain.ExecutionPhase) string {
	if len(phases) == 0 {
		return "legacy"
	}
	values := make([]string, len(phases))
	for index, phase := range phases {
		values[index] = string(phase)
	}
	return strings.Join(values, ",")
}

func joinRoles(roles []domain.AgentRole) string {
	if len(roles) == 0 {
		return "legacy"
	}
	values := make([]string, len(roles))
	for index, role := range roles {
		values[index] = string(role)
	}
	return strings.Join(values, ",")
}

func joinToolDependencies(dependencies []toolgateway.ToolName) string {
	values := make([]string, len(dependencies))
	for index, dependency := range dependencies {
		values[index] = string(dependency)
	}
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}

func printSkillBoundary(a *App) {
	fmt.Fprintln(a.out, "context_injection: root_selected_and_specialist_minimized")
	fmt.Fprintln(a.out, "tool_capability_grant: disabled")
}
