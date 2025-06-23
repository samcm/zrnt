package electra

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/util/hashing"
)

// GetNextSyncCommitteeIndices returns the sync committee indices for Electra, with possible duplicates
func GetNextSyncCommitteeIndices(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState) ([]common.ValidatorIndex, error) {
	epoch := epc.CurrentEpoch.Epoch + 1

	vals, err := state.Validators()
	if err != nil {
		return nil, err
	}
	active := epc.NextEpoch.ActiveIndices
	activeCount := uint64(len(active))
	
	mixes, err := state.RandaoMixes()
	if err != nil {
		return nil, err
	}
	periodSeed, err := common.GetSeed(spec, mixes, epoch, common.DOMAIN_SYNC_COMMITTEE)
	if err != nil {
		return nil, err
	}

	var syncCommitteeIndices []common.ValidatorIndex
	MAX_RANDOM_VALUE := uint64(0xFFFF) // 2^16 - 1 [Modified in Electra]
	
	hFn := hashing.GetHashFn()
	var buf [32 + 8]byte
	copy(buf[0:32], periodSeed[:])
	var h [32]byte
	i := uint64(0)
	
	for uint64(len(syncCommitteeIndices)) < uint64(spec.SYNC_COMMITTEE_SIZE) {
		shuffledIndex := common.PermuteIndex(uint8(spec.SHUFFLE_ROUND_COUNT), 
			common.ValidatorIndex(i%activeCount), activeCount, periodSeed)
		candidateIndex := active[shuffledIndex]
		
		validator, err := vals.Validator(candidateIndex)
		if err != nil {
			return nil, err
		}
		
		effectiveBalance, err := validator.EffectiveBalance()
		if err != nil {
			return nil, err
		}
		
		// [Modified in Electra]
		// every 16 indices, create a new source for randomValue
		if i%16 == 0 {
			binary.LittleEndian.PutUint64(buf[32:32+8], i/16)
			h = hFn(buf[:])
		}
		
		// [Modified in Electra]
		// Use 16-bit random value instead of 8-bit
		offset := (i % 16) * 2
		randomValue := uint64(binary.LittleEndian.Uint16(h[offset : offset+2]))
		
		// [Modified in Electra:EIP7251]
		if uint64(effectiveBalance)*MAX_RANDOM_VALUE >= uint64(spec.MAX_EFFECTIVE_BALANCE_ELECTRA)*randomValue {
			syncCommitteeIndices = append(syncCommitteeIndices, candidateIndex)
		}
		i += 1
	}
	return syncCommitteeIndices, nil
}

// ComputeNextSyncCommittee computes the next sync committee for Electra
func ComputeNextSyncCommittee(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState) (*common.SyncCommittee, error) {
	indices, err := GetNextSyncCommitteeIndices(spec, epc, state)
	if err != nil {
		return nil, err
	}
	if uint64(len(indices)) != uint64(spec.SYNC_COMMITTEE_SIZE) {
		return nil, fmt.Errorf("expected %d sync committee indices, got %d", spec.SYNC_COMMITTEE_SIZE, len(indices))
	}
	return common.IndicesToSyncCommittee(indices, epc.ValidatorPubkeyCache)
}

// ProcessSyncCommitteeUpdates processes sync committee updates for Electra
func ProcessSyncCommitteeUpdates(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.SyncCommitteeBeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nextEpoch := epc.NextEpoch.Epoch
	if nextEpoch%spec.EPOCHS_PER_SYNC_COMMITTEE_PERIOD == 0 {
		next, err := ComputeNextSyncCommittee(spec, epc, state)
		if err != nil {
			return fmt.Errorf("failed to update sync committee: %v", err)
		}
		nextView, err := next.View(spec)
		if err != nil {
			return fmt.Errorf("failed to convert sync committee to state tree representation")
		}
		if err := state.RotateSyncCommittee(nextView); err != nil {
			return fmt.Errorf("failed to rotate sync committee: %v", err)
		}
	}
	return nil
}