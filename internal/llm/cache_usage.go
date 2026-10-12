package llm

import "errors"

type openAIInputTokenDetails struct {
	CachedTokens     *int `json:"cached_tokens"`
	CacheWriteTokens *int `json:"cache_write_tokens"`
}

func applyOpenAICacheUsage(usage Usage, details *openAIInputTokenDetails) (Usage, error) {
	if details != nil {
		if details.CachedTokens != nil {
			usage.CacheReadTokens = *details.CachedTokens
			usage.CacheUsageReported = true
		}
		if details.CacheWriteTokens != nil {
			usage.CacheCreationTokens = *details.CacheWriteTokens
			usage.CacheUsageReported = true
		}
	}
	return usage, usage.Validate()
}

type anthropicCacheCreation struct {
	FiveMinuteTokens *int `json:"ephemeral_5m_input_tokens"`
	OneHourTokens    *int `json:"ephemeral_1h_input_tokens"`
}

// Pointers distinguish a missing cumulative stream update from an explicit
// zero. InputTokens is the wire's uncached input, unlike canonical Usage.
type anthropicUsage struct {
	InputTokens         *int                    `json:"input_tokens"`
	OutputTokens        *int                    `json:"output_tokens"`
	CacheReadTokens     *int                    `json:"cache_read_input_tokens"`
	CacheCreationTokens *int                    `json:"cache_creation_input_tokens"`
	CacheCreation       *anthropicCacheCreation `json:"cache_creation"`
}

func (wire anthropicUsage) normalized() (Usage, error) {
	// Compatible providers historically may omit usage entirely. Preserve an
	// unknown receipt so the existing budget path retains its reservation;
	// do not confuse missing usage with reported cache zeroes.
	if wire.InputTokens == nil && wire.OutputTokens == nil && wire.CacheReadTokens == nil &&
		wire.CacheCreationTokens == nil && wire.CacheCreation == nil {
		return Usage{}, nil
	}
	if wire.InputTokens == nil || wire.OutputTokens == nil {
		return Usage{}, errors.New("Anthropic token count is missing")
	}
	usage := Usage{InputTokens: *wire.InputTokens, OutputTokens: *wire.OutputTokens}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return Usage{}, errors.New("Anthropic token count is negative")
	}
	if wire.CacheReadTokens != nil {
		usage.CacheReadTokens = *wire.CacheReadTokens
		usage.CacheUsageReported = true
	}
	if wire.CacheCreationTokens != nil {
		usage.CacheCreationTokens = *wire.CacheCreationTokens
		usage.CacheUsageReported = true
	}
	if wire.CacheCreation != nil {
		if wire.CacheCreation.FiveMinuteTokens == nil || wire.CacheCreation.OneHourTokens == nil {
			return Usage{}, errors.New("Anthropic cache creation breakdown is incomplete")
		}
		usage.CacheCreation5mTokens = *wire.CacheCreation.FiveMinuteTokens
		usage.CacheCreation1hTokens = *wire.CacheCreation.OneHourTokens
		usage.CacheCreationBreakdownKnown = true
		usage.CacheUsageReported = true
		maxInt := int(^uint(0) >> 1)
		if usage.CacheCreation5mTokens < 0 || usage.CacheCreation1hTokens < 0 ||
			usage.CacheCreation5mTokens > maxInt-usage.CacheCreation1hTokens {
			return Usage{}, errors.New("Anthropic cache creation breakdown is invalid")
		}
		creation := usage.CacheCreation5mTokens + usage.CacheCreation1hTokens
		if wire.CacheCreationTokens == nil {
			usage.CacheCreationTokens = creation
		} else if usage.CacheCreationTokens != creation {
			return Usage{}, errors.New("Anthropic cache creation breakdown does not match total creation")
		}
	}
	maxInt := int(^uint(0) >> 1)
	for _, tokens := range []int{usage.CacheReadTokens, usage.CacheCreationTokens} {
		if tokens < 0 || usage.InputTokens > maxInt-tokens {
			return Usage{}, errors.New("Anthropic input token count is invalid")
		}
		usage.InputTokens += tokens
	}
	if usage.InputTokens > maxInt-usage.OutputTokens {
		return Usage{}, errors.New("Anthropic token count overflow")
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return usage, usage.Validate()
}

// Anthropic message_delta usage is cumulative. Omitted fields retain their
// message_start values; an explicit zero is an update, never an omission.
func (wire anthropicUsage) merge(update anthropicUsage) (anthropicUsage, error) {
	merged := wire
	for _, field := range []struct {
		current  **int
		incoming *int
	}{
		{&merged.InputTokens, update.InputTokens}, {&merged.OutputTokens, update.OutputTokens},
		{&merged.CacheReadTokens, update.CacheReadTokens}, {&merged.CacheCreationTokens, update.CacheCreationTokens},
	} {
		if field.incoming != nil {
			if *field.incoming < 0 || (*field.current != nil && *field.incoming < **field.current) {
				return wire, errors.New("Anthropic cumulative usage decreased")
			}
			*field.current = field.incoming
		}
	}
	if update.CacheCreation != nil {
		creation := anthropicCacheCreation{}
		if wire.CacheCreation != nil {
			creation = *wire.CacheCreation
		}
		for _, field := range []struct {
			current  **int
			incoming *int
		}{
			{&creation.FiveMinuteTokens, update.CacheCreation.FiveMinuteTokens},
			{&creation.OneHourTokens, update.CacheCreation.OneHourTokens},
		} {
			if field.incoming != nil {
				if *field.incoming < 0 || (*field.current != nil && *field.incoming < **field.current) {
					return wire, errors.New("Anthropic cumulative cache creation decreased")
				}
				*field.current = field.incoming
			}
		}
		merged.CacheCreation = &creation
	}
	if _, err := merged.normalized(); err != nil {
		return wire, err
	}
	return merged, nil
}
