package electra

import (
	"bytes"

	"github.com/protolambda/ztyp/codec"
	. "github.com/protolambda/ztyp/view"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/deneb"
)

func UpgradeToElectra(spec *common.Spec, epc *common.EpochsContext, pre *deneb.BeaconStateView) (*BeaconStateView, error) {
	// Get epoch
	preSlot, err := pre.Slot()
	if err != nil {
		return nil, err
	}
	epoch := spec.SlotToEpoch(preSlot)

	// Create new Electra state
	post := NewBeaconStateView(spec)

	// Copy all fields from Deneb state
	// Versioning
	genesisTime, err := pre.GenesisTime()
	if err != nil {
		return nil, err
	}
	if err := post.SetGenesisTime(genesisTime); err != nil {
		return nil, err
	}

	genesisValidatorsRoot, err := pre.GenesisValidatorsRoot()
	if err != nil {
		return nil, err
	}
	if err := post.SetGenesisValidatorsRoot(genesisValidatorsRoot); err != nil {
		return nil, err
	}

	if err := post.SetSlot(preSlot); err != nil {
		return nil, err
	}

	// Update fork version
	newFork := common.Fork{
		PreviousVersion: spec.DENEB_FORK_VERSION,
		CurrentVersion:  spec.ELECTRA_FORK_VERSION,
		Epoch:           epoch,
	}
	if err := post.SetFork(newFork); err != nil {
		return nil, err
	}

	// Copy all other fields from pre state
	// This is simplified - in production, all fields need to be copied
	// For now, we'll copy the raw state and update the new fields
	rawPre, err := pre.Raw(spec)
	if err != nil {
		return nil, err
	}

	// Create raw post state from pre state fields
	rawPost := &BeaconState{
		// Copy all existing fields
		GenesisTime:                   rawPre.GenesisTime,
		GenesisValidatorsRoot:         rawPre.GenesisValidatorsRoot,
		Slot:                          rawPre.Slot,
		Fork:                          newFork,
		LatestBlockHeader:             rawPre.LatestBlockHeader,
		BlockRoots:                    rawPre.BlockRoots,
		StateRoots:                    rawPre.StateRoots,
		HistoricalRoots:               rawPre.HistoricalRoots,
		Eth1Data:                      rawPre.Eth1Data,
		Eth1DataVotes:                 rawPre.Eth1DataVotes,
		Eth1DepositIndex:              rawPre.Eth1DepositIndex,
		Validators:                    rawPre.Validators,
		Balances:                      rawPre.Balances,
		RandaoMixes:                   rawPre.RandaoMixes,
		Slashings:                     rawPre.Slashings,
		PreviousEpochParticipation:    rawPre.PreviousEpochParticipation,
		CurrentEpochParticipation:     rawPre.CurrentEpochParticipation,
		JustificationBits:             rawPre.JustificationBits,
		PreviousJustifiedCheckpoint:   rawPre.PreviousJustifiedCheckpoint,
		CurrentJustifiedCheckpoint:    rawPre.CurrentJustifiedCheckpoint,
		FinalizedCheckpoint:           rawPre.FinalizedCheckpoint,
		InactivityScores:              rawPre.InactivityScores,
		CurrentSyncCommittee:          rawPre.CurrentSyncCommittee,
		NextSyncCommittee:             rawPre.NextSyncCommittee,
		LatestExecutionPayloadHeader:  rawPre.LatestExecutionPayloadHeader,
		NextWithdrawalIndex:           rawPre.NextWithdrawalIndex,
		NextWithdrawalValidatorIndex:  rawPre.NextWithdrawalValidatorIndex,
		HistoricalSummaries:           rawPre.HistoricalSummaries,
		// Initialize new Electra fields
		DepositRequestsStartIndex:     Uint64View(UNSET_DEPOSIT_REQUESTS_START_INDEX),
		DepositBalanceToConsume:       0,
		ExitBalanceToConsume:          0,
		EarliestExitEpoch:             epoch,
		ConsolidationBalanceToConsume: 0,
		EarliestConsolidationEpoch:    epoch,
		PendingDeposits:               make(common.PendingDeposits, 0),
		PendingPartialWithdrawals:     make(common.PendingPartialWithdrawals, 0),
		PendingConsolidations:         make(common.PendingConsolidations, 0),
	}

	// Convert raw state to view
	var buf bytes.Buffer
	if err := rawPost.Serialize(spec, codec.NewEncodingWriter(&buf)); err != nil {
		return nil, err
	}

	return AsBeaconStateView(BeaconStateType(spec).Deserialize(codec.NewDecodingReader(bytes.NewReader(buf.Bytes()), uint64(len(buf.Bytes())))))
}
