package application

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceImageActivityProjectsReadPathWithoutPrivateImageMetadata(t *testing.T) {
	call := threadActivityFactsCall("workspace_view_image",
		[]byte(`{"version":"agent-code-tools.v1","path":"output/preview.png"}`),
		map[string]string{"artifact_sha256": strings.Repeat("a", 64)},
		`{"version":"workspace_image_result.v1","image":{"private":"image-metadata-canary"}}`)
	detail, found, err := ProjectThreadActivityTypedDetail(call)
	if err != nil || !found || detail.Kind != "file_read" || detail.FileRead == nil {
		t.Fatalf("image detail=%+v found=%t err=%v", detail, found, err)
	}
	read := detail.FileRead
	if read.Operation != "查看图片" || read.Path != "output/preview.png" || read.StartLine != 0 ||
		read.EndLine != 0 || read.Summary != "图片查看已完成" {
		t.Fatalf("image read projection=%+v", read)
	}
	encoded, _ := json.Marshal(detail)
	for _, private := range []string{"image-metadata-canary", strings.Repeat("a", 64)} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("image activity exposed private metadata: %s", encoded)
		}
	}
}
