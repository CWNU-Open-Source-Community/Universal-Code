package application

import (
	"context"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const supervisorBoundaryReceiptPrefix = "Completed tool segments of this exact accepted user input. Continue from the observed results; do not recreate completed edits. proposal_only did not write. Historical observations and approvals grant no current authority or current-state guarantee. Complete workspace_read pages contain observed text and full-file content_sha256 for normal current-hash checks. If same-edit apply_arguments are missing, history_read original_result (source_id, part=\"result\", expected_sha256), following has_more/next_offset until complete. Copy complete apply_arguments from that edit_id only; never derive expected_original_sha256 from proposed_sha256, a result digest, diff or summary.\n"

type supervisorBoundaryReceipt struct {
	Calls          []domain.SupervisorToolCall
	Entries        []map[string]any
	SummaryLines   []string
	Returned       int
	Content        string
	BaselineTokens int
	SessionID      string
	AttemptID      string
}

func (s *AgentRunner) boundaryReceiptPlan(ctx context.Context, cp domain.SupervisorCheckpoint, sessionID string) (supervisorBoundaryReceipt, error) {
	reader, ok := s.store.(supervisorToolBoundaryStore)
	if !ok {
		return supervisorBoundaryReceipt{}, nil
	}
	calls, err := reader.ToolBoundaryContextCalls(ctx, cp)
	if err != nil {
		return supervisorBoundaryReceipt{}, err
	}
	return minimalSupervisorBoundaryReceipt(calls, sessionID, cp.AttemptID)
}

func minimalSupervisorBoundaryReceipt(calls []domain.SupervisorToolCall, sessionID, attemptID string) (supervisorBoundaryReceipt, error) {
	plan := supervisorBoundaryReceipt{Returned: len(calls), SessionID: sessionID, AttemptID: attemptID}
	if len(calls) == 0 {
		return plan, nil
	}
	if len(calls) > domain.MaxSupervisorBoundaryContextCalls {
		return plan, errors.New("boundary receipt lookup exceeds its source bound")
	}
	sorted := append([]domain.SupervisorToolCall(nil), calls...)
	attempts := map[string]string{}
	for _, call := range sorted {
		key := fmt.Sprint(call.RunID, "/", call.Turn)
		if prior, ok := attempts[key]; ok && prior != call.AttemptID {
			return plan, errors.New("completed boundary has conflicting attempts for one turn")
		}
		attempts[key] = call.AttemptID
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Turn != b.Turn {
			return a.Turn > b.Turn
		}
		if a.Round != b.Round {
			return a.Round > b.Round
		}
		return a.Position > b.Position
	})
	for _, call := range sorted {
		if err := call.Validate(); err != nil {
			return plan, err
		}
		if !call.Status.Terminal() {
			return plan, errors.New("boundary receipt requires a terminal call")
		}
		if call.ToolName == "history_read" || call.ToolName == "history_search" {
			continue
		}
		ref := domain.SupervisorToolResultReference(call)
		entry := map[string]any{"run_id": call.RunID, "turn": call.Turn, "attempt_id": call.AttemptID, "call_id": call.CallID, "tool": call.ToolName, "status": call.Status, "error_code": call.ErrorCode, "result_sha256": session.ContentSHA256(call.ResultJSON), "original_result": ref, "result_details_omitted": true, "operation_outcome_omitted": true}
		if effect := domain.ObservedSupervisorToolEffect(call); effect != nil {
			entry["observation"] = effect
		}
		if call.ToolName == "workspace_read" {
			entry["content_omitted"] = true
		}
		plan.Calls = append(plan.Calls, call)
		plan.Entries = append(plan.Entries, entry)
	}
	sizer, err := newBoundaryTokenSizer(plan, plan.Entries)
	if err != nil {
		return plan, err
	}
	plan.Content = sizer.render(plan)
	plan.BaselineTokens = sizer.totalTokens()
	return plan, nil
}

func supervisorBoundaryReceiptSuffix(selected, returned int) string {
	return fmt.Sprintf("\nSelected %d of %d returned receipts (the lookup itself is bounded). Recall wrappers are excluded. Other details or receipts are omitted. Omitted or unknown outcomes need exact readback before inferring success; tool completed alone never proves a write. A citation records a claim, not independent proof.\n", selected, returned)
}

func renderSupervisorBoundaryReceipt(plan supervisorBoundaryReceipt, entries []map[string]any) (string, error) {
	lines := make([]string, 0, len(entries)+len(plan.SummaryLines))
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return "", err
		}
		lines = append(lines, string(data))
	}
	lines = append(lines, plan.SummaryLines...)
	return renderSupervisorBoundaryReceiptLines(plan, lines, len(entries)), nil
}

func renderSupervisorBoundaryReceiptLines(plan supervisorBoundaryReceipt, lines []string, selected int) string {
	return supervisorBoundaryReceiptPrefix + strings.Join(lines, "\n") + supervisorBoundaryReceiptSuffix(selected, plan.Returned)
}

func supervisorBoundaryReceiptTokens(plan supervisorBoundaryReceipt, content string) int {
	return estimateModelRequestTokens(llm.ChatRequest{Messages: []llm.Message{toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, content)}}) - 8
}

type asciiTokenRun struct {
	bytes      int
	words      int
	leadSpace  bool
	trailSpace bool
}

func concatASCIIRun(a, b asciiTokenRun) asciiTokenRun {
	if a.bytes == 0 {
		return b
	}
	if b.bytes == 0 {
		return a
	}
	words := a.words + b.words
	if a.words > 0 && b.words > 0 && !a.trailSpace && !b.leadSpace {
		words--
	}
	return asciiTokenRun{
		bytes:      a.bytes + b.bytes,
		words:      words,
		leadSpace:  a.leadSpace,
		trailSpace: b.trailSpace,
	}
}

func asciiRunTokens(r asciiTokenRun) int {
	if r.bytes == 0 {
		return 0
	}
	byChars := (r.bytes + 3) / 4
	if r.words > byChars {
		return r.words
	}
	return byChars
}

func measureASCIIRun(s string) asciiTokenRun {
	if s == "" {
		return asciiTokenRun{}
	}
	words := 0
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		space := c == ' ' || (c >= '\t' && c <= '\r')
		if !space && !inWord {
			words++
			inWord = true
		} else if space {
			inWord = false
		}
	}
	c0 := s[0]
	cn := s[len(s)-1]
	return asciiTokenRun{
		bytes:      len(s),
		words:      words,
		leadSpace:  c0 == ' ' || (c0 >= '\t' && c0 <= '\r'),
		trailSpace: cn == ' ' || (cn >= '\t' && cn <= '\r'),
	}
}

type tokenMonoid struct {
	hasNonASCII bool
	lead        asciiTokenRun
	midTokens   int
	trail       asciiTokenRun
}

func concatTokenMonoid(a, b tokenMonoid) tokenMonoid {
	if !a.hasNonASCII && !b.hasNonASCII {
		return tokenMonoid{lead: concatASCIIRun(a.lead, b.lead)}
	}
	if !a.hasNonASCII && b.hasNonASCII {
		return tokenMonoid{
			hasNonASCII: true,
			lead:        concatASCIIRun(a.lead, b.lead),
			midTokens:   b.midTokens,
			trail:       b.trail,
		}
	}
	if a.hasNonASCII && !b.hasNonASCII {
		return tokenMonoid{
			hasNonASCII: true,
			lead:        a.lead,
			midTokens:   a.midTokens,
			trail:       concatASCIIRun(a.trail, b.lead),
		}
	}
	return tokenMonoid{
		hasNonASCII: true,
		lead:        a.lead,
		midTokens:   a.midTokens + asciiRunTokens(concatASCIIRun(a.trail, b.lead)) + b.midTokens,
		trail:       b.trail,
	}
}

func evalTokenMonoid(m tokenMonoid) int {
	if !m.hasNonASCII {
		return asciiRunTokens(m.lead)
	}
	return asciiRunTokens(m.lead) + m.midTokens + asciiRunTokens(m.trail)
}

func measureTokenMonoid(s string) tokenMonoid {
	if s == "" {
		return tokenMonoid{}
	}
	var out tokenMonoid
	for len(s) > 0 {
		asciiEnd := 0
		for asciiEnd < len(s) && s[asciiEnd] < utf8.RuneSelf {
			asciiEnd++
		}
		if asciiEnd > 0 {
			run := measureASCIIRun(s[:asciiEnd])
			if !out.hasNonASCII {
				out.lead = concatASCIIRun(out.lead, run)
			} else {
				out.trail = concatASCIIRun(out.trail, run)
			}
			s = s[asciiEnd:]
			continue
		}
		_, size := utf8.DecodeRuneInString(s)
		if size <= 0 {
			size = 1
		}
		if !out.hasNonASCII {
			out.hasNonASCII = true
		} else {
			out.midTokens += asciiRunTokens(out.trail)
			out.trail = asciiTokenRun{}
		}
		out.midTokens += size
		s = s[size:]
	}
	return out
}

func jsonEscapeString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded[1 : len(encoded)-1])
}

func receiptEnvelopeFrameMonoids(sourceRef, sessionID string) (tokenMonoid, tokenMonoid) {
	const sentinel = "__RECEIPT_CONTENT_SENTINEL__"
	msg := session.ProjectContextMessage(session.NewEvidenceMessage(sessionID, session.SourceToolResult,
		sourceRef, sentinel))
	idx := strings.LastIndex(msg.Content, sentinel)
	return measureTokenMonoid(msg.Content[:idx]), measureTokenMonoid(msg.Content[idx+len(sentinel):])
}

type receiptTokenTree struct {
	n          int
	size       int
	headMonoid tokenMonoid
	tailMonoid tokenMonoid
	tree       []tokenMonoid
	lines      []string
}

func newReceiptTokenTree(sourceRef, sessionID, prefix, suffix string, summaryLines, rawLines []string) *receiptTokenTree {
	envPrefix, envSuffix := receiptEnvelopeFrameMonoids(sourceRef, sessionID)
	headMonoid := concatTokenMonoid(envPrefix, measureTokenMonoid(jsonEscapeString(redact.String(prefix))))

	tailMonoid := concatTokenMonoid(measureTokenMonoid(jsonEscapeString(redact.String(suffix))), envSuffix)
	if len(summaryLines) > 0 {
		summaryText := strings.Join(summaryLines, "\n")
		if len(rawLines) > 0 {
			summaryText = "\n" + summaryText
		}
		summaryMonoid := measureTokenMonoid(jsonEscapeString(redact.String(summaryText)))
		tailMonoid = concatTokenMonoid(summaryMonoid, tailMonoid)
	}

	n := len(rawLines)
	size := 1
	for size < n {
		size *= 2
	}
	tree := &receiptTokenTree{
		n:          n,
		size:       size,
		headMonoid: headMonoid,
		tailMonoid: tailMonoid,
		tree:       make([]tokenMonoid, 2*size),
		lines:      append([]string(nil), rawLines...),
	}
	for i, raw := range rawLines {
		tree.tree[size+i] = tree.leafMonoid(raw, i < n-1)
	}
	for k := size - 1; k >= 1; k-- {
		tree.tree[k] = concatTokenMonoid(tree.tree[2*k], tree.tree[2*k+1])
	}
	return tree
}

func newBoundaryTokenSizer(plan supervisorBoundaryReceipt, entries []map[string]any) (*receiptTokenTree, error) {
	rawLines := make([]string, len(entries))
	for i, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		rawLines[i] = string(data)
	}
	sourceRef := fmt.Sprintf("tool-boundary-%s", plan.AttemptID)
	suffix := supervisorBoundaryReceiptSuffix(len(entries), plan.Returned)
	return newReceiptTokenTree(sourceRef, plan.SessionID, supervisorBoundaryReceiptPrefix, suffix, plan.SummaryLines, rawLines), nil
}

func (s *receiptTokenTree) leafMonoid(rawLine string, withNewline bool) tokenMonoid {
	redacted := redact.String(rawLine)
	if withNewline {
		redacted += "\n"
	}
	return measureTokenMonoid(jsonEscapeString(redacted))
}

func (s *receiptTokenTree) setLeaf(i int, leaf tokenMonoid) {
	idx := s.size + i
	s.tree[idx] = leaf
	for idx >>= 1; idx >= 1; idx >>= 1 {
		s.tree[idx] = concatTokenMonoid(s.tree[2*idx], s.tree[2*idx+1])
	}
}

func (s *receiptTokenTree) totalTokens() int {
	var body tokenMonoid
	if s.n > 0 {
		body = s.tree[1]
	}
	full := concatTokenMonoid(s.headMonoid, concatTokenMonoid(body, s.tailMonoid))
	// +5 matches estimateModelMessageTokens framing (4 + EstimateTokens("user")=1).
	return 5 + evalTokenMonoid(full)
}

func (s *receiptTokenTree) trySetLine(i int, raw string, tokenBudget int) bool {
	newLeaf := s.leafMonoid(raw, i < s.n-1)
	oldLeaf := s.tree[s.size+i]
	s.setLeaf(i, newLeaf)
	if s.totalTokens() <= tokenBudget {
		s.lines[i] = raw
		return true
	}
	s.setLeaf(i, oldLeaf)
	return false
}

func (s *receiptTokenTree) trySet(i int, entry map[string]any, tokenBudget int) (string, bool) {
	data, err := json.Marshal(entry)
	if err != nil {
		return "", false
	}
	raw := string(data)
	if !s.trySetLine(i, raw, tokenBudget) {
		return "", false
	}
	return raw, true
}

type boundaryBatchCandidate struct {
	index int
	entry map[string]any
}

func (s *receiptTokenTree) trySetBatchLines(indices []int, rawLines []string, tokenBudget int) bool {
	if len(indices) == 0 {
		return true
	}
	oldLeaves := make([]tokenMonoid, len(indices))
	for j, idx := range indices {
		oldLeaves[j] = s.tree[s.size+idx]
		s.setLeaf(idx, s.leafMonoid(rawLines[j], idx < s.n-1))
	}
	if s.totalTokens() <= tokenBudget {
		for j, idx := range indices {
			s.lines[idx] = rawLines[j]
		}
		return true
	}
	for j := len(indices) - 1; j >= 0; j-- {
		s.setLeaf(indices[j], oldLeaves[j])
	}
	return false
}

func (s *receiptTokenTree) trySetBatch(candidates []boundaryBatchCandidate, tokenBudget int) ([]string, bool) {
	if len(candidates) == 0 {
		return nil, true
	}
	indices := make([]int, len(candidates))
	newLines := make([]string, len(candidates))
	for j, cand := range candidates {
		data, err := json.Marshal(cand.entry)
		if err != nil {
			return nil, false
		}
		indices[j] = cand.index
		newLines[j] = string(data)
	}
	if !s.trySetBatchLines(indices, newLines, tokenBudget) {
		return nil, false
	}
	return newLines, true
}

func (s *receiptTokenTree) renderWith(prefix, suffix string, summaryLines []string) string {
	allLines := make([]string, 0, len(s.lines)+len(summaryLines))
	allLines = append(allLines, s.lines...)
	allLines = append(allLines, summaryLines...)
	return prefix + strings.Join(allLines, "\n") + suffix
}

func (s *receiptTokenTree) render(plan supervisorBoundaryReceipt) string {
	return s.renderWith(supervisorBoundaryReceiptPrefix, supervisorBoundaryReceiptSuffix(len(s.lines), plan.Returned), plan.SummaryLines)
}

// Mandatory identity/effect/recall membership never changes during fitting.
// Contracts share capacity with the current-segment receipt before file bodies.
func supervisorBoundaryReceiptContent(plan supervisorBoundaryReceipt, tokenBudget int, blockers []domain.SupervisorToolCall, contractsOnly bool) (string, error) {
	content, _, err := supervisorBoundaryReceiptContentAndTokens(plan, tokenBudget, blockers, contractsOnly)
	return content, err
}

func supervisorBoundaryReceiptContentAndTokens(plan supervisorBoundaryReceipt, tokenBudget int, blockers []domain.SupervisorToolCall, contractsOnly bool) (string, int, error) {
	entries := make([]map[string]any, len(plan.Entries))
	for i, entry := range plan.Entries {
		entries[i] = cloneBoundaryReceiptEntry(entry)
	}
	baseline := plan.BaselineTokens
	if baseline <= 0 {
		baseline = supervisorBoundaryReceiptTokens(plan, plan.Content)
	}
	if baseline > tokenBudget {
		return "", 0, fmt.Errorf("boundary identities need %d tokens; capacity is %d", baseline, tokenBudget)
	}
	sizer, err := newBoundaryTokenSizer(plan, entries)
	if err != nil {
		return "", 0, err
	}
	keep := func(i int, result any, complete bool) bool {
		previous := entries[i]
		entry := cloneBoundaryReceiptEntry(previous)
		entry["result"] = result
		entry["result_excerpted"] = !complete
		delete(entry, "operation_outcome_omitted")
		entry["result_details_omitted"] = !complete
		if complete {
			delete(entry, "content_omitted")
		}
		if _, ok := sizer.trySet(i, entry, tokenBudget); !ok {
			entries[i] = previous
			return false
		}
		entries[i] = entry
		return true
	}
	// A completed boundary advances its turn. Other attempts of that turn in
	// the broader file-effect lookup preceded that completed boundary, even if
	// their failed round number was larger. Never order attempts by their IDs.
	completedAttempts := map[int]string{}
	for _, call := range plan.Calls {
		completedAttempts[call.Turn] = call.AttemptID
	}
	// Older failed attempts still shadow earlier turns, including actual writes
	// before that attempt failed. Preserve them; only order the completed source
	// after them within its own turn.
	priority := supervisorReceiptApplyPriorityCalls(plan.Calls, blockers, completedAttempts)
	whole := map[int]bool{}
	for i, call := range plan.Calls {
		if !priority.Priority[supervisorReceiptCallKey(call)] {
			continue
		}
		if result, ok := supervisorSegmentReceiptProjection(call); ok && keep(i, result, true) {
			// The apply contract is whole; unrelated diff/prose was projected out.
			updated := cloneBoundaryReceiptEntry(entries[i])
			updated["result_excerpted"] = true
			if _, ok := sizer.trySet(i, updated, tokenBudget); ok {
				entries[i] = updated
			} else {
				entries[i]["result_excerpted"] = true
			}
			whole[i] = true
		}
	}
	if !contractsOnly {
		groups, _ := supervisorWorkspaceCoverage(plan.Calls, nil)
		sort.SliceStable(groups, func(i, j int) bool {
			a, b := groups[i].pages[0].call, groups[j].pages[0].call
			if a.Turn != b.Turn {
				return a.Turn > b.Turn
			}
			if a.Round != b.Round {
				return a.Round > b.Round
			}
			return a.Position > b.Position
		})
		for _, group := range groups {
			candidates := make([]boundaryBatchCandidate, 0, len(group.needed))
			for _, page := range group.needed {
				entry := cloneBoundaryReceiptEntry(entries[page.index])
				entry["result"] = page.page
				delete(entry, "content_omitted")
				delete(entry, "operation_outcome_omitted")
				delete(entry, "result_details_omitted")
				candidates = append(candidates, boundaryBatchCandidate{index: page.index, entry: entry})
			}
			if _, ok := sizer.trySetBatch(candidates, tokenBudget); ok {
				for _, cand := range candidates {
					entries[cand.index] = cand.entry
					whole[cand.index] = true
				}
			}
		}
		seenPages := map[string]bool{}
		for i, call := range plan.Calls {
			if whole[i] {
				if _, key, ok := supervisorWorkspaceReadPage(call); ok {
					seenPages[key] = true
				}
				continue
			}
			if priority.Superseded[supervisorReceiptCallKey(call)] {
				continue
			}
			if page, key, ok := supervisorWorkspaceReadPage(call); ok {
				if !seenPages[key] && keep(i, page, true) {
					seenPages[key] = true
					whole[i] = true
				} else if result, ok := supervisorSegmentReceiptProjection(call); ok {
					keep(i, result, false)
				}
				continue
			}
			result, ok := supervisorSegmentReceiptProjection(call)
			if !ok {
				continue
			}
			// Keep cursors and a complete claim before optional prose. Never trim one.
			complete := call.ToolName == "mcp_tool_search"
			if keep(i, result, complete) {
				whole[i] = complete
			} else {
				if toolBoundaryOmitClaim(result) {
					keep(i, result, false)
				}
			}
		}
		for i, call := range plan.Calls {
			if whole[i] || call.ToolName == "workspace_read" || call.ToolName == "mcp_tool_search" || priority.Superseded[supervisorReceiptCallKey(call)] {
				continue
			}
			// An eligible contract that did not fit stays wholly omitted, not excerpted.
			if priority.Priority[supervisorReceiptCallKey(call)] {
				continue
			}
			projected, err := supervisorToolContextResult(call)
			if err != nil {
				return "", 0, err
			}
			var envelope supervisorToolResultEnvelope
			var result any
			if json.Unmarshal([]byte(projected), &envelope) != nil {
				continue
			}
			previous := entries[i]
			entry := cloneBoundaryReceiptEntry(previous)
			if json.Unmarshal([]byte(envelope.Stdout), &result) == nil {
				entry["result"] = compactToolBoundaryValue(supervisorHistoricalToolResult(call, result), "", 0)
				delete(entry, "operation_outcome_omitted")
			}
			metadata := map[string]any{}
			for key, value := range envelope.Metadata {
				if !toolBoundaryContainsValue(entry["result"], key, value) && (toolBoundaryReceiptString(key) || toolBoundaryScalarMetadata(value)) {
					metadata[key] = value
				}
			}
			if len(metadata) > 0 {
				entry["metadata"] = metadata
			}
			if envelope.Stderr != "" {
				entry["stderr_excerpt"] = boundedFailedEvidenceText(envelope.Stderr, 256)
			}
			if _, ok := sizer.trySet(i, entry, tokenBudget); ok {
				entries[i] = entry
			} else {
				entries[i] = previous
			}
		}
	}
	return sizer.render(plan), sizer.totalTokens(), nil
}

func cloneBoundaryReceiptEntry(entry map[string]any) map[string]any {
	out := make(map[string]any, len(entry)+1)
	for k, v := range entry {
		out[k] = v
	}
	return out
}

func buildAggregateBoundaryOmissionSummary(calls []domain.SupervisorToolCall, entries []map[string]any, omitted []int) string {
	tools := map[string]int{}
	statuses := map[string]int{}
	minTurn, maxTurn := 0, 0
	minRound, maxRound := 0, 0
	omittedEffects := 0
	for k, idx := range omitted {
		call := calls[idx]
		tools[call.ToolName]++
		statuses[string(call.Status)]++
		if entries[idx]["observation"] != nil {
			omittedEffects++
		}
		if k == 0 || call.Turn < minTurn {
			minTurn = call.Turn
		}
		if k == 0 || call.Turn > maxTurn {
			maxTurn = call.Turn
		}
		if k == 0 || call.Round < minRound {
			minRound = call.Round
		}
		if k == 0 || call.Round > maxRound {
			maxRound = call.Round
		}
	}
	summary := map[string]any{
		"omitted_receipt_summary": true,
		"omitted_calls":           len(omitted),
		"omitted_turn_range":      []int{minTurn, maxTurn},
		"omitted_round_range":     []int{minRound, maxRound},
		"tools":                   tools,
		"statuses":                statuses,
		"note":                    "Earlier call identities were folded to fit the model context window; use history_search if exact call_id readback is required.",
	}
	if omittedEffects > 0 {
		summary["omitted_file_effects"] = omittedEffects
	}
	encoded, _ := json.Marshal(summary)
	return string(encoded)
}

func buildPerRoundBoundaryOmissionSummaries(calls []domain.SupervisorToolCall, entries []map[string]any, omitted []int) []string {
	type roundKey struct {
		turn      int
		attemptID string
		round     int
	}
	type roundGroup struct {
		key            roundKey
		runID          string
		count          int
		tools          map[string]int
		statuses       map[string]int
		firstCallID    string
		lastCallID     string
		omittedEffects int
	}
	order := make([]roundKey, 0)
	groups := map[roundKey]*roundGroup{}
	for _, idx := range omitted {
		call := calls[idx]
		rk := roundKey{turn: call.Turn, attemptID: call.AttemptID, round: call.Round}
		g := groups[rk]
		if g == nil {
			g = &roundGroup{
				key:         rk,
				runID:       call.RunID,
				tools:       map[string]int{},
				statuses:    map[string]int{},
				firstCallID: call.CallID,
			}
			groups[rk] = g
			order = append(order, rk)
		}
		g.count++
		g.tools[call.ToolName]++
		g.statuses[string(call.Status)]++
		g.lastCallID = call.CallID
		if entries[idx]["observation"] != nil {
			g.omittedEffects++
		}
	}
	lines := make([]string, 0, len(order))
	for _, rk := range order {
		g := groups[rk]
		summary := map[string]any{
			"omitted_receipt_summary": true,
			"run_id":                  g.runID,
			"turn":                    g.key.turn,
			"attempt_id":              g.key.attemptID,
			"round":                   g.key.round,
			"omitted_calls":           g.count,
			"tools":                   g.tools,
			"statuses":                g.statuses,
			"first_call_id":           g.firstCallID,
			"last_call_id":            g.lastCallID,
		}
		if g.omittedEffects > 0 {
			summary["omitted_file_effects"] = g.omittedEffects
		}
		encoded, _ := json.Marshal(summary)
		lines = append(lines, string(encoded))
	}
	return lines
}

func foldSupervisorBoundaryReceiptPlan(plan supervisorBoundaryReceipt, capacity int, blockers []domain.SupervisorToolCall) (supervisorBoundaryReceipt, bool) {
	n := len(plan.Entries)
	if n == 0 || capacity <= 0 {
		return plan, false
	}
	envPrefix, envSuffix := receiptEnvelopeFrameMonoids(fmt.Sprintf("tool-boundary-%s", plan.AttemptID), plan.SessionID)
	headMonoid := concatTokenMonoid(envPrefix, measureTokenMonoid(jsonEscapeString(redact.String(supervisorBoundaryReceiptPrefix))))

	allIdx := make([]int, n)
	for i := range allIdx {
		allIdx[i] = i
	}
	maxSummaryLine := buildAggregateBoundaryOmissionSummary(plan.Calls, plan.Entries, allIdx)
	maxSummaryTokens := 5 + evalTokenMonoid(concatTokenMonoid(headMonoid, concatTokenMonoid(
		measureTokenMonoid(jsonEscapeString(redact.String(maxSummaryLine+supervisorBoundaryReceiptSuffix(0, plan.Returned)))),
		envSuffix)))
	if maxSummaryTokens > capacity {
		return plan, false
	}

	completedAttempts := map[int]string{}
	for _, call := range plan.Calls {
		completedAttempts[call.Turn] = call.AttemptID
	}
	priority := supervisorReceiptApplyPriorityCalls(plan.Calls, blockers, completedAttempts)
	tier1 := make([]bool, n)
	var tier1Indices, tier2Indices []int
	for i, call := range plan.Calls {
		if priority.Priority[supervisorReceiptCallKey(call)] || plan.Entries[i]["observation"] != nil {
			tier1[i] = true
			tier1Indices = append(tier1Indices, i)
		} else {
			tier2Indices = append(tier2Indices, i)
		}
	}

	rawLines := make([]string, n)
	leafWithNL := make([]tokenMonoid, n)
	reservedLines := make([]string, n)
	for i, entry := range plan.Entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return plan, false
		}
		raw := string(data)
		rawLines[i] = raw
		reservedLines[i] = raw
		leafWithNL[i] = measureTokenMonoid(jsonEscapeString(redact.String(raw) + "\n"))
	}

	projectedLines := make([]string, n)
	projectedLeafWithNL := make([]tokenMonoid, n)
	hasProjected := make([]bool, n)
	for _, idx := range tier1Indices {
		call := plan.Calls[idx]
		if !priority.Priority[supervisorReceiptCallKey(call)] {
			continue
		}
		result, ok := supervisorSegmentReceiptProjection(call)
		if !ok {
			continue
		}
		projEntry := cloneBoundaryReceiptEntry(plan.Entries[idx])
		projEntry["result"] = result
		projEntry["result_excerpted"] = false
		delete(projEntry, "operation_outcome_omitted")
		projEntry["result_details_omitted"] = false
		delete(projEntry, "content_omitted")
		data, err := json.Marshal(projEntry)
		if err != nil {
			continue
		}
		projRaw := string(data)
		projectedLines[idx] = projRaw
		projectedLeafWithNL[idx] = measureTokenMonoid(jsonEscapeString(redact.String(projRaw) + "\n"))
		hasProjected[idx] = true
	}

	// Reserve space for the aggregate summary line + newline + suffix + envelope.
	aggReserve := concatTokenMonoid(
		measureTokenMonoid(jsonEscapeString(redact.String("\n"+maxSummaryLine+supervisorBoundaryReceiptSuffix(plan.Returned, plan.Returned)))),
		envSuffix,
	)

	kept := make([]bool, n)
	var keptOrder []int // order in which entries were added (for popping oldest if needed)
	curBody := tokenMonoid{}
	fitsWithCurrent := func(extra tokenMonoid) bool {
		total := 5 + evalTokenMonoid(concatTokenMonoid(headMonoid, concatTokenMonoid(extra, aggReserve)))
		return total <= capacity
	}
	for _, idx := range tier1Indices {
		if hasProjected[idx] {
			next := concatTokenMonoid(curBody, projectedLeafWithNL[idx])
			if fitsWithCurrent(next) {
				kept[idx] = true
				keptOrder = append(keptOrder, idx)
				curBody = next
				reservedLines[idx] = projectedLines[idx]
				continue
			}
		}
		next := concatTokenMonoid(curBody, leafWithNL[idx])
		if fitsWithCurrent(next) {
			kept[idx] = true
			keptOrder = append(keptOrder, idx)
			curBody = next
		}
	}
	for _, idx := range tier2Indices {
		next := concatTokenMonoid(curBody, leafWithNL[idx])
		if !fitsWithCurrent(next) {
			break
		}
		kept[idx] = true
		keptOrder = append(keptOrder, idx)
		curBody = next
	}

	buildCandidate := func(perRound bool) (supervisorBoundaryReceipt, []string, []int) {
		var omitted []int
		var keptLines []string
		folded := supervisorBoundaryReceipt{
			Returned:  plan.Returned,
			SessionID: plan.SessionID,
			AttemptID: plan.AttemptID,
		}
		for i := 0; i < n; i++ {
			if kept[i] {
				folded.Calls = append(folded.Calls, plan.Calls[i])
				folded.Entries = append(folded.Entries, plan.Entries[i])
				keptLines = append(keptLines, reservedLines[i])
			} else {
				omitted = append(omitted, i)
			}
		}
		if len(omitted) > 0 {
			if perRound {
				folded.SummaryLines = buildPerRoundBoundaryOmissionSummaries(plan.Calls, plan.Entries, omitted)
			} else {
				folded.SummaryLines = []string{buildAggregateBoundaryOmissionSummary(plan.Calls, plan.Entries, omitted)}
			}
		}
		return folded, keptLines, omitted
	}

	// Prefer per-round omission summaries when they fit without sacrificing too many kept calls.
	if candidate, keptLines, omitted := buildCandidate(true); len(omitted) > 0 {
		sourceRef := fmt.Sprintf("tool-boundary-%s", candidate.AttemptID)
		suffix := supervisorBoundaryReceiptSuffix(len(keptLines), candidate.Returned)
		sizer := newReceiptTokenTree(sourceRef, candidate.SessionID, supervisorBoundaryReceiptPrefix, suffix, candidate.SummaryLines, keptLines)
		if tokens := sizer.totalTokens(); tokens <= capacity {
			candidate.Content = sizer.render(candidate)
			candidate.BaselineTokens = tokens
			return candidate, true
		}
	}

	// Fall back to a single compact aggregate omission summary, popping oldest kept calls if needed.
	for {
		candidate, keptLines, _ := buildCandidate(false)
		sourceRef := fmt.Sprintf("tool-boundary-%s", candidate.AttemptID)
		suffix := supervisorBoundaryReceiptSuffix(len(keptLines), candidate.Returned)
		sizer := newReceiptTokenTree(sourceRef, candidate.SessionID, supervisorBoundaryReceiptPrefix, suffix, candidate.SummaryLines, keptLines)
		if tokens := sizer.totalTokens(); tokens <= capacity {
			candidate.Content = sizer.render(candidate)
			candidate.BaselineTokens = tokens
			return candidate, true
		}
		if len(keptOrder) == 0 {
			return plan, false
		}
		popIdx := keptOrder[len(keptOrder)-1]
		if reservedLines[popIdx] != rawLines[popIdx] {
			reservedLines[popIdx] = rawLines[popIdx]
			continue
		}
		keptOrder = keptOrder[:len(keptOrder)-1]
		kept[popIdx] = false
	}
}

func fitSupervisorBoundaryReceiptRequest(request llm.ChatRequest, plan supervisorBoundaryReceipt, inputLimit int, blockers []domain.SupervisorToolCall, contractsOnly bool) (llm.ChatRequest, error) {
	if plan.Content == "" {
		return request, nil
	}
	owned := toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, plan.Content)
	// First pass owns minimal content; subsequent pass owns the exact first fit.
	if content := request.Metadata["context_boundary_receipt_content"]; content != "" {
		owned = toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, content)
	}
	index, err := ownedReceiptMessageIndex(request, owned)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	without := request
	without.Messages = append(append([]llm.Message(nil), request.Messages[:index]...), request.Messages[index+1:]...)
	capacity := inputLimit - estimateModelRequestTokens(without)
	workingPlan := plan
	baseline := plan.BaselineTokens
	if baseline <= 0 {
		baseline = supervisorBoundaryReceiptTokens(plan, plan.Content)
	}
	if capacity < baseline {
		folded, ok := foldSupervisorBoundaryReceiptPlan(plan, capacity, blockers)
		if !ok {
			return request, nil
		}
		workingPlan = folded
	}
	content, tokens, err := supervisorBoundaryReceiptContentAndTokens(workingPlan, capacity, blockers, contractsOnly)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	request.Messages = append([]llm.Message(nil), request.Messages...)
	request.Messages[index] = toolBoundaryEvidenceMessage(plan.SessionID, plan.AttemptID, content)
	metadata := map[string]string{}
	for k, v := range request.Metadata {
		metadata[k] = v
	}
	metadata["context_boundary_receipt_tokens"] = strconv.Itoa(tokens)
	metadata["context_boundary_receipt_budget"] = strconv.Itoa(capacity)
	// Internal ownership only; removed before dispatch rather than sent as metadata.
	metadata["context_boundary_receipt_content"] = content
	request.Metadata = metadata
	if estimateModelRequestTokens(request) > inputLimit {
		return llm.ChatRequest{}, errors.New("boundary receipt exceeds prepared input capacity")
	}
	return request, nil
}
