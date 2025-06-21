package electra

import (
	"context"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/altair"
	"github.com/protolambda/zrnt/eth2/beacon/capella"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
)

// ProcessEpoch processes an epoch transition for Electra
func (state *BeaconStateView) ProcessEpoch(ctx context.Context, spec *common.Spec, epc *common.EpochsContext) error {
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	flats, err := common.FlattenValidators(vals)
	if err != nil {
		return err
	}
	attesterData, err := altair.ComputeEpochAttesterData(ctx, spec, epc, flats, state)
	if err != nil {
		return err
	}
	just := phase0.JustificationStakeData{
		CurrentEpoch:                  epc.CurrentEpoch.Epoch,
		TotalActiveStake:              epc.TotalActiveStake,
		PrevEpochUnslashedTargetStake: attesterData.PrevEpochUnslashedStake.TargetStake,
		CurrEpochUnslashedTargetStake: attesterData.CurrEpochUnslashedTargetStake,
	}
	if err := phase0.ProcessEpochJustification(ctx, spec, &just, state); err != nil {
		return err
	}
	if err := altair.ProcessInactivityUpdates(ctx, spec, attesterData, state); err != nil {
		return err
	}
	if err := altair.ProcessEpochRewardsAndPenalties(ctx, spec, epc, attesterData, state); err != nil {
		return err
	}
	// [Modified in Electra:EIP7251]
	if err := ProcessRegistryUpdates(ctx, spec, epc, flats, state); err != nil {
		return err
	}
	// [Modified in Electra:EIP7251]
	if err := ProcessSlashings(ctx, spec, epc, flats, state); err != nil {
		return err
	}
	if err := phase0.ProcessEth1DataReset(ctx, spec, epc, state); err != nil {
		return err
	}
	// [New in Electra:EIP7251]
	if err := ProcessPendingDeposits(ctx, spec, epc, state); err != nil {
		return err
	}
	// [New in Electra:EIP7251]
	if err := ProcessPendingConsolidations(ctx, spec, epc, state); err != nil {
		return err
	}
	// [Modified in Electra:EIP7251]
	if err := ProcessEffectiveBalanceUpdates(ctx, spec, epc, flats, state); err != nil {
		return err
	}
	if err := phase0.ProcessSlashingsReset(ctx, spec, epc, state); err != nil {
		return err
	}
	if err := phase0.ProcessRandaoMixesReset(ctx, spec, epc, state); err != nil {
		return err
	}
	if err := capella.ProcessHistoricalSummariesUpdate(ctx, spec, epc, state); err != nil {
		return err
	}
	if err := altair.ProcessParticipationFlagUpdates(ctx, spec, state); err != nil {
		return err
	}
	if err := ProcessSyncCommitteeUpdates(ctx, spec, epc, state); err != nil {
		return err
	}
	return nil
}

// ProcessBlock processes a beacon block for Electra
func (state *BeaconStateView) ProcessBlock(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, benv *common.BeaconBlockEnvelope) error {
	body, ok := benv.Body.(*BeaconBlockBody)
	if !ok {
		return fmt.Errorf("unexpected block type %T in Electra ProcessBlock", benv.Body)
	}
	expectedProposer, err := epc.GetBeaconProposer(benv.Slot)
	if err != nil {
		return err
	}
	if err := common.ProcessHeader(ctx, spec, state, &benv.BeaconBlockHeader, expectedProposer); err != nil {
		return err
	}
	// [Modified in Electra:EIP7251]
	if err := ProcessWithdrawals(ctx, spec, state, &body.ExecutionPayload); err != nil {
		return err
	}
	// [Modified in Electra:EIP6110]
	eng, ok := spec.ExecutionEngine.(ExecutionEngine)
	if !ok {
		return fmt.Errorf("provided execution-engine interface does not support Electra: %T", spec.ExecutionEngine)
	}
	if err := ProcessExecutionPayload(ctx, spec, state, body, eng); err != nil {
		return err
	}
	if err := phase0.ProcessRandaoReveal(ctx, spec, epc, state, body.RandaoReveal); err != nil {
		return err
	}
	if err := phase0.ProcessEth1Vote(ctx, spec, epc, state, body.Eth1Data); err != nil {
		return err
	}
	// Safety checks, in case the user of the function provided too many operations
	if err := body.CheckLimits(spec); err != nil {
		return err
	}
	// [Modified in Electra:EIP6110:EIP7002:EIP7549:EIP7251]
	if err := ProcessOperations(ctx, spec, epc, state, body); err != nil {
		return err
	}
	if err := altair.ProcessSyncAggregate(ctx, spec, epc, state, &body.SyncAggregate); err != nil {
		return err
	}
	return nil
}
