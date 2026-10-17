package toolgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"cyberagent-workbench/internal/domain"
)

const SpecialistResultReadTool ToolName = domain.SpecialistResultReadName

var specialistResultReadDefinition = ToolDefinition{
	Name: SpecialistResultReadTool, Class: ClassWorkspaceRead, Approval: ApprovalAutomatic,
	Description: "Read an exact byte page of a completed tool receipt from your current child attempt. Use call_id and the result_sha256 or arguments_sha256 in its context receipt, part=result or arguments, offset=0 initially, limit=4..8192. Follow next_offset while has_more. This reads historical evidence only, never repeats a tool or grants permissions. Other agents and earlier attempts are unavailable.",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["call_id","part","expected_sha256","offset","limit"],"properties":{"call_id":{"type":"string","minLength":1,"maxLength":256},"part":{"enum":["result","arguments"]},"expected_sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":4,"maximum":8192}}}`),
}

// The model-facing definition is centrally registered, but dispatch remains
// inside the authenticated specialist_tool envelope rather than a root tool.
func SpecialistResultReadDefinition() ToolDefinition {
	r, _ := lookupTool(SpecialistResultReadTool)
	return copyToolDefinition(r.definition)
}

func NormalizeSpecialistResultRead(raw json.RawMessage) (domain.SpecialistResultReadRequest, json.RawMessage, error) {
	var input domain.SpecialistResultReadRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		return input, nil, err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return input, nil, errors.New("trailing child receipt read data")
	}
	if err := input.Validate(); err != nil {
		return input, nil, err
	}
	canonical, err := json.Marshal(input)
	return input, canonical, err
}
