package electra

import (
	"bytes"
	"context"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/deneb"
	. "github.com/protolambda/ztyp/view"
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
	pendingPartialWithdrawals, err := state.PendingPartialWithdrawals()
	if err != nil {
		return nil, 0, err
	}
	
	length, err := pendingPartialWithdrawals.Length()
	if err != nil {
		return nil, 0, err
	}
	
	for i := uint64(0); i < length; i++ {
		// Break if we've reached the max pending partials per sweep or max withdrawals
		if processedPartialWithdrawalsCount >= uint64(spec.MAX_PENDING_PARTIALS_PER_WITHDRAWALS_SWEEP) ||
			len(withdrawals) >= int(spec.MAX_WITHDRAWALS_PER_PAYLOAD) {
			break
		}
		
		elem, err := pendingPartialWithdrawals.Get(i)
		if err != nil {
			return nil, 0, err
		}
		
		container, err := AsContainer(elem, nil)
		if err != nil {
			return nil, 0, err
		}
		
		// Get withdrawal data from container
		validatorIndexView, err := container.Get(0) // validator_index
		if err != nil {
			return nil, 0, err
		}
		withdrawalValidatorIndex, err := common.AsValidatorIndex(validatorIndexView, nil)
		if err != nil {
			return nil, 0, err
		}
		
		amountView, err := container.Get(1) // amount
		if err != nil {
			return nil, 0, err
		}
		withdrawalAmount, err := common.AsGwei(amountView, nil)
		if err != nil {
			return nil, 0, err
		}
		
		withdrawableEpochView, err := container.Get(2) // withdrawable_epoch
		if err != nil {
			return nil, 0, err
		}
		withdrawableEpoch, err := common.AsEpoch(withdrawableEpochView, nil)
		if err != nil {
			return nil, 0, err
		}
		
		// Check if withdrawal epoch has passed
		if withdrawableEpoch > epoch {
			break
		}
		
		validator, err := validators.Validator(withdrawalValidatorIndex)
		if err != nil {
			return nil, 0, err
		}
		
		// Check validator conditions
		effectiveBalance, err := validator.EffectiveBalance()
		if err != nil {
			return nil, 0, err
		}
		
		exitEpoch, err := validator.ExitEpoch()
		if err != nil {
			return nil, 0, err
		}
		
		hasSufficientEffectiveBalance := effectiveBalance >= spec.MIN_ACTIVATION_BALANCE
		
		// Calculate total already withdrawn in this payload for this validator
		var totalWithdrawn common.Gwei
		for _, w := range withdrawals {
			if w.ValidatorIndex == withdrawalValidatorIndex {
				totalWithdrawn += w.Amount
			}
		}
		
		balance, err := balances.GetBalance(withdrawalValidatorIndex)
		if err != nil {
			return nil, 0, err
		}
		actualBalance := balance - totalWithdrawn
		hasExcessBalance := actualBalance > spec.MIN_ACTIVATION_BALANCE
		
		if exitEpoch == common.FAR_FUTURE_EPOCH && hasSufficientEffectiveBalance && hasExcessBalance {
			withdrawableBalance := actualBalance - spec.MIN_ACTIVATION_BALANCE
			if withdrawableBalance > withdrawalAmount {
				withdrawableBalance = withdrawalAmount
			}
			
			withdrawalCredentials, err := validator.WithdrawalCredentials()
			if err != nil {
				return nil, 0, err
			}
			
			withdrawals = append(withdrawals, common.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: withdrawalValidatorIndex,
				Address:        common.Eth1Address(withdrawalCredentials[12:]),
				Amount:         withdrawableBalance,
			})
			withdrawalIndex++
		}
		
		processedPartialWithdrawalsCount++
	}
	
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
		
		// [Modified in Electra:EIP7251] Account for already withdrawn amounts
		var totalWithdrawn common.Gwei
		for _, w := range withdrawals {
			if w.ValidatorIndex == validatorIndex {
				totalWithdrawn += w.Amount
			}
		}
		
		balance, err := balances.GetBalance(validatorIndex)
		if err != nil {
			return nil, 0, err
		}
		actualBalance := balance - totalWithdrawn
		
		if IsFullyWithdrawableValidator(validator, actualBalance, epoch) {
			withdrawalCredentials, err := validator.WithdrawalCredentials()
			if err != nil {
				return nil, 0, err
			}
			
			withdrawals = append(withdrawals, common.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        common.Eth1Address(withdrawalCredentials[12:]),
				Amount:         actualBalance,
			})
			withdrawalIndex++
		} else if IsPartiallyWithdrawableValidator(spec, validator, actualBalance) {
			maxEffectiveBalance := GetMaxEffectiveBalance(spec, validator)
			withdrawalCredentials, err := validator.WithdrawalCredentials()
			if err != nil {
				return nil, 0, err
			}
			
			withdrawals = append(withdrawals, common.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        common.Eth1Address(withdrawalCredentials[12:]),
				Amount:         actualBalance - maxEffectiveBalance,
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
	// Remove processed partial withdrawals from the pending list
	if processedPartialWithdrawalsCount > 0 {
		pendingPartialWithdrawals, err := state.PendingPartialWithdrawals()
		if err != nil {
			return err
		}
		
		// Create new list with remaining withdrawals after processed ones
		length, err := pendingPartialWithdrawals.Length()
		if err != nil {
			return err
		}
		
		if processedPartialWithdrawalsCount >= length {
			// All pending withdrawals were processed, clear the list
			if err := state.SetPendingPartialWithdrawals(spec, common.PendingPartialWithdrawals{}); err != nil {
				return fmt.Errorf("failed to clear pending partial withdrawals: %w", err)
			}
		} else {
			// Some withdrawals remain, create new list without processed ones
			remainingWithdrawals := make(common.PendingPartialWithdrawals, 0, length-processedPartialWithdrawalsCount)
			
			for i := processedPartialWithdrawalsCount; i < length; i++ {
				elem, err := pendingPartialWithdrawals.Get(i)
				if err != nil {
					return err
				}
				
				container, err := AsContainer(elem, nil)
				if err != nil {
					return err
				}
				
				// Extract withdrawal data
				validatorIndexView, err := container.Get(0)
				if err != nil {
					return err
				}
				validatorIdx, err := common.AsValidatorIndex(validatorIndexView, nil)
				if err != nil {
					return err
				}
				
				amountView, err := container.Get(1)
				if err != nil {
					return err
				}
				amount, err := common.AsGwei(amountView, nil)
				if err != nil {
					return err
				}
				
				withdrawableEpochView, err := container.Get(2)
				if err != nil {
					return err
				}
				withdrawableEpoch, err := common.AsEpoch(withdrawableEpochView, nil)
				if err != nil {
					return err
				}
				
				remainingWithdrawals = append(remainingWithdrawals, common.PendingPartialWithdrawal{
					ValidatorIndex:    validatorIdx,
					Amount:            amount,
					WithdrawableEpoch: withdrawableEpoch,
				})
			}
			
			if err := state.SetPendingPartialWithdrawals(spec, remainingWithdrawals); err != nil {
				return fmt.Errorf("failed to update pending partial withdrawals: %w", err)
			}
		}
	}
	
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
	return (HasEth1WithdrawalCredential(validator) || HasCompoundingWithdrawalCredential(validator)) &&
		withdrawableEpoch <= epoch && balance > 0
}

// IsPartiallyWithdrawableValidator checks if validator is partially withdrawable
func IsPartiallyWithdrawableValidator(spec *common.Spec, validator common.Validator, balance common.Gwei) bool {
	effectiveBalance, err := validator.EffectiveBalance()
	if err != nil {
		return false
	}
	maxEffectiveBalance := GetMaxEffectiveBalance(spec, validator)
	hasMaxEffectiveBalance := effectiveBalance == maxEffectiveBalance
	hasExcessBalance := balance > maxEffectiveBalance
	return (HasEth1WithdrawalCredential(validator) || HasCompoundingWithdrawalCredential(validator)) &&
		hasMaxEffectiveBalance && hasExcessBalance
}

