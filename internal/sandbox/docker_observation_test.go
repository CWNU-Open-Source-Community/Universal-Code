package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

type dockerObservationDoer struct {
	t             *testing.T
	imageDigest   string
	duplicateInfo bool
	imageMissing  bool
	requests      []*http.Request
}

type dockerObservationDoerFunc func(*http.Request) (*http.Response, error)

func (doer dockerObservationDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return doer(request)
}

func (doer *dockerObservationDoer) Do(request *http.Request) (*http.Response, error) {
	doer.t.Helper()
	doer.requests = append(doer.requests, request)
	status := http.StatusOK
	contentType := "application/json"
	body := ""
	switch request.URL.Path {
	case "/_ping":
		contentType = "text/plain"
		body = "OK"
	case "/version":
		body = `{"ApiVersion":"1.47","MinAPIVersion":"1.24","Version":"27.5.1",` +
			`"GitCommit":"abc123","Os":"linux","Arch":"amd64"}`
	case "/info":
		body = `{"ID":"daemon-id","Name":"build-host","DockerRootDir":"/var/lib/docker",` +
			`"ServerVersion":"27.5.1","OperatingSystem":"Test Linux","OSType":"linux",` +
			`"Architecture":"amd64","Driver":"overlay2","CgroupDriver":"systemd",` +
			`"CgroupVersion":"2","DefaultRuntime":"runc","NCPU":8,"MemTotal":17179869184,` +
			`"PidsLimit":true,"SecurityOptions":["name=seccomp,profile=builtin","name=rootless"]}`
		if doer.duplicateInfo {
			body = `{"ID":"daemon-id","ID":"other"}`
		}
	case "/images/" + doer.imageDigest + "/json":
		if doer.imageMissing {
			status = http.StatusNotFound
			body = `{"message":"No such image"}`
		} else {
			body = `{"Id":"sha256:` + strings.Repeat("a", 64) + `",` +
				`"RepoDigests":["example.invalid/workbench@` + doer.imageDigest + `"],` +
				`"Os":"linux","Architecture":"amd64","Size":1048576,` +
				`"Config":{"User":"65532:65532"},"RootFS":{"Type":"layers"},` +
				`"GraphDriver":{"Name":"overlay2"}}`
		}
	default:
		doer.t.Fatalf("unexpected Docker observation path %q", request.URL.Path)
	}
	response := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
	response.Header.Set("Content-Type", contentType)
	return response, nil
}

func TestReadOnlyDockerProductionObserverCollectsBoundMetadataWithoutAuthority(t *testing.T) {
	imageDigest := "sha256:" + strings.Repeat("e", 64)
	doer := &dockerObservationDoer{t: t, imageDigest: imageDigest}
	endpoint, err := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newDockerEngineReadOnlyTransport(doer, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	report, err := NewReadOnlyDockerProductionObserver(transport).Observe(context.Background(),
		DockerObservationProbeRequest{
			BindingFingerprint: strings.Repeat("b", 64), ImageDigest: imageDigest,
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if report.Status != DockerObservationStatusComplete || !report.DaemonReachable ||
		!report.ImageInspected || !report.ProductionObserved || report.ProductionVerified ||
		report.BackendAvailable || report.BackendEnabled || report.ExecutionAuthorized ||
		report.ArtifactCommitAuthorized || !report.Rootless ||
		report.PrivateMountState != DockerPrivateMountNotObservable ||
		report.ImageUserState != DockerImageUserExplicitNonRoot ||
		countObservedDockerItems(report.Items) != 5 {
		t.Fatalf("unexpected Docker observation report: %#v", report)
	}
	if len(doer.requests) != 4 {
		t.Fatalf("request count=%d, want 4", len(doer.requests))
	}
	for _, request := range doer.requests {
		if request.Method != http.MethodGet || request.URL.Scheme != "http" ||
			request.URL.Host != "docker" || request.URL.RawQuery != "" || request.Body != nil {
			t.Fatalf("Docker observer issued a non-read request: %#v", request)
		}
	}

	transportType := reflect.TypeOf((*DockerReadOnlyTransport)(nil)).Elem()
	methods := make([]string, 0, transportType.NumMethod())
	for index := 0; index < transportType.NumMethod(); index++ {
		methods = append(methods, transportType.Method(index).Name)
	}
	for _, forbidden := range []string{"Create", "Start", "Run", "Exec", "Pull", "Remove"} {
		for _, method := range methods {
			if strings.Contains(method, forbidden) {
				t.Fatalf("read-only Docker interface exposes %q: %v", method, methods)
			}
		}
	}
}

func TestReadOnlyDockerProductionObserverPersistsBoundedUnavailableStates(t *testing.T) {
	imageDigest := "sha256:" + strings.Repeat("d", 64)
	request := DockerObservationProbeRequest{
		BindingFingerprint: strings.Repeat("c", 64), ImageDigest: imageDigest,
	}
	unavailable := NewUnavailableDockerReadOnlyTransport(DockerObservationEndpointLocalNPipe,
		DockerObservationFailureTransportUnsupported)
	report, err := NewReadOnlyDockerProductionObserver(unavailable).Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != DockerObservationStatusDaemonUnavailable || report.DaemonReachable ||
		report.FailureCode != DockerObservationFailureTransportUnsupported ||
		countObservedDockerItems(report.Items) != 0 || report.ProductionObserved {
		t.Fatalf("unexpected unavailable report: %#v", report)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}

	doer := &dockerObservationDoer{t: t, imageDigest: imageDigest, imageMissing: true}
	endpoint, _ := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	transport, err := newDockerEngineReadOnlyTransport(doer, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	report, err = NewReadOnlyDockerProductionObserver(transport).Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != DockerObservationStatusImageUnavailable || !report.DaemonReachable ||
		report.ImageInspected || report.FailureCode != DockerObservationFailureImageNotFound ||
		countObservedDockerItems(report.Items) != 4 || report.ProductionObserved {
		t.Fatalf("unexpected image-unavailable report: %#v", report)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDockerObservationRejectsAmbiguousResponsesAndHonorsCancellation(t *testing.T) {
	imageDigest := "sha256:" + strings.Repeat("f", 64)
	doer := &dockerObservationDoer{t: t, imageDigest: imageDigest, duplicateInfo: true}
	endpoint, _ := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	transport, err := newDockerEngineReadOnlyTransport(doer, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewReadOnlyDockerProductionObserver(transport).Observe(context.Background(),
		DockerObservationProbeRequest{
			BindingFingerprint: strings.Repeat("a", 64), ImageDigest: imageDigest,
		})
	if DockerObservationErrorCode(err) != DockerObservationFailureInvalidResponse {
		t.Fatalf("duplicate response error=%v code=%q", err, DockerObservationErrorCode(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewReadOnlyDockerProductionObserver(
		NewUnavailableDockerReadOnlyTransport(DockerObservationEndpointLocalNPipe,
			DockerObservationFailureTransportUnsupported),
	).Observe(ctx, DockerObservationProbeRequest{
		BindingFingerprint: strings.Repeat("a", 64), ImageDigest: imageDigest,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled observation error=%v", err)
	}
}

func TestDockerObservationTransportRejectsUntrustedHTTPResponses(t *testing.T) {
	endpoint, _ := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	tests := []struct {
		name string
		doer dockerObservationDoerFunc
		call func(dockerEngineReadOnlyTransport) error
	}{
		{
			name: "changed final host",
			doer: func(request *http.Request) (*http.Response, error) {
				changed := request.Clone(request.Context())
				changed.URL.Host = "elsewhere"
				return dockerObservationHTTPResponse(changed, http.StatusOK, "text/plain", "OK"), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				return transport.Ping(context.Background())
			},
		},
		{
			name: "missing final request",
			doer: func(*http.Request) (*http.Response, error) {
				return dockerObservationHTTPResponse(nil, http.StatusOK, "text/plain", "OK"), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				return transport.Ping(context.Background())
			},
		},
		{
			name: "changed final method",
			doer: func(request *http.Request) (*http.Response, error) {
				changed := request.Clone(request.Context())
				changed.Method = http.MethodPost
				return dockerObservationHTTPResponse(changed, http.StatusOK, "text/plain", "OK"), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				return transport.Ping(context.Background())
			},
		},
		{
			name: "changed final path",
			doer: func(request *http.Request) (*http.Response, error) {
				changed := request.Clone(request.Context())
				changed.URL.Path = "/containers/json"
				return dockerObservationHTTPResponse(changed, http.StatusOK, "text/plain", "OK"), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				return transport.Ping(context.Background())
			},
		},
		{
			name: "changed final query",
			doer: func(request *http.Request) (*http.Response, error) {
				changed := request.Clone(request.Context())
				changed.URL.RawQuery = "all=1"
				return dockerObservationHTTPResponse(changed, http.StatusOK, "text/plain", "OK"), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				return transport.Ping(context.Background())
			},
		},
		{
			name: "wrong JSON content type",
			doer: func(request *http.Request) (*http.Response, error) {
				return dockerObservationHTTPResponse(request, http.StatusOK, "text/html", `{}`), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				_, err := transport.Version(context.Background())
				return err
			},
		},
		{
			name: "JSON content type prefix confusion",
			doer: func(request *http.Request) (*http.Response, error) {
				return dockerObservationHTTPResponse(request, http.StatusOK,
					"application/json-patch+json", `{}`), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				_, err := transport.Version(context.Background())
				return err
			},
		},
		{
			name: "oversized response",
			doer: func(request *http.Request) (*http.Response, error) {
				return dockerObservationHTTPResponse(request, http.StatusOK, "text/plain",
					strings.Repeat("x", maxDockerObservationResponseBytes+1)), nil
			},
			call: func(transport dockerEngineReadOnlyTransport) error {
				return transport.Ping(context.Background())
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport, err := newDockerEngineReadOnlyTransport(test.doer, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.call(transport); DockerObservationErrorCode(err) != DockerObservationFailureInvalidResponse {
				t.Fatalf("untrusted response error=%v code=%q", err,
					DockerObservationErrorCode(err))
			}
		})
	}

	calls := 0
	transport, err := newDockerEngineReadOnlyTransport(dockerObservationDoerFunc(
		func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("unexpected request")
		}), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.get(context.Background(), "/containers/json", true); DockerObservationErrorCode(err) != DockerObservationFailureInvalidResponse || calls != 0 {
		t.Fatalf("non-allowlisted path reached HTTP doer: calls=%d err=%v", calls, err)
	}
}

func TestDockerObservationCanonicalizesDaemonArchitectureNames(t *testing.T) {
	endpoint, _ := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	transport, err := newDockerEngineReadOnlyTransport(dockerObservationDoerFunc(
		func(request *http.Request) (*http.Response, error) {
			var payload map[string]any
			switch request.URL.Path {
			case "/version":
				payload = map[string]any{"ApiVersion": "1.44",
					"MinAPIVersion": "1.24", "Version": "25.0.0",
					"Os": "linux", "Arch": "amd64"}
			case "/info":
				payload = map[string]any{"ID": "info-1", "Name": "docker-desktop",
					"ServerVersion": "25.0.0", "OSType": "linux",
					"Architecture": "x86_64", "NCPU": 8, "MemTotal": int64(8 << 30),
					"PidsLimit": true}
			case "/images/" + strings.Repeat("f", 64) + "/json":
				t.Fatalf("canonicalization probe must not inspect an image")
			default:
				t.Fatalf("unexpected read-only path %q", request.URL.Path)
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			return dockerObservationHTTPResponse(request, http.StatusOK,
				"application/json", string(body)), nil
		}), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	version, err := transport.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	info, err := transport.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version.Architecture != "amd64" || info.Architecture != "amd64" {
		t.Fatalf("daemon architecture was not canonicalized: version=%q info=%q",
			version.Architecture, info.Architecture)
	}
}


func TestDockerObservationTransportUsesExactEvidenceResourceFilter(t *testing.T) {
	endpoint, _ := NewDockerObservationEndpoint(DockerObservationEndpointLocalUnix)
	attemptID := "production-attempt"
	requests := 0
	transport, err := newDockerEngineReadOnlyTransport(dockerObservationDoerFunc(
		func(request *http.Request) (*http.Response, error) {
			requests++
			if request.Method != http.MethodGet || request.URL.Path != "/containers/json" ||
				request.Body != nil {
				t.Fatalf("unexpected resource reconciliation request: %#v", request)
			}
			query := request.URL.Query()
			if len(query) != 2 || query.Get("all") != "1" ||
				query.Get("filters") != `{"label":["`+
					DockerProductionEvidenceHarnessLabelKey+`=`+attemptID+`"]}` {
				t.Fatalf("resource reconciliation query escaped its fixed filter: %q",
					request.URL.RawQuery)
			}
			return dockerObservationHTTPResponse(request, http.StatusOK,
				"application/json", `[]`), nil
		}), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := transport.ListProductionEvidenceResources(context.Background(), attemptID)
	if err != nil || requests != 1 || inventory.OwnedResourceCount != 0 ||
		inventory.DaemonReadCount != 1 || !inventory.RealDaemonContacted {
		t.Fatalf("unexpected resource inventory: %#v requests=%d err=%v",
			inventory, requests, err)
	}

	transport, err = newDockerEngineReadOnlyTransport(dockerObservationDoerFunc(
		func(request *http.Request) (*http.Response, error) {
			return dockerObservationHTTPResponse(request, http.StatusOK, "application/json",
				`[{"Id":"container-id","Labels":{"`+
					DockerProductionEvidenceHarnessLabelKey+`":"other-attempt"}}]`), nil
		}), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.ListProductionEvidenceResources(context.Background(),
		attemptID); DockerObservationErrorCode(err) != DockerObservationFailureInvalidResponse {
		t.Fatalf("mismatched resource label was trusted: %v", err)
	}
}

func dockerObservationHTTPResponse(request *http.Request, status int, contentType, body string,
) *http.Response {
	response := &http.Response{StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request}
	response.Header.Set("Content-Type", contentType)
	return response
}

func TestLocalDockerReadOnlyObservationIntegration(t *testing.T) {
	if os.Getenv("CYBERAGENT_DOCKER_READONLY_INTEGRATION") != "1" {
		t.Skip("set CYBERAGENT_DOCKER_READONLY_INTEGRATION=1 for an opt-in local daemon probe")
	}
	imageDigest := strings.TrimSpace(os.Getenv("CYBERAGENT_DOCKER_READONLY_IMAGE_DIGEST"))
	if !ValidOCIImageDigest(imageDigest) {
		t.Fatal("CYBERAGENT_DOCKER_READONLY_IMAGE_DIGEST must be an already-present OCI sha256 digest")
	}
	report, err := NewReadOnlyDockerProductionObserver(
		NewLocalDockerReadOnlyTransport(),
	).Observe(context.Background(), DockerObservationProbeRequest{
		BindingFingerprint: strings.Repeat("a", 64), ImageDigest: imageDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != DockerObservationStatusComplete || !report.ProductionObserved ||
		report.ProductionVerified || report.BackendAvailable || report.BackendEnabled ||
		report.ExecutionAuthorized || report.ArtifactCommitAuthorized {
		t.Fatalf("opt-in read-only observation did not complete safely: %#v", report)
	}
}
