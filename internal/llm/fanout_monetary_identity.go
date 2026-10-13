package llm

import (
	"hash/fnv"
	"strconv"
)

// ReadOnlyFanoutMonetaryAttemptNumber binds a reservation and its durable
// receipt to one shard attempt. Recovery starts a separately billed request.
func ReadOnlyFanoutMonetaryAttemptNumber(executionID string, ordinal, attempt int) int {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(executionID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.Itoa(ordinal)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.Itoa(attempt)))
	return int(hash.Sum64()&0x7FFFFFFF) + 1
}
