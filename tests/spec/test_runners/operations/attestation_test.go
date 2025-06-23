package operations

import (
	"context"
	"fmt"
	"testing"

	"github.com/protolambda/zrnt/eth2/beacon/altair"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/deneb"
	"github.com/protolambda/zrnt/eth2/beacon/electra"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/zrnt/tests/spec/test_util"
)

type AttestationTestCase struct {
	test_util.BaseTransitionTest
	Attestation interface{}
}

func (c *AttestationTestCase) Load(t *testing.T, forkName test_util.ForkName, readPart test_util.TestPartReader) {
	c.BaseTransitionTest.Load(t, forkName, readPart)
	
	// Load fork-specific attestation type
	switch forkName {
	case "electra":
		var att electra.Attestation
		test_util.LoadSpecObj(t, "attestation", &att, readPart)
		c.Attestation = &att
	default:
		var att phase0.Attestation
		test_util.LoadSpecObj(t, "attestation", &att, readPart)
		c.Attestation = &att
	}
}

func (c *AttestationTestCase) Run() error {
	epc, err := common.NewEpochsContext(c.Spec, c.Pre)
	if err != nil {
		return err
	}
	// Check for Electra state first to use the correct attestation type
	if s, ok := c.Pre.(*electra.BeaconStateView); ok {
		// Electra attestations are handled differently
		att := c.Attestation.(*electra.Attestation)
		return electra.ProcessAttestation(context.Background(), c.Spec, epc, s, att)
	} else if s, ok := c.Pre.(phase0.Phase0PendingAttestationsBeaconState); ok {
		att := c.Attestation.(*phase0.Attestation)
		return phase0.ProcessAttestation(c.Spec, epc, s, att)
	} else if s, ok := c.Pre.(altair.AltairLikeBeaconState); ok {
		att := c.Attestation.(*phase0.Attestation)
		switch c.Fork {
		case "altair", "bellatrix", "capella":
			return altair.ProcessAttestation(c.Spec, epc, s, att)
		case "deneb":
			return deneb.ProcessAttestation(c.Spec, epc, s, att)
		default:
			return fmt.Errorf("unrecognized fork: %s", c.Fork)
		}
	} else {
		return fmt.Errorf("unrecognized state type: %T", c.Pre)
	}
}

func TestAttestation(t *testing.T) {
	test_util.RunTransitionTest(t, test_util.AllForks, "operations", "attestation",
		func() test_util.TransitionTest { return new(AttestationTestCase) })
}
