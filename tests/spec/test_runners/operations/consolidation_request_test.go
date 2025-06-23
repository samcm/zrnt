package operations

import (
	"context"
	"testing"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/electra"
	"github.com/protolambda/zrnt/tests/spec/test_util"
)

type ConsolidationRequestTestCase struct {
	test_util.BaseTransitionTest
	ConsolidationRequest common.ConsolidationRequest
}

func (c *ConsolidationRequestTestCase) Load(t *testing.T, forkName test_util.ForkName, readPart test_util.TestPartReader) {
	c.BaseTransitionTest.Load(t, forkName, readPart)
	test_util.LoadSSZ(t, "consolidation_request", &c.ConsolidationRequest, readPart)
}

func (c *ConsolidationRequestTestCase) Run() error {
	epc, err := common.NewEpochsContext(c.Spec, c.Pre)
	if err != nil {
		return err
	}

	// Cast to Electra state
	state, ok := c.Pre.(*electra.BeaconStateView)
	if !ok {
		return nil // Skip for non-Electra states
	}

	return electra.ProcessConsolidationRequest(context.Background(), c.Spec, epc, state, &c.ConsolidationRequest)
}

func TestConsolidationRequest(t *testing.T) {
	test_util.RunTransitionTest(t, []test_util.ForkName{"electra"}, "operations", "consolidation_request",
		func() test_util.TransitionTest { return new(ConsolidationRequestTestCase) })
}