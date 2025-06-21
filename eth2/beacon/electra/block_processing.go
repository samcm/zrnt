package electra

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/beacon/altair"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/zrnt/eth2/util/hashing"
	"github.com/protolambda/zrnt/eth2/util/math"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

// Modified process_operations for Electra
func ProcessOperations(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, body *BeaconBlockBody) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// [Modified in Electra:EIP6110]
	// Disable former deposit mechanism once all prior deposits are processed
	eth1Data, err := state.Eth1Data()
	if err != nil {
		return err
	}
	eth1DepositIndex, err := state.Eth1DepositIndex()
	if err != nil {
		return err
	}
	depositRequestsStartIndex, err := state.DepositRequestsStartIndex()
	if err != nil {
		return err
	}

	eth1DepositIndexLimit := eth1Data.DepositCount
	if eth1DepositIndexLimit > common.DepositIndex(depositRequestsStartIndex) {
		eth1DepositIndexLimit = common.DepositIndex(depositRequestsStartIndex)
	}

	if eth1DepositIndex < eth1DepositIndexLimit {
		expectedDeposits := eth1DepositIndexLimit - eth1DepositIndex
		if expectedDeposits > common.DepositIndex(spec.MAX_DEPOSITS) {
			expectedDeposits = common.DepositIndex(spec.MAX_DEPOSITS)
		}
		if uint64(len(body.Deposits)) != uint64(expectedDeposits) {
			return fmt.Errorf("expected %d deposits, got %d", expectedDeposits, len(body.Deposits))
		}
	} else {
		if len(body.Deposits) != 0 {
			return fmt.Errorf("expected 0 deposits when all eth1 deposits processed, got %d", len(body.Deposits))
		}
	}

	// Process slashings
	if err := phase0.ProcessProposerSlashings(ctx, spec, epc, state, body.ProposerSlashings); err != nil {
		return err
	}
	// Process attester slashings
	if err := ProcessAttesterSlashings(ctx, spec, epc, state, body.AttesterSlashings); err != nil {
		return err
	}

	// [Modified in Electra:EIP7549]
	if err := ProcessAttestations(ctx, spec, epc, state, body.Attestations); err != nil {
		return err
	}
	
	// Process deposits
	if err := ProcessDeposits(ctx, spec, epc, state, body.Deposits); err != nil {
		return err
	}
	
	// [Modified in Electra:EIP7251]
	if err := ProcessVoluntaryExits(ctx, spec, epc, state, body.VoluntaryExits); err != nil {
		return err
	}
	
	// Process BLS to execution changes using the same pattern as capella
	for i := range body.BLSToExecutionChanges {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessBLSToExecutionChange(ctx, spec, epc, state, &body.BLSToExecutionChanges[i]); err != nil {
			return err
		}
	}

	// [New in Electra] Process execution requests
	// Check if ExecutionRequests is not empty (all fields are slices)
	if len(body.ExecutionRequests.Deposits) > 0 || len(body.ExecutionRequests.Withdrawals) > 0 || len(body.ExecutionRequests.Consolidations) > 0 {
		// [New in Electra:EIP6110]
		for _, depositRequest := range body.ExecutionRequests.Deposits {
			if err := ProcessDepositRequest(ctx, spec, epc, state, &depositRequest); err != nil {
				return err
			}
		}
		
		// [New in Electra:EIP7002:EIP7251]
		for _, withdrawalRequest := range body.ExecutionRequests.Withdrawals {
			if err := ProcessWithdrawalRequest(ctx, spec, epc, state, &withdrawalRequest); err != nil {
				return err
			}
		}
		
		// [New in Electra:EIP7251]
		for _, consolidationRequest := range body.ExecutionRequests.Consolidations {
			if err := ProcessConsolidationRequest(ctx, spec, epc, state, &consolidationRequest); err != nil {
				return err
			}
		}
	}

	return nil
}

// Modified process_attestation for EIP7549
func ProcessAttestations(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []Attestation) error {
	for i := range ops {
		if err := ProcessAttestation(ctx, spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func ProcessAttestation(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, attestation *Attestation) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	data := &attestation.Data
	slot, err := state.Slot()
	if err != nil {
		return err
	}

	currentEpoch := spec.SlotToEpoch(slot)
	previousEpoch := common.Epoch(0)
	if currentEpoch > 0 {
		previousEpoch = currentEpoch - 1
	}

	// Verify epoch
	if data.Target.Epoch != previousEpoch && data.Target.Epoch != currentEpoch {
		return fmt.Errorf("attestation target epoch %d not current %d or previous epoch %d", data.Target.Epoch, currentEpoch, previousEpoch)
	}

	// Verify slot
	if data.Target.Epoch != spec.SlotToEpoch(data.Slot) {
		return fmt.Errorf("attestation slot %d not in target epoch %d", data.Slot, data.Target.Epoch)
	}

	// Verify inclusion delay
	if data.Slot+spec.MIN_ATTESTATION_INCLUSION_DELAY > slot {
		return fmt.Errorf("attestation slot %d too recent for inclusion in slot %d", data.Slot, slot)
	}

	// [Modified in Electra:EIP7549]
	if data.Index != 0 {
		return fmt.Errorf("attestation index must be 0 in Electra, got %d", data.Index)
	}

	// Get committee indices from committee bits
	committeeIndices := GetCommitteeIndices(attestation.CommitteeBits)
	committeeOffset := uint64(0)

	for _, committeeIndex := range committeeIndices {
		committeeCount, err := epc.GetCommitteeCountPerSlot(data.Target.Epoch)
		if err != nil {
			return err
		}
		if uint64(committeeIndex) >= committeeCount {
			return fmt.Errorf("committee index %d out of range %d", committeeIndex, committeeCount)
		}

		committee, err := epc.GetBeaconCommittee(data.Slot, committeeIndex)
		if err != nil {
			return err
		}

		// Check attesters
		attesters := 0
		for i := range committee {
			if attestation.AggregationBits.GetBit(uint64(committeeOffset + uint64(i))) {
				attesters++
			}
		}

		if attesters == 0 {
			return fmt.Errorf("attestation has no attesters for committee %d", committeeIndex)
		}

		committeeOffset += uint64(len(committee))
	}

	// Verify aggregation bits length
	if uint64(attestation.AggregationBits.BitLen()) != committeeOffset {
		return fmt.Errorf("attestation aggregation bits length %d does not match expected %d", attestation.AggregationBits.BitLen(), committeeOffset)
	}

	// Get attesting indices
	attestingIndices, err := GetAttestingIndices(spec, epc, state, attestation)
	if err != nil {
		return err
	}

	// Verify signature
	indexedAttestation := GetIndexedAttestation(spec, state, attestation, attestingIndices)
	if err := ValidateIndexedAttestation(spec, epc, state, indexedAttestation); err != nil {
		return fmt.Errorf("invalid attestation signature: %w", err)
	}

	// Update participation flags - cast to Electra state which has participation tracking
	electraState, ok := state.(*BeaconStateView)
	if !ok {
		return errors.New("state must be Electra state for participation tracking")
	}
	
	if data.Target.Epoch == currentEpoch {
		epochParticipation, err := electraState.CurrentEpochParticipation()
		if err != nil {
			return err
		}
		if err := UpdateParticipation(spec, epc, electraState, attestation, epochParticipation, attestingIndices, slot-data.Slot); err != nil {
			return err
		}
	} else {
		epochParticipation, err := electraState.PreviousEpochParticipation()
		if err != nil {
			return err
		}
		if err := UpdateParticipation(spec, epc, electraState, attestation, epochParticipation, attestingIndices, slot-data.Slot); err != nil {
			return err
		}
	}

	return nil
}

// ProcessAttesterSlashings processes attester slashings with Electra modifications
func ProcessAttesterSlashings(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []AttesterSlashing) error {
	for i := range ops {
		if err := ProcessAttesterSlashing(ctx, spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func ProcessAttesterSlashing(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, attesterSlashing *AttesterSlashing) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Check if the attestations constitute a slashable offense
	if !phase0.IsSlashableAttestationData(&attesterSlashing.Attestation1.Data, &attesterSlashing.Attestation2.Data) {
		return errors.New("attester slashing has no valid reasoning")
	}

	// Verify attestations
	if err := ValidateIndexedAttestation(spec, epc, state, &attesterSlashing.Attestation1); err != nil {
		return fmt.Errorf("invalid attestation 1 in attester slashing: %w", err)
	}

	if err := ValidateIndexedAttestation(spec, epc, state, &attesterSlashing.Attestation2); err != nil {
		return fmt.Errorf("invalid attestation 2 in attester slashing: %w", err)
	}

	// Get indices using modified get_attesting_indices
	att1Indices, err := GetIndexedAttestationAttestingIndices(spec, state, &attesterSlashing.Attestation1)
	if err != nil {
		return err
	}
	att2Indices, err := GetIndexedAttestationAttestingIndices(spec, state, &attesterSlashing.Attestation2)
	if err != nil {
		return err
	}

	// Find slashable validators
	slashableIndices := make([]common.ValidatorIndex, 0)
	for _, idx := range att1Indices {
		for _, idx2 := range att2Indices {
			if idx == idx2 {
				slashableIndices = append(slashableIndices, idx)
				break
			}
		}
	}

	if len(slashableIndices) == 0 {
		return errors.New("no slashable indices in attester slashing")
	}

	// Slash validators
	vals, err := state.Validators()
	if err != nil {
		return err
	}

	slot, err := state.Slot()
	if err != nil {
		return err
	}
	currentEpoch := spec.SlotToEpoch(slot)

	slashedAny := false
	for _, idx := range slashableIndices {
		val, err := vals.Validator(idx)
		if err != nil {
			return err
		}
		
		// Check if validator is slashable
		slashable, err := phase0.IsSlashable(val, currentEpoch)
		if err != nil {
			return err
		}
		
		if slashable {
			if err := SlashValidator(spec, epc, state, idx, nil); err != nil {
				return err
			}
			slashedAny = true
		}
	}

	if !slashedAny {
		return errors.New("attester slashing is not effective, no validators were slashed")
	}

	return nil
}

// GetIndexedAttestationAttestingIndices gets attesting indices from an indexed attestation
func GetIndexedAttestationAttestingIndices(spec *common.Spec, state common.BeaconState, indexedAttestation *IndexedAttestation) ([]common.ValidatorIndex, error) {
	// For indexed attestations, the indices are already provided
	indices := make([]common.ValidatorIndex, len(indexedAttestation.AttestingIndices))
	for i, idx := range indexedAttestation.AttestingIndices {
		indices[i] = common.ValidatorIndex(idx)
	}
	return indices, nil
}

// Modified get_attesting_indices for EIP7549
func GetAttestingIndices(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, attestation *Attestation) ([]common.ValidatorIndex, error) {
	output := make([]common.ValidatorIndex, 0)
	committeeIndices := GetCommitteeIndices(attestation.CommitteeBits)
	committeeOffset := uint64(0)

	slot, err := state.Slot()
	if err != nil {
		return nil, err
	}
	currentEpoch := spec.SlotToEpoch(slot)
	previousEpoch := common.Epoch(0)
	if currentEpoch > 0 {
		previousEpoch = currentEpoch - 1
	}

	targetEpoch := attestation.Data.Target.Epoch
	if targetEpoch != currentEpoch && targetEpoch != previousEpoch {
		return nil, fmt.Errorf("invalid target epoch %d", targetEpoch)
	}

	// Get committees from the appropriate epoch
	for _, committeeIndex := range committeeIndices {
		committee, err := epc.GetBeaconCommittee(attestation.Data.Slot, committeeIndex)
		if err != nil {
			return nil, err
		}

		for i, attesterIndex := range committee {
			if attestation.AggregationBits.GetBit(uint64(committeeOffset + uint64(i))) {
				output = append(output, attesterIndex)
			}
		}

		committeeOffset += uint64(len(committee))
	}

	return output, nil
}

// GetCommitteeIndices extracts committee indices from committee bits
func GetCommitteeIndices(committeeBits CommitteeBits) []common.CommitteeIndex {
	indices := make([]common.CommitteeIndex, 0)
	for i := uint64(0); i < uint64(committeeBits.BitLen()); i++ {
		if committeeBits.GetBit(i) {
			indices = append(indices, common.CommitteeIndex(i))
		}
	}
	return indices
}

// GetIndexedAttestation creates an indexed attestation from an attestation
func GetIndexedAttestation(spec *common.Spec, state common.BeaconState, attestation *Attestation, attestingIndices []common.ValidatorIndex) *IndexedAttestation {
	// Sort indices
	sortedIndices := make([]uint64, len(attestingIndices))
	for i, idx := range attestingIndices {
		sortedIndices[i] = uint64(idx)
	}
	
	// Sort using a simple bubble sort for now
	for i := 0; i < len(sortedIndices); i++ {
		for j := i + 1; j < len(sortedIndices); j++ {
			if sortedIndices[i] > sortedIndices[j] {
				sortedIndices[i], sortedIndices[j] = sortedIndices[j], sortedIndices[i]
			}
		}
	}

	// Convert to Electra IndexedAttestation which uses SlotCommitteeIndices
	slotCommitteeIndices := make(common.SlotCommitteeIndices, len(sortedIndices))
	for i, idx := range sortedIndices {
		slotCommitteeIndices[i] = common.ValidatorIndex(idx)
	}
	
	return &IndexedAttestation{
		AttestingIndices: slotCommitteeIndices,
		Data:             attestation.Data,
		Signature:        attestation.Signature,
	}
}

// UpdateParticipation updates participation flags for attesters
func UpdateParticipation(spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, attestation *Attestation, epochParticipation *altair.ParticipationRegistryView, attestingIndices []common.ValidatorIndex, inclusionDelay common.Slot) error {
	// Get participation flag indices using the same logic as altair
	flagIndices, err := GetAttestationParticipationFlagIndices(spec, state, &attestation.Data, inclusionDelay)
	if err != nil {
		return err
	}

	proposerRewardNumerator := common.Gwei(0)
	for _, index := range attestingIndices {
		// Get participation byte
		flagsView, err := epochParticipation.Get(uint64(index))
		if err != nil {
			return err
		}
		flags, err := AsUint8(flagsView, err)
		if err != nil {
			return err
		}

		// Calculate base reward using the same method as in Altair
		// Use effective balance from EpochsContext for consistency with other forks
		baseRewardPerIncrement := spec.EFFECTIVE_BALANCE_INCREMENT * common.Gwei(spec.BASE_REWARD_FACTOR) / epc.TotalActiveStakeSqRoot
		increments := epc.EffectiveBalances[index] / spec.EFFECTIVE_BALANCE_INCREMENT
		baseReward := increments * baseRewardPerIncrement
		
		// Debug: Print calculation details
		// fmt.Printf("DEBUG: Validator %d - EffBal: %d, Increments: %d, BaseRewardPerInc: %d, BaseReward: %d\n", 
		//     index, epc.EffectiveBalances[index], increments, baseRewardPerIncrement, baseReward)

		// Update participation flags
		weights := []common.Gwei{altair.TIMELY_SOURCE_WEIGHT, altair.TIMELY_TARGET_WEIGHT, altair.TIMELY_HEAD_WEIGHT}
		for flagIndex := uint8(0); flagIndex < 3; flagIndex++ {
			if HasFlag(uint8(flagIndices), flagIndex) && !HasFlag(uint8(flags), flagIndex) {
				flags = Uint8View(AddFlag(uint8(flags), flagIndex))
				proposerRewardNumerator += baseReward * weights[flagIndex]
			}
		}

		if err := epochParticipation.Set(uint64(index), Uint8View(flags)); err != nil {
			return err
		}
	}

	// Reward proposer
	if proposerRewardNumerator > 0 {
		proposerRewardDenominator := ((altair.WEIGHT_DENOMINATOR - altair.PROPOSER_WEIGHT) * altair.WEIGHT_DENOMINATOR) / altair.PROPOSER_WEIGHT
		proposerReward := proposerRewardNumerator / proposerRewardDenominator

		slot, err := state.Slot()
		if err != nil {
			return err
		}
		proposerIndex, err := epc.GetBeaconProposer(slot)
		if err != nil {
			return err
		}

		bals, err := state.Balances()
		if err != nil {
			return err
		}
		if err := common.IncreaseBalance(bals, proposerIndex, proposerReward); err != nil {
			return err
		}
	}

	return nil
}

// ProcessDeposits processes all deposits with Electra modifications
func ProcessDeposits(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []common.Deposit) error {
	for i := range ops {
		if err := ProcessDeposit(ctx, spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

// Modified process_deposit for Electra
func ProcessDeposit(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, deposit *common.Deposit) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Verify the Merkle branch
	eth1Data, err := state.Eth1Data()
	if err != nil {
		return err
	}
	eth1DepositIndex, err := state.Eth1DepositIndex()
	if err != nil {
		return err
	}

	depositRoot := deposit.Data.HashTreeRoot(tree.GetHashFn())
	// Verify the Merkle branch
	branch := make([]common.Root, len(deposit.Proof))
	for i, h := range deposit.Proof {
		branch[i] = h
	}
	// Verify merkle proof - use depth 33 (32 + 1)
	depth := uint64(33)
	index := uint64(eth1DepositIndex)
	root := eth1Data.DepositRoot
	
	// Verify the branch
	computed := depositRoot
	for i := uint64(0); i < depth; i++ {
		if i < uint64(len(branch)) {
			if (index >> i) & 1 == 1 {
				computed = hashing.Hash(append(branch[i][:], computed[:]...))
			} else {
				computed = hashing.Hash(append(computed[:], branch[i][:]...))
			}
		}
	}
	
	if computed != root {
		return errors.New("invalid deposit merkle proof")
	}

	// Update deposit index
	if err := state.IncrementDepositIndex(); err != nil {
		return err
	}

	// [Modified in Electra:EIP7251] Apply deposit
	return ApplyDeposit(ctx, spec, epc, state.(*BeaconStateView), deposit.Data.Pubkey, deposit.Data.WithdrawalCredentials, deposit.Data.Amount, deposit.Data.Signature)
}

// ApplyDeposit applies a deposit with Electra modifications
func ApplyDeposit(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, pubkey common.BLSPubkey, withdrawalCredentials common.Root, amount common.Gwei, signature common.BLSSignature) error {
	validators, err := state.Validators()
	if err != nil {
		return err
	}

	// Check if validator already exists
	validatorIndex := common.ValidatorIndexMarker
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return err
	}

	for i := common.ValidatorIndex(0); uint64(i) < validatorCount; i++ {
		val, err := validators.Validator(i)
		if err != nil {
			return err
		}
		valPubkey, err := val.Pubkey()
		if err != nil {
			return err
		}
		if valPubkey == pubkey {
			validatorIndex = i
			break
		}
	}

	if validatorIndex == common.ValidatorIndexMarker {
		// Verify the deposit signature
		if isValid, err := IsValidDepositSignature(spec, pubkey, withdrawalCredentials, amount, signature); err != nil {
			return err
		} else if isValid {
			// [Modified in Electra:EIP7251] Add validator with 0 balance
			if err := AddValidatorToRegistry(ctx, spec, epc, state, pubkey, withdrawalCredentials, 0); err != nil {
				return err
			}
		} else {
			return nil // Invalid signature, ignore deposit
		}
	}

	// [Modified in Electra:EIP7251] Create pending deposit

	pendingDeposit := common.PendingDeposit{
		Pubkey:                pubkey,
		WithdrawalCredentials: withdrawalCredentials,
		Amount:                amount,
		Signature:             signature,
		Slot:                  common.GENESIS_SLOT, // Use GENESIS_SLOT to distinguish from deposit request
	}

	return state.AppendPendingDeposit(pendingDeposit)
}

// ProcessVoluntaryExits processes voluntary exits with Electra modifications
func ProcessVoluntaryExits(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops []phase0.SignedVoluntaryExit) error {
	for i := range ops {
		if err := ProcessVoluntaryExit(ctx, spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

// Modified process_voluntary_exit for Electra
func ProcessVoluntaryExit(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, signedExit *phase0.SignedVoluntaryExit) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	exit := &signedExit.Message
	vals, err := state.Validators()
	if err != nil {
		return err
	}

	validator, err := vals.Validator(exit.ValidatorIndex)
	if err != nil {
		return err
	}

	slot, err := state.Slot()
	if err != nil {
		return err
	}
	currentEpoch := spec.SlotToEpoch(slot)

	// Verify the validator is active
	isActive, err := phase0.IsActive(validator, currentEpoch)
	if err != nil {
		return err
	}
	if !isActive {
		return fmt.Errorf("validator %d is not active", exit.ValidatorIndex)
	}

	// Verify exit has not been initiated
	exitEpoch, err := validator.ExitEpoch()
	if err != nil {
		return err
	}
	if exitEpoch != common.FAR_FUTURE_EPOCH {
		return fmt.Errorf("validator %d already has exit initiated", exit.ValidatorIndex)
	}

	// Exits must specify an epoch when they become valid
	if currentEpoch < exit.Epoch {
		return fmt.Errorf("exit epoch %d is in the future (current: %d)", exit.Epoch, currentEpoch)
	}

	// Verify the validator has been active long enough
	activationEpoch, err := validator.ActivationEpoch()
	if err != nil {
		return err
	}
	if currentEpoch < activationEpoch+spec.SHARD_COMMITTEE_PERIOD {
		return fmt.Errorf("validator %d has not been active long enough", exit.ValidatorIndex)
	}

	// [New in Electra:EIP7251] Check pending withdrawals
	pendingBalance, err := get_pending_balance_to_withdraw(state.(*BeaconStateView), exit.ValidatorIndex)
	if err != nil {
		return err
	}
	if pendingBalance != 0 {
		return fmt.Errorf("validator %d has pending withdrawals", exit.ValidatorIndex)
	}

	// Verify signature
	genesisValidatorsRoot, err := state.GenesisValidatorsRoot()
	if err != nil {
		return err
	}

	domain := common.ComputeDomain(common.DOMAIN_VOLUNTARY_EXIT, spec.CAPELLA_FORK_VERSION, genesisValidatorsRoot)

	signingRoot := common.ComputeSigningRoot(
		exit.HashTreeRoot(tree.GetHashFn()),
		domain,
	)

	pubkey, err := validator.Pubkey()
	if err != nil {
		return err
	}

	blsPub, err := pubkey.Pubkey()
	if err != nil {
		return err
	}

	blsSig, err := signedExit.Signature.Signature()
	if err != nil {
		return err
	}

	if !blsu.Verify(blsPub, signingRoot[:], blsSig) {
		return errors.New("invalid voluntary exit signature")
	}

	// Initiate exit
	return InitiateValidatorExit(ctx, spec, epc, state.(*BeaconStateView), exit.ValidatorIndex)
}

// SlashValidator slashes a validator with Electra modifications
func SlashValidator(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, slashedIndex common.ValidatorIndex, whistleblowerIndex *common.ValidatorIndex) error {
	slot, err := state.Slot()
	if err != nil {
		return err
	}
	epoch := spec.SlotToEpoch(slot)

	if err := phase0.InitiateValidatorExit(spec, epc, state, slashedIndex); err != nil {
		return err
	}

	vals, err := state.Validators()
	if err != nil {
		return err
	}

	validator, err := vals.Validator(slashedIndex)
	if err != nil {
		return err
	}

	if err := validator.MakeSlashed(); err != nil {
		return err
	}

	withdrawableEpoch, err := validator.WithdrawableEpoch()
	if err != nil {
		return err
	}

	newWithdrawableEpoch := epoch + spec.EPOCHS_PER_SLASHINGS_VECTOR
	if newWithdrawableEpoch < epoch { // Check for overflow
		return fmt.Errorf("withdrawable epoch overflow in slashing: %d + %d = %d", epoch, spec.EPOCHS_PER_SLASHINGS_VECTOR, newWithdrawableEpoch)
	}
	if newWithdrawableEpoch > withdrawableEpoch {
		if err := validator.SetWithdrawableEpoch(newWithdrawableEpoch); err != nil {
			return err
		}
	}

	effectiveBalance, err := validator.EffectiveBalance()
	if err != nil {
		return err
	}

	// Update slashings
	slashings, err := state.Slashings()
	if err != nil {
		return err
	}

	slashingIndex := uint64(epoch) % uint64(spec.EPOCHS_PER_SLASHINGS_VECTOR)
	// Get current slashing value and update it
	slashingsMux, ok := slashings.(*phase0.SlashingsView)
	if !ok {
		return fmt.Errorf("slashings is not a SlashingsView")
	}
	currentSlashingView, err := slashingsMux.Get(slashingIndex)
	if err != nil {
		return err
	}
	currentSlashing := common.Gwei(0)
	if gv, ok := currentSlashingView.(Uint64View); ok {
		currentSlashing = common.Gwei(gv)
	} else {
		return fmt.Errorf("invalid slashing view type")
	} 
	if err != nil {
		return err
	}

	// Update slashing
	newSlashing := currentSlashing + effectiveBalance
	if err := slashingsMux.Set(slashingIndex, Uint64View(newSlashing)); err != nil {
		return err
	}

	// [Modified in Electra:EIP7251] Apply slashing penalty
	slashingPenalty := effectiveBalance / common.Gwei(spec.MIN_SLASHING_PENALTY_QUOTIENT_ELECTRA)
	bals, err := state.Balances()
	if err != nil {
		return err
	}

	if err := common.DecreaseBalance(bals, slashedIndex, slashingPenalty); err != nil {
		return err
	}

	// Apply proposer and whistleblower rewards
	// Get proposer index
	proposerIndex, err := epc.GetBeaconProposer(slot)
	if err != nil {
		return err
	}

	if whistleblowerIndex == nil {
		whistleblowerIndex = &proposerIndex
	}

	// [Modified in Electra:EIP7251]
	whistleblowerReward := effectiveBalance / common.Gwei(spec.WHISTLEBLOWER_REWARD_QUOTIENT_ELECTRA)
	proposerReward := whistleblowerReward * altair.PROPOSER_WEIGHT / altair.WEIGHT_DENOMINATOR

	if err := common.IncreaseBalance(bals, proposerIndex, proposerReward); err != nil {
		return err
	}
	if err := common.IncreaseBalance(bals, *whistleblowerIndex, whistleblowerReward-proposerReward); err != nil {
		return err
	}

	return nil
}

// Helper functions for participation flags
func HasFlag(flags uint8, flagIndex uint8) bool {
	return (flags & (1 << flagIndex)) != 0
}

func AddFlag(flags uint8, flagIndex uint8) uint8 {
	return flags | (1 << flagIndex)
}

// ProcessBLSToExecutionChange processes a single BLS to execution change
func ProcessBLSToExecutionChange(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, op *common.SignedBLSToExecutionChange) error {
	validators, err := state.Validators()
	if err != nil {
		return err
	}
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return err
	}

	addressChange := op.BLSToExecutionChange
	if uint64(addressChange.ValidatorIndex) >= validatorCount {
		return fmt.Errorf("invalid validator index for bls to execution change")
	}

	validator, err := validators.Validator(addressChange.ValidatorIndex)
	if err != nil {
		return err
	}

	validatorWithdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		return err
	}
	if !bytes.Equal(validatorWithdrawalCredentials[:1], []byte{common.BLS_WITHDRAWAL_PREFIX}) {
		return fmt.Errorf("invalid bls to execution change, validator not bls: %v", validatorWithdrawalCredentials)
	}
	sigHash := hashing.Hash(addressChange.FromBLSPubKey[:])
	if !bytes.Equal(validatorWithdrawalCredentials[1:], sigHash[1:]) {
		return fmt.Errorf("invalid bls to execution change, incorrect public key: got %v, want %v", addressChange.FromBLSPubKey, validatorWithdrawalCredentials)
	}
	genesisValidatorsRoot, err := state.GenesisValidatorsRoot()
	if err != nil {
		return err
	}
	domain := common.ComputeDomain(common.DOMAIN_BLS_TO_EXECUTION_CHANGE, spec.GENESIS_FORK_VERSION, genesisValidatorsRoot)

	sigRoot := common.ComputeSigningRoot(addressChange.HashTreeRoot(tree.GetHashFn()), domain)
	pubKey, err := addressChange.FromBLSPubKey.Pubkey()
	if err != nil {
		return err
	}

	signature, err := op.Signature.Signature()
	if err != nil {
		return err
	}

	if !blsu.Verify(pubKey, sigRoot[:], signature) {
		return fmt.Errorf("invalid bls to execution change signature")
	}
	var newWithdrawalCredentials tree.Root
	copy(newWithdrawalCredentials[0:1], []byte{common.ETH1_ADDRESS_WITHDRAWAL_PREFIX})
	copy(newWithdrawalCredentials[12:], addressChange.ToExecutionAddress[:])
	return validator.SetWithdrawalCredentials(newWithdrawalCredentials)
}

// ValidateIndexedAttestation validates an indexed attestation
func ValidateIndexedAttestation(spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, indexedAttestation *IndexedAttestation) error {
	// Validate the indices
	indices := common.ValidatorSet(indexedAttestation.AttestingIndices)
	
	// Verify max number of indices
	if count := uint64(len(indices)); count > uint64(spec.MAX_VALIDATORS_PER_COMMITTEE) * uint64(spec.MAX_COMMITTEES_PER_SLOT) {
		return fmt.Errorf("invalid indices count in indexed attestation: %d", count)
	}
	
	// empty attestation
	if len(indices) <= 0 {
		return errors.New("empty attestation signatures are not allowed")
	}
	
	// The indices must be sorted
	for i := 1; i < len(indices); i++ {
		if indices[i-1] >= indices[i] {
			return errors.New("attestation indices are not sorted")
		}
	}
	
	// Check the last item of the sorted list to be a valid index
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	valid, err := vals.IsValidIndex(indices[len(indices)-1])
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("attestation indices contain out of range index")
	}
	
	// Verify the signature
	dom, err := common.GetDomain(state, common.DOMAIN_BEACON_ATTESTER, indexedAttestation.Data.Target.Epoch)
	if err != nil {
		return err
	}
	
	pubkeys := make([]*blsu.Pubkey, 0, len(indexedAttestation.AttestingIndices))
	for _, i := range indexedAttestation.AttestingIndices {
		pub, ok := epc.ValidatorPubkeyCache.Pubkey(i)
		if !ok {
			return fmt.Errorf("could not find pubkey for index %d", i)
		}
		blsPub, err := pub.Pubkey()
		if err != nil {
			return fmt.Errorf("failed to deserialize pubkey in cache: %v", err)
		}
		pubkeys = append(pubkeys, blsPub)
	}
	
	signingRoot := common.ComputeSigningRoot(indexedAttestation.Data.HashTreeRoot(tree.GetHashFn()), dom)
	sig, err := indexedAttestation.Signature.Signature()
	if err != nil {
		return fmt.Errorf("failed to deserialize and sub-group check indexed attestation signature: %v", err)
	}
	if !blsu.Eth2FastAggregateVerify(pubkeys, signingRoot[:], sig) {
		return errors.New("could not verify BLS signature for indexed attestation")
	}
	return nil
}

// GetAttestationParticipationFlagIndices gets the participation flag indices
func GetAttestationParticipationFlagIndices(spec *common.Spec, state common.BeaconState, data *phase0.AttestationData, inclusionDelay common.Slot) (altair.ParticipationFlags, error) {
	currentSlot, err := state.Slot()
	if err != nil {
		return 0, err
	}
	
	currentEpoch := spec.SlotToEpoch(currentSlot)
	
	var justifiedCheckpoint common.Checkpoint
	if data.Target.Epoch == currentEpoch {
		justifiedCheckpoint, err = state.CurrentJustifiedCheckpoint()
		if err != nil {
			return 0, err
		}
	} else {
		justifiedCheckpoint, err = state.PreviousJustifiedCheckpoint()
		if err != nil {
			return 0, err
		}
	}
	
	expectedHead, err := common.GetBlockRootAtSlot(spec, state, data.Slot)
	if err != nil {
		return 0, err
	}
	expectedTarget, err := common.GetBlockRoot(spec, state, data.Target.Epoch)
	if err != nil {
		return 0, err
	}
	
	isMatchingSource := data.Source == justifiedCheckpoint
	isMatchingTarget := isMatchingSource && expectedTarget == data.Target.Root
	isMatchingHead := isMatchingTarget && expectedHead == data.BeaconBlockRoot
	
	if !isMatchingSource {
		return 0, fmt.Errorf("source %s must match justified %s", data.Source, justifiedCheckpoint)
	}
	
	var out altair.ParticipationFlags
	if isMatchingSource && inclusionDelay <= common.Slot(math.IntegerSquareroot(uint64(spec.SLOTS_PER_EPOCH))) {
		out |= altair.TIMELY_SOURCE_FLAG
	}
	if isMatchingTarget && inclusionDelay <= spec.SLOTS_PER_EPOCH {
		out |= altair.TIMELY_TARGET_FLAG
	}
	if isMatchingHead && inclusionDelay == spec.MIN_ATTESTATION_INCLUSION_DELAY {
		out |= altair.TIMELY_HEAD_FLAG
	}
	return out, nil
}





