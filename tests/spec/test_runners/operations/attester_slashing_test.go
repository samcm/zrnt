package operations

import (
	"context"
	"testing"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/electra"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/zrnt/tests/spec/test_util"
)

type AttesterSlashingTestCase struct {
	test_util.BaseTransitionTest
	AttesterSlashing interface{}
}

func (c *AttesterSlashingTestCase) Load(t *testing.T, forkName test_util.ForkName, readPart test_util.TestPartReader) {
	c.BaseTransitionTest.Load(t, forkName, readPart)
	
	// Load fork-specific attester slashing type
	switch forkName {
	case "electra":
		var as electra.AttesterSlashing
		test_util.LoadSpecObj(t, "attester_slashing", &as, readPart)
		c.AttesterSlashing = &as
	default:
		var as phase0.AttesterSlashing
		test_util.LoadSpecObj(t, "attester_slashing", &as, readPart)
		c.AttesterSlashing = &as
	}
}

func (c *AttesterSlashingTestCase) Run() error {
	epc, err := common.NewEpochsContext(c.Spec, c.Pre)
	if err != nil {
		return err
	}
	
	// Check if this is an Electra state
	if s, ok := c.Pre.(*electra.BeaconStateView); ok {
		// Electra has modified attester slashing processing
		as := c.AttesterSlashing.(*electra.AttesterSlashing)
		return electra.ProcessAttesterSlashing(context.Background(), c.Spec, epc, s, as)
	}
	
	as := c.AttesterSlashing.(*phase0.AttesterSlashing)
	return phase0.ProcessAttesterSlashing(c.Spec, epc, c.Pre, as)
}

func TestAttesterSlashing(t *testing.T) {
	test_util.RunTransitionTest(t, test_util.AllForks, "operations", "attester_slashing",
		func() test_util.TransitionTest { return new(AttesterSlashingTestCase) })
}
