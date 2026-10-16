package toolgateway_test

import (
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type proposalStore interface{ scriptprocess.Store }

func TestScriptProcessCapabilitiesDescribeProductionAndOptionalStores(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gateway-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, tc := range []struct {
		name            string
		store           toolgateway.Store
		proposals, runs bool
		reasons         int
	}{
		{"production SQLite", st, true, true, 0},
		{"base only", struct{ toolgateway.Store }{st}, false, false, 2},
		{"proposal only", struct {
			toolgateway.Store
			proposalStore
		}{st, st}, true, false, 1},
		{"uninstalled", nil, false, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := toolgateway.New(tc.store, policy.NewDefaultChecker()).ScriptProcessCapabilities()
			if value.ProposalsAvailable != tc.proposals || value.RunCreationAvailable != tc.runs || len(value.UnavailableReasons) != tc.reasons {
				t.Fatalf("capability diagnostic=%+v", value)
			}
		})
	}
}
