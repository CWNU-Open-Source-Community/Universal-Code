package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const maxDockerObservationResponseBytes = 2 * 1024 * 1024

type dockerObservationHTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type dockerEngineReadOnlyTransport struct {
	doer     dockerObservationHTTPDoer
	endpoint DockerObservationEndpoint
}

func newDockerEngineReadOnlyTransport(doer dockerObservationHTTPDoer,
	endpoint DockerObservationEndpoint,
) (dockerEngineReadOnlyTransport, error) {
	if doer == nil {
		return dockerEngineReadOnlyTransport{}, errors.New("docker observation HTTP client is required")
	}
	if err := endpoint.Validate(); err != nil {
		return dockerEngineReadOnlyTransport{}, err
	}
	return dockerEngineReadOnlyTransport{doer: doer, endpoint: endpoint}, nil
}

func (transport dockerEngineReadOnlyTransport) Endpoint() DockerObservationEndpoint {
	return transport.endpoint
}

func (transport dockerEngineReadOnlyTransport) Ping(ctx context.Context) error {
	body, err := transport.get(ctx, "/_ping", false)
	if err != nil {
		return err
	}
	if string(body) != "OK" {
		return newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	return nil
}

func (transport dockerEngineReadOnlyTransport) Version(ctx context.Context) (DockerDaemonVersion, error) {
	body, err := transport.get(ctx, "/version", true)
	if err != nil {
		return DockerDaemonVersion{}, err
	}
	var payload struct {
		APIVersion    string `json:"ApiVersion"`
		MinAPIVersion string `json:"MinAPIVersion"`
		Version       string `json:"Version"`
		GitCommit     string `json:"GitCommit"`
		OS            string `json:"Os"`
		Arch          string `json:"Arch"`
	}
	if err := decodeDockerObservationJSON(body, &payload); err != nil {
		return DockerDaemonVersion{}, err
	}
	return DockerDaemonVersion{APIVersion: payload.APIVersion,
		MinAPIVersion: payload.MinAPIVersion, EngineVersion: payload.Version,
		GitCommit: payload.GitCommit, OSType: payload.OS,
		Architecture: canonicalDockerArchitecture(payload.Arch)}, nil
}

// canonicalDockerArchitecture folds the GOARCH spellings that /info may emit
// (x86_64, aarch64) into the OCI names used by /version and image configs
// (amd64, arm64). Different endpoints of the same daemon must never look like
// two different platforms.
func canonicalDockerArchitecture(architecture string) string {
	switch architecture {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return architecture
	}
}

func (transport dockerEngineReadOnlyTransport) Info(ctx context.Context) (DockerDaemonInfo, error) {
	body, err := transport.get(ctx, "/info", true)
	if err != nil {
		return DockerDaemonInfo{}, err
	}
	var payload struct {
		ID              string   `json:"ID"`
		Name            string   `json:"Name"`
		DockerRootDir   string   `json:"DockerRootDir"`
		ServerVersion   string   `json:"ServerVersion"`
		OperatingSystem string   `json:"OperatingSystem"`
		OSType          string   `json:"OSType"`
		Architecture    string   `json:"Architecture"`
		Driver          string   `json:"Driver"`
		CgroupDriver    string   `json:"CgroupDriver"`
		CgroupVersion   string   `json:"CgroupVersion"`
		DefaultRuntime  string   `json:"DefaultRuntime"`
		NCPU            int      `json:"NCPU"`
		MemTotal        int64    `json:"MemTotal"`
		PidsLimit       bool     `json:"PidsLimit"`
		SecurityOptions []string `json:"SecurityOptions"`
	}
	if err := decodeDockerObservationJSON(body, &payload); err != nil {
		return DockerDaemonInfo{}, err
	}
	return DockerDaemonInfo{
		ID: payload.ID, Name: payload.Name, DockerRootDir: payload.DockerRootDir,
		ServerVersion: payload.ServerVersion, OperatingSystem: payload.OperatingSystem,
		OSType:       payload.OSType,
		Architecture: canonicalDockerArchitecture(payload.Architecture), Driver: payload.Driver,
		CgroupDriver: payload.CgroupDriver, CgroupVersion: payload.CgroupVersion,
		DefaultRuntime: payload.DefaultRuntime, NCPU: payload.NCPU,
		MemoryBytes: payload.MemTotal, PidsLimit: payload.PidsLimit,
		SecurityOptions: payload.SecurityOptions,
	}, nil
}

func (transport dockerEngineReadOnlyTransport) InspectImage(ctx context.Context,
	imageDigest string,
) (DockerImageInspection, error) {
	if !ValidOCIImageDigest(imageDigest) {
		return DockerImageInspection{}, errors.New("docker image digest is invalid")
	}
	body, err := transport.get(ctx, "/images/"+url.PathEscape(imageDigest)+"/json", true)
	if err != nil {
		return DockerImageInspection{}, err
	}
	var payload struct {
		ID           string   `json:"Id"`
		RepoDigests  []string `json:"RepoDigests"`
		OS           string   `json:"Os"`
		Architecture string   `json:"Architecture"`
		Size         int64    `json:"Size"`
		Config       struct {
			User    string                     `json:"User"`
			Env     []string                   `json:"Env"`
			Volumes map[string]json.RawMessage `json:"Volumes"`
			Labels  map[string]string          `json:"Labels"`
		} `json:"Config"`
		RootFS struct {
			Type string `json:"Type"`
		} `json:"RootFS"`
		GraphDriver struct {
			Name string `json:"Name"`
		} `json:"GraphDriver"`
	}
	if err := decodeDockerObservationJSON(body, &payload); err != nil {
		return DockerImageInspection{}, err
	}
	return DockerImageInspection{
		ID: payload.ID, RepoDigests: payload.RepoDigests, OSType: payload.OS,
		Architecture: canonicalDockerArchitecture(payload.Architecture), SizeBytes: payload.Size,
		User: payload.Config.User, RootFSType: payload.RootFS.Type,
		GraphDriver:      payload.GraphDriver.Name,
		EnvironmentCount: len(payload.Config.Env), VolumeCount: len(payload.Config.Volumes),
		ProjectActionsVersion: payload.Config.Labels[DockerStandardCodeProjectActionsLabel],
		PreparationVersion:    payload.Config.Labels[DockerStandardCodePreparationLabel],
	}, nil
}

func (transport dockerEngineReadOnlyTransport) ListProductionEvidenceResources(
	ctx context.Context, attemptID string,
) (DockerProductionEvidenceHarnessInventory, error) {
	if validateStoredIdentity("Docker production evidence harness attempt", attemptID) != nil ||
		transport.endpoint.Class != DockerObservationEndpointLocalUnix {
		return DockerProductionEvidenceHarnessInventory{}, errors.New(
			"docker production evidence harness resource query is invalid")
	}
	filterJSON, err := json.Marshal(map[string][]string{
		"label": {DockerProductionEvidenceHarnessLabelKey + "=" + attemptID},
	})
	if err != nil {
		return DockerProductionEvidenceHarnessInventory{}, newDockerObservationError(
			DockerObservationFailureInvalidResponse)
	}
	query := url.Values{"all": {"1"}, "filters": {string(filterJSON)}}.Encode()
	body, err := transport.getExact(ctx, "/containers/json", query, true)
	if err != nil {
		return DockerProductionEvidenceHarnessInventory{}, err
	}
	var payload []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if err := decodeDockerObservationJSON(body, &payload); err != nil ||
		len(payload) > MaxDockerProductionEvidenceHarnessResources {
		return DockerProductionEvidenceHarnessInventory{}, newDockerObservationError(
			DockerObservationFailureInvalidResponse)
	}
	fingerprints := make([]string, 0, len(payload))
	seen := make(map[string]struct{}, len(payload))
	for _, resource := range payload {
		if !validObservationText(resource.ID, 256, false) ||
			resource.Labels[DockerProductionEvidenceHarnessLabelKey] != attemptID {
			return DockerProductionEvidenceHarnessInventory{}, newDockerObservationError(
				DockerObservationFailureInvalidResponse)
		}
		if _, exists := seen[resource.ID]; exists {
			return DockerProductionEvidenceHarnessInventory{}, newDockerObservationError(
				DockerObservationFailureInvalidResponse)
		}
		seen[resource.ID] = struct{}{}
		fingerprints = append(fingerprints, fingerprint(
			"sandbox_docker_production_evidence_harness_resource.v1", resource.ID))
	}
	sort.Strings(fingerprints)
	return NewDockerProductionEvidenceHarnessInventory(transport.endpoint, fingerprints)
}

func (transport dockerEngineReadOnlyTransport) get(ctx context.Context, path string,
	wantJSON bool,
) ([]byte, error) {
	return transport.getExact(ctx, path, "", wantJSON)
}

func (transport dockerEngineReadOnlyTransport) getExact(ctx context.Context, path,
	rawQuery string, wantJSON bool,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validDockerObservationGET(path, rawQuery, wantJSON) {
		return nil, newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	requestURL := "http://docker" + path
	if rawQuery != "" {
		requestURL += "?" + rawQuery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "cyberagent-workbench/docker-observer-v1")
	response, err := transport.doer.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newDockerObservationError(DockerObservationFailureConnection)
	}
	if response == nil || response.Body == nil {
		return nil, newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil ||
		response.Request.Method != http.MethodGet || response.Request.URL.Scheme != "http" ||
		response.Request.URL.Host != "docker" || response.Request.URL.Path != request.URL.Path ||
		response.Request.URL.RawQuery != rawQuery {
		return nil, newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	if response.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/images/") {
		return nil, newDockerObservationError(DockerObservationFailureImageNotFound)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	if wantJSON {
		contentType := response.Header.Get("Content-Type")
		if contentType != "" {
			mediaType, _, err := mime.ParseMediaType(contentType)
			if err != nil || !strings.EqualFold(mediaType, "application/json") {
				return nil, newDockerObservationError(DockerObservationFailureInvalidResponse)
			}
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDockerObservationResponseBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxDockerObservationResponseBytes {
		return nil, newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	return body, nil
}

func validDockerObservationGET(path, rawQuery string, wantJSON bool) bool {
	switch path {
	case "/_ping":
		return !wantJSON && rawQuery == ""
	case "/version", "/info":
		return wantJSON && rawQuery == ""
	case "/containers/json":
		return wantJSON && validDockerProductionEvidenceResourceQuery(rawQuery)
	default:
		if !wantJSON || rawQuery != "" || !strings.HasPrefix(path, "/images/") ||
			!strings.HasSuffix(path, "/json") {
			return false
		}
		digest := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		return ValidOCIImageDigest(digest) && path == "/images/"+url.PathEscape(digest)+"/json"
	}
}

func validDockerProductionEvidenceResourceQuery(rawQuery string) bool {
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) != 2 || len(values["all"]) != 1 ||
		values["all"][0] != "1" || len(values["filters"]) != 1 {
		return false
	}
	data := []byte(values["filters"][0])
	if !json.Valid(data) || rejectDuplicateDockerObservationJSON(data) != nil {
		return false
	}
	var filters map[string][]string
	if err := json.Unmarshal(data, &filters); err != nil || len(filters) != 1 ||
		len(filters["label"]) != 1 {
		return false
	}
	label := filters["label"][0]
	prefix := DockerProductionEvidenceHarnessLabelKey + "="
	return strings.HasPrefix(label, prefix) &&
		validateStoredIdentity("Docker production evidence harness attempt",
			strings.TrimPrefix(label, prefix)) == nil
}

func decodeDockerObservationJSON(data []byte, target any) error {
	if !json.Valid(data) || rejectDuplicateDockerObservationJSON(data) != nil {
		return newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newDockerObservationError(DockerObservationFailureInvalidResponse)
	}
	return nil
}

func rejectDuplicateDockerObservationJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("docker observation JSON is too deep")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				fieldToken, err := decoder.Token()
				if err != nil {
					return err
				}
				field, ok := fieldToken.(string)
				if !ok {
					return errors.New("docker observation JSON field is invalid")
				}
				if _, exists := seen[field]; exists {
					return errors.New("docker observation JSON contains duplicate fields")
				}
				seen[field] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("docker observation JSON delimiter is invalid")
		}
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("docker observation JSON contains trailing data")
	}
	return nil
}
