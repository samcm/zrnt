package electra

import (
	"context"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
)

// computeExitEpochAndUpdateChurn computes the exit epoch and updates churn
// [Modified in Electra:EIP7251]
func computeExitEpochAndUpdateChurn(state *BeaconStateView, exit_balance common.Gwei, spec *common.Spec) (common.Epoch, error) {
	slot, err := state.Slot()
	if err != nil {
		return 0, err
	}
	current_epoch := spec.SlotToEpoch(slot)

	// Get earliest exit epoch
	earliest_exit_epoch, err := state.EarliestExitEpoch()
	if err != nil {
		return 0, err
	}

	// Compute activation exit epoch
	activation_exit_epoch := spec.ComputeActivationExitEpoch(current_epoch)
	if activation_exit_epoch > earliest_exit_epoch {
		earliest_exit_epoch = activation_exit_epoch
	}

	// Get per epoch churn
	per_epoch_churn, err := GetActivationExitChurnLimit(spec, state)
	if err != nil {
		return 0, err
	}

	// Get current state values
	state_earliest_exit_epoch, err := state.EarliestExitEpoch()
	if err != nil {
		return 0, err
	}

	// New epoch for exits
	var exit_balance_to_consume common.Gwei
	if state_earliest_exit_epoch < earliest_exit_epoch {
		exit_balance_to_consume = per_epoch_churn
	} else {
		// Use existing balance to consume
		exit_balance_to_consume, err = state.ExitBalanceToConsume()
		if err != nil {
			return 0, err
		}
	}

	// Exit doesn't fit in the current earliest epoch
	if exit_balance > exit_balance_to_consume {
		balance_to_process := exit_balance - exit_balance_to_consume
		additional_epochs := (balance_to_process - 1) / per_epoch_churn + 1
		earliest_exit_epoch += common.Epoch(additional_epochs)
		exit_balance_to_consume += common.Gwei(additional_epochs) * per_epoch_churn
	}

	// Consume the balance and update state variables
	if err := state.SetExitBalanceToConsume(exit_balance_to_consume - exit_balance); err != nil {
		return 0, err
	}
	if err := state.SetEarliestExitEpoch(earliest_exit_epoch); err != nil {
		return 0, err
	}

	return earliest_exit_epoch, nil
}

// computeConsolidationEpochAndUpdateChurn computes the consolidation epoch and updates churn
// [New in Electra:EIP7251]
func computeConsolidationEpochAndUpdateChurn(state *BeaconStateView, consolidation_balance common.Gwei, spec *common.Spec) (common.Epoch, error) {
	slot, err := state.Slot()
	if err != nil {
		return 0, err
	}
	current_epoch := spec.SlotToEpoch(slot)

	// Get earliest consolidation epoch
	earliest_consolidation_epoch, err := state.EarliestConsolidationEpoch()
	if err != nil {
		return 0, err
	}

	// Compute activation exit epoch
	activation_exit_epoch := spec.ComputeActivationExitEpoch(current_epoch)
	if activation_exit_epoch > earliest_consolidation_epoch {
		earliest_consolidation_epoch = activation_exit_epoch
	}

	// Get per epoch consolidation churn
	per_epoch_consolidation_churn, err := GetConsolidationChurnLimit(spec, state)
	if err != nil {
		return 0, err
	}

	// New epoch for consolidations
	consolidation_balance_to_consume, err := state.ConsolidationBalanceToConsume()
	if err != nil {
		return 0, err
	}

	state_earliest_consolidation_epoch, err := state.EarliestConsolidationEpoch()
	if err != nil {
		return 0, err
	}

	if state_earliest_consolidation_epoch < earliest_consolidation_epoch {
		consolidation_balance_to_consume = per_epoch_consolidation_churn
	}

	// Consolidation doesn't fit in the current earliest epoch
	if consolidation_balance > consolidation_balance_to_consume {
		balance_to_process := consolidation_balance - consolidation_balance_to_consume
		additional_epochs := (balance_to_process - 1) / per_epoch_consolidation_churn + 1
		earliest_consolidation_epoch += common.Epoch(additional_epochs)
		consolidation_balance_to_consume += common.Gwei(additional_epochs) * per_epoch_consolidation_churn
	}

	// Consume the balance and update state variables
	if err := state.SetConsolidationBalanceToConsume(consolidation_balance_to_consume - consolidation_balance); err != nil {
		return 0, err
	}
	if err := state.SetEarliestConsolidationEpoch(earliest_consolidation_epoch); err != nil {
		return 0, err
	}

	return earliest_consolidation_epoch, nil
}

// initiateValidatorExit initiates the validator exit process
// [Modified in Electra:EIP7251]
func initiateValidatorExit(state *BeaconStateView, index common.ValidatorIndex, spec *common.Spec, epc *common.EpochsContext) error {
	// Return if validator already initiated exit
	validators, err := state.Validators()
	if err != nil {
		return err
	}

	validator, err := validators.Validator(index)
	if err != nil {
		return err
	}

	exit_epoch, err := validator.ExitEpoch()
	if err != nil {
		return err
	}

	if exit_epoch != common.FAR_FUTURE_EPOCH {
		return nil
	}

	// Compute exit queue epoch [Modified in Electra:EIP7251]
	slot, err := state.Slot()
	if err != nil {
		return err
	}
	current_epoch := spec.SlotToEpoch(slot)

	// Get validator effective balance
	effective_balance, err := validator.EffectiveBalance()
	if err != nil {
		return err
	}

	// Check if validator is active
	isActive, err := phase0.IsActive(validator, current_epoch)
	if err != nil {
		return err
	}

	// If validator is not active, use 0 balance
	var exit_balance common.Gwei
	if isActive {
		exit_balance = effective_balance
	} else {
		exit_balance = 0
	}

	// Compute exit epoch
	exit_queue_epoch, err := computeExitEpochAndUpdateChurn(state, exit_balance, spec)
	if err != nil {
		return err
	}

	// Set validator exit epoch and withdrawable epoch
	if err := validator.SetExitEpoch(exit_queue_epoch); err != nil {
		return err
	}

	withdrawable_epoch := exit_queue_epoch + spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY
	if withdrawable_epoch < exit_queue_epoch { // Check for overflow
		return fmt.Errorf("withdrawable epoch overflow: %d + %d = %d", exit_queue_epoch, spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY, withdrawable_epoch)
	}
	if err := validator.SetWithdrawableEpoch(withdrawable_epoch); err != nil {
		return err
	}

	return nil
}

// isEligibleForActivationQueue checks if a validator is eligible for the activation queue
// [Modified in Electra:EIP7251]
func isEligibleForActivationQueue(validator common.Validator, spec *common.Spec) (bool, error) {
	activation_eligibility_epoch, err := validator.ActivationEligibilityEpoch()
	if err != nil {
		return false, err
	}

	if activation_eligibility_epoch != common.FAR_FUTURE_EPOCH {
		return false, nil
	}

	effective_balance, err := validator.EffectiveBalance()
	if err != nil {
		return false, err
	}

	// [Modified in Electra:EIP7251]
	// Check effective balance threshold
	return effective_balance >= spec.MIN_ACTIVATION_BALANCE, nil
}

// isEligibleForActivation checks if a validator is eligible for activation
// [Modified in Electra:EIP7251]
func isEligibleForActivation(state common.BeaconState, validator common.Validator, spec *common.Spec) (bool, error) {
	activation_eligibility_epoch, err := validator.ActivationEligibilityEpoch()
	if err != nil {
		return false, err
	}

	activation_epoch, err := validator.ActivationEpoch()
	if err != nil {
		return false, err
	}

	if activation_epoch != common.FAR_FUTURE_EPOCH {
		return false, nil
	}

	// [Modified in Electra:EIP7251]
	// Check if validator is eligible
	finalized_checkpoint, err := state.FinalizedCheckpoint()
	if err != nil {
		return false, err
	}

	finalized_epoch := finalized_checkpoint.Epoch

	return activation_eligibility_epoch <= finalized_epoch, nil
}

// Exported wrappers for consensus-specs compatibility

// ComputeExitEpochAndUpdateChurn computes the exit epoch and updates churn
func ComputeExitEpochAndUpdateChurn(ctx context.Context, spec *common.Spec, state *BeaconStateView, exitBalance common.Gwei) (common.Epoch, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return computeExitEpochAndUpdateChurn(state, exitBalance, spec)
}

// ComputeConsolidationEpochAndUpdateChurn computes the consolidation epoch and updates churn
func ComputeConsolidationEpochAndUpdateChurn(ctx context.Context, spec *common.Spec, state *BeaconStateView, consolidationBalance common.Gwei) (common.Epoch, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return computeConsolidationEpochAndUpdateChurn(state, consolidationBalance, spec)
}

// InitiateValidatorExit initiates the validator exit process
func InitiateValidatorExit(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, index common.ValidatorIndex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return initiateValidatorExit(state, index, spec, epc)
}


// IsEligibleForActivation checks if a validator is eligible for activation
func IsEligibleForActivation(state common.BeaconState, validator common.Validator, spec *common.Spec) (bool, error) {
	return isEligibleForActivation(state, validator, spec)
}