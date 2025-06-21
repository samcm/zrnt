package electra

import (
	"bytes"
	"context"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/deneb"
)

// GetExpectedWithdrawals returns expected withdrawals and processed partial withdrawals count
func GetExpectedWithdrawals(state *BeaconStateView, spec *common.Spec) ([]common.Withdrawal, uint64, error) {
	slot, err := state.Slot()
	if err != nil {
		return nil, 0, err
	}
	epoch := spec.SlotToEpoch(slot)
	
	withdrawalIndex, err := state.NextWithdrawalIndex()
	if err != nil {
		return nil, 0, err
	}
	
	validatorIndex, err := state.NextWithdrawalValidatorIndex()
	if err != nil {
		return nil, 0, err
	}
	
	validators, err := state.Validators()
	if err != nil {
		return nil, 0, err
	}
	
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return nil, 0, err
	}
	
	balances, err := state.Balances()
	if err != nil {
		return nil, 0, err
	}
	
	withdrawals := make(common.Withdrawals, 0)
	processedPartialWithdrawalsCount := uint64(0)
	
	// [New in Electra:EIP7251] Process pending partial withdrawals
	// For simplicity, we'll skip processing pending partial withdrawals for now
	// In a full implementation, this would iterate through pending withdrawals
	// and process eligible ones
	
	// Sweep for remaining withdrawals
	bound := validatorCount
	if bound > uint64(spec.MAX_VALIDATORS_PER_WITHDRAWALS_SWEEP) {
		bound = uint64(spec.MAX_VALIDATORS_PER_WITHDRAWALS_SWEEP)
	}
	
	for i := uint64(0); i < bound; i++ {
		validator, err := validators.Validator(validatorIndex)
		if err != nil {
			return nil, 0, err
		}
		
		balance, err := balances.GetBalance(validatorIndex)
		if err != nil {
			return nil, 0, err
		}
		
		if IsFullyWithdrawableValidator(validator, balance, epoch) {
			withdrawalCredentials, err := validator.WithdrawalCredentials()
			if err != nil {
				return nil, 0, err
			}
			
			withdrawals = append(withdrawals, common.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        common.Eth1Address(withdrawalCredentials[12:]),
				Amount:         balance,
			})
			withdrawalIndex++
		} else if IsPartiallyWithdrawableValidator(spec, validator, balance) {
			maxEffectiveBalance := get_max_effective_balance(spec, validator)
			withdrawalCredentials, err := validator.WithdrawalCredentials()
			if err != nil {
				return nil, 0, err
			}
			
			withdrawals = append(withdrawals, common.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        common.Eth1Address(withdrawalCredentials[12:]),
				Amount:         balance - maxEffectiveBalance,
			})
			withdrawalIndex++
		}
		
		if len(withdrawals) == int(spec.MAX_WITHDRAWALS_PER_PAYLOAD) {
			break
		}
		
		validatorIndex = common.ValidatorIndex((uint64(validatorIndex) + 1) % validatorCount)
	}
	
	return withdrawals, processedPartialWithdrawalsCount, nil
}

// ProcessWithdrawals processes withdrawals with Electra modifications
func ProcessWithdrawals(ctx context.Context, spec *common.Spec, state *BeaconStateView, executionPayload *deneb.ExecutionPayload) error {
	expectedWithdrawals, processedPartialWithdrawalsCount, err := GetExpectedWithdrawals(state, spec)
	if err != nil {
		return err
	}
	
	withdrawals := executionPayload.Withdrawals
	if len(expectedWithdrawals) != len(withdrawals) {
		return fmt.Errorf("unexpected number of withdrawals: want=%d, got=%d", len(expectedWithdrawals), len(withdrawals))
	}
	
	bals, err := state.Balances()
	if err != nil {
		return err
	}
	
	for w := 0; w < len(expectedWithdrawals); w++ {
		withdrawal := withdrawals[w]
		expectedWithdrawal := expectedWithdrawals[w]
		if withdrawal.Index != expectedWithdrawal.Index ||
			withdrawal.ValidatorIndex != expectedWithdrawal.ValidatorIndex ||
			!bytes.Equal(withdrawal.Address[:], expectedWithdrawal.Address[:]) ||
			withdrawal.Amount != expectedWithdrawal.Amount {
			return fmt.Errorf("unexpected withdrawal: want=%v, got=%v", expectedWithdrawal, withdrawal)
		}
		if err := common.DecreaseBalance(bals, expectedWithdrawal.ValidatorIndex, expectedWithdrawal.Amount); err != nil {
			return fmt.Errorf("failed to decrease balance: %w", err)
		}
	}
	
	// [New in Electra:EIP7251] Update pending partial withdrawals
	// For simplicity, we'll skip updating partial withdrawals for now
	_ = processedPartialWithdrawalsCount
	
	// Update withdrawal indices
	if len(expectedWithdrawals) > 0 {
		latestWithdrawal := expectedWithdrawals[len(expectedWithdrawals)-1]
		if err := state.SetNextWithdrawalIndex(latestWithdrawal.Index + 1); err != nil {
			return fmt.Errorf("failed to set withdrawal index: %w", err)
		}
	}
	
	validators, err := state.Validators()
	if err != nil {
		return err
	}
	
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return err
	}
	
	if len(expectedWithdrawals) == int(spec.MAX_WITHDRAWALS_PER_PAYLOAD) {
		latestWithdrawal := expectedWithdrawals[len(expectedWithdrawals)-1]
		nextValidatorIndex := common.ValidatorIndex((uint64(latestWithdrawal.ValidatorIndex) + 1) % validatorCount)
		if err := state.SetNextWithdrawalValidatorIndex(nextValidatorIndex); err != nil {
			return err
		}
	} else {
		nextValidatorIndex, err := state.NextWithdrawalValidatorIndex()
		if err != nil {
			return err
		}
		nextValidatorIndex = common.ValidatorIndex((uint64(nextValidatorIndex) + uint64(spec.MAX_VALIDATORS_PER_WITHDRAWALS_SWEEP)) % validatorCount)
		if err := state.SetNextWithdrawalValidatorIndex(nextValidatorIndex); err != nil {
			return err
		}
	}
	
	return nil
}

// HasEth1WithdrawalCredential checks if validator has ETH1 withdrawal credentials
func HasEth1WithdrawalCredential(validator common.Validator) bool {
	withdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		return false
	}
	return withdrawalCredentials[0] == common.ETH1_ADDRESS_WITHDRAWAL_PREFIX
}


// IsFullyWithdrawableValidator checks if validator is fully withdrawable
func IsFullyWithdrawableValidator(validator common.Validator, balance common.Gwei, epoch common.Epoch) bool {
	withdrawableEpoch, err := validator.WithdrawableEpoch()
	if err != nil {
		return false
	}
	return (HasEth1WithdrawalCredential(validator) || has_compounding_withdrawal_credential(validator)) &&
		withdrawableEpoch <= epoch && balance > 0
}

// IsPartiallyWithdrawableValidator checks if validator is partially withdrawable
func IsPartiallyWithdrawableValidator(spec *common.Spec, validator common.Validator, balance common.Gwei) bool {
	effectiveBalance, err := validator.EffectiveBalance()
	if err != nil {
		return false
	}
	maxEffectiveBalance := get_max_effective_balance(spec, validator)
	hasMaxEffectiveBalance := effectiveBalance == maxEffectiveBalance
	hasExcessBalance := balance > maxEffectiveBalance
	return (HasEth1WithdrawalCredential(validator) || has_compounding_withdrawal_credential(validator)) &&
		hasMaxEffectiveBalance && hasExcessBalance
}

