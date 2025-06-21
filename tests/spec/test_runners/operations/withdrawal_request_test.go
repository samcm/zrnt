package operations

import (
	"context"
	"testing"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/electra"
	"github.com/protolambda/zrnt/tests/spec/test_util"
)

type WithdrawalRequestTestCase struct {
	test_util.BaseTransitionTest
	ExecutionLayerWithdrawalRequest common.WithdrawalRequest
}

func (c *WithdrawalRequestTestCase) Load(t *testing.T, forkName test_util.ForkName, readPart test_util.TestPartReader) {
	c.BaseTransitionTest.Load(t, forkName, readPart)
	test_util.LoadSSZ(t, "execution_layer_withdrawal_request", &c.ExecutionLayerWithdrawalRequest, readPart)
}

func (c *WithdrawalRequestTestCase) Run() error {
	epc, err := common.NewEpochsContext(c.Spec, c.Pre)
	if err != nil {
		return err
	}

	// Cast to Electra state
	state, ok := c.Pre.(*electra.BeaconStateView)
	if !ok {
		return nil // Skip for non-Electra states
	}

	return electra.ProcessWithdrawalRequest(context.Background(), c.Spec, epc, state, &c.ExecutionLayerWithdrawalRequest)
}

func TestWithdrawalRequest(t *testing.T) {
	test_util.RunTransitionTest(t, []test_util.ForkName{"electra"}, "operations", "withdrawal_request",
		func() test_util.TransitionTest { return new(WithdrawalRequestTestCase) })
}