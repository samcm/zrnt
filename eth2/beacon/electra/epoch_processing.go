package electra

import (
	"context"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	. "github.com/protolambda/ztyp/view"
)

// apply_pending_deposit applies a pending deposit to the state
func ApplyPendingDeposit(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, deposit *common.PendingDeposit) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	validators, err := state.Validators()
	if err != nil {
		return err
	}

	// Check if validator already exists
	pubkey := deposit.Pubkey
	validatorIndex := common.ValidatorIndexMarker
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return err
	}

	// Find validator by pubkey
	for i := common.ValidatorIndex(0); i < common.ValidatorIndex(validatorCount); i++ {
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
		// Verify the deposit signature (proof of possession)
		if isValid, err := IsValidDepositSignature(spec, pubkey, deposit.WithdrawalCredentials, deposit.Amount, deposit.Signature); err != nil {
			return err
		} else if isValid {
			// Add new validator
			stateView, ok := state.(*BeaconStateView)
			if !ok {
				return fmt.Errorf("state is not a BeaconStateView")
			}
			if err := AddValidatorToRegistry(ctx, spec, epc, stateView, pubkey, deposit.WithdrawalCredentials, deposit.Amount); err != nil {
				return err
			}
		}
	} else {
		// Increase balance of existing validator
		balances, err := state.Balances()
		if err != nil {
			return err
		}
		if err := common.IncreaseBalance(balances, validatorIndex, deposit.Amount); err != nil {
			return err
		}
	}

	return nil
}

// process_pending_deposits processes all pending deposits for the epoch
func ProcessPendingDeposits(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	slot, err := state.Slot()
	if err != nil {
		return err
	}

	currentEpoch := spec.SlotToEpoch(slot)
	nextEpoch := currentEpoch + 1

	depositBalanceToConsume, err := state.DepositBalanceToConsume()
	if err != nil {
		return err
	}

	churnLimit, err := GetActivationExitChurnLimit(spec, state)
	if err != nil {
		return err
	}
	availableForProcessing := depositBalanceToConsume + churnLimit
	processedAmount := common.Gwei(0)
	nextDepositIndex := uint64(0)
	depositsToPostpone := make(common.PendingDeposits, 0)
	isChurnLimitReached := false

	finalizedCheckpoint, err := state.FinalizedCheckpoint()
	if err != nil {
		return err
	}
	finalizedSlot := common.Slot(uint64(spec.SLOTS_PER_EPOCH) * uint64(finalizedCheckpoint.Epoch))

	pendingDeposits, err := state.PendingDeposits()
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

	validators, err := state.Validators()
	if err != nil {
		return err
	}

	// Process each pending deposit
	pendingDepositsCount, err := pendingDeposits.Length()
	if err != nil {
		return err
	}
	for i := uint64(0); i < pendingDepositsCount; i++ {
		if nextDepositIndex >= uint64(spec.MAX_PENDING_DEPOSITS_PER_EPOCH) {
			break
		}

		depositView, err := pendingDeposits.Get(uint64(i))
		if err != nil {
			return err
		}

		// Extract deposit from view
		containerView, ok := depositView.(*ContainerView)
		if !ok {
			return fmt.Errorf("invalid deposit view type")
		}
		
		// Get fields from container
		pubkeyView, err := containerView.Get(0)
		if err != nil {
			return err
		}
		pubkey, err := common.AsBLSPubkey(pubkeyView, err)
		if err != nil {
			return err
		}
		
		wcView, err := containerView.Get(1)
		if err != nil {
			return err
		}
		wc, err := AsRoot(wcView, err)
		if err != nil {
			return err
		}
		
		amountView, err := containerView.Get(2)
		if err != nil {
			return err
		}
		amount := common.Gwei(0)
		if gv, ok := amountView.(Uint64View); ok {
			amount = common.Gwei(gv)
		} else {
			return fmt.Errorf("invalid amount view type")
		}
		if err != nil {
			return err
		}
		
		sigView, err := containerView.Get(3)
		if err != nil {
			return err
		}
		sig, err := common.AsBLSSignature(sigView, err)
		if err != nil {
			return err
		}
		
		slotView, err := containerView.Get(4)
		if err != nil {
			return err
		}
		slot, err := common.AsSlot(slotView, err)
		if err != nil {
			return err
		}
		
		deposit := common.PendingDeposit{
			Pubkey:                pubkey,
			WithdrawalCredentials: wc,
			Amount:                amount,
			Signature:             sig,
			Slot:                  slot,
		}

		// Do not process deposit requests if Eth1 bridge deposits are not yet applied
		if deposit.Slot > common.GENESIS_SLOT && uint64(eth1DepositIndex) < uint64(depositRequestsStartIndex) {
			break
		}

		// Check if deposit has been finalized
		if deposit.Slot > finalizedSlot {
			break
		}

		// Read validator state
		isValidatorExited := false
		isValidatorWithdrawn := false
		validatorIndex := common.ValidatorIndexMarker
		_ = validatorIndex // Mark as used

		validatorCount, err := validators.ValidatorCount()
		if err != nil {
			return err
		}

		// Find validator by pubkey
		for idx := common.ValidatorIndex(0); idx < common.ValidatorIndex(validatorCount); idx++ {
			val, err := validators.Validator(idx)
			if err != nil {
				return err
			}
			valPubkey, err := val.Pubkey()
			if err != nil {
				return err
			}
			if valPubkey == deposit.Pubkey {
				validatorIndex = idx
				exitEpoch, err := val.ExitEpoch()
				if err != nil {
					return err
				}
				withdrawableEpoch, err := val.WithdrawableEpoch()
				if err != nil {
					return err
				}
				isValidatorExited = exitEpoch < common.FAR_FUTURE_EPOCH
				isValidatorWithdrawn = withdrawableEpoch < nextEpoch
				break
			}
		}

		if isValidatorWithdrawn {
			// Deposited balance will never become active. Increase balance but do not consume churn
			if err := ApplyPendingDeposit(ctx, spec, epc, state, &deposit); err != nil {
				return err
			}
		} else if isValidatorExited {
			// Validator is exiting, postpone the deposit until after withdrawable epoch
			depositsToPostpone = append(depositsToPostpone, deposit)
		} else {
			// Check if deposit fits in the churn
			isChurnLimitReached = processedAmount+deposit.Amount > availableForProcessing
			if isChurnLimitReached {
				break
			}

			// Consume churn and apply deposit
			processedAmount += deposit.Amount
			if err := ApplyPendingDeposit(ctx, spec, epc, state, &deposit); err != nil {
				return err
			}
		}

		nextDepositIndex++
	}

	// Update pending deposits list
	remainingDeposits := make(common.PendingDeposits, 0)
	for i := nextDepositIndex; i < pendingDepositsCount; i++ {
		depositView, err := pendingDeposits.Get(uint64(i))
		if err != nil {
			return err
		}
		// Extract deposit from view
		containerView, ok := depositView.(*ContainerView)
		if !ok {
			return fmt.Errorf("invalid deposit view type")
		}
		
		// Get fields from container
		pubkeyView, err := containerView.Get(0)
		if err != nil {
			return err
		}
		pubkey, err := common.AsBLSPubkey(pubkeyView, err)
		if err != nil {
			return err
		}
		
		wcView, err := containerView.Get(1)
		if err != nil {
			return err
		}
		wc, err := AsRoot(wcView, err)
		if err != nil {
			return err
		}
		
		amountView, err := containerView.Get(2)
		if err != nil {
			return err
		}
		amount := common.Gwei(0)
		if gv, ok := amountView.(Uint64View); ok {
			amount = common.Gwei(gv)
		} else {
			return fmt.Errorf("invalid amount view type")
		}
		if err != nil {
			return err
		}
		
		sigView, err := containerView.Get(3)
		if err != nil {
			return err
		}
		sig, err := common.AsBLSSignature(sigView, err)
		if err != nil {
			return err
		}
		
		slotView, err := containerView.Get(4)
		if err != nil {
			return err
		}
		slot, err := common.AsSlot(slotView, err)
		if err != nil {
			return err
		}
		
		deposit := common.PendingDeposit{
			Pubkey:                pubkey,
			WithdrawalCredentials: wc,
			Amount:                amount,
			Signature:             sig,
			Slot:                  slot,
		}
		remainingDeposits = append(remainingDeposits, deposit)
	}
	
	newPendingDeposits := append(remainingDeposits, depositsToPostpone...)
	if err := state.SetPendingDeposits(spec, newPendingDeposits); err != nil {
		return err
	}

	// Update deposit balance to consume
	if isChurnLimitReached {
		if err := state.SetDepositBalanceToConsume(availableForProcessing - processedAmount); err != nil {
			return err
		}
	} else {
		if err := state.SetDepositBalanceToConsume(0); err != nil {
			return err
		}
	}

	return nil
}

// process_pending_consolidations processes all pending consolidations
func ProcessPendingConsolidations(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	slot, err := state.Slot()
	if err != nil {
		return err
	}
	currentEpoch := spec.SlotToEpoch(slot)
	nextEpoch := currentEpoch + 1

	pendingConsolidations, err := state.PendingConsolidations()
	if err != nil {
		return err
	}

	validators, err := state.Validators()
	if err != nil {
		return err
	}

	balances, err := state.Balances()
	if err != nil {
		return err
	}

	nextPendingConsolidation := uint64(0)
	consolidationsCount, err := pendingConsolidations.Length()
	if err != nil {
		return err
	}

	for i := uint64(0); i < consolidationsCount; i++ {
		consolidationView, err := pendingConsolidations.Get(i)
		if err != nil {
			return err
		}

		// Extract consolidation from view
		containerView, ok := consolidationView.(*ContainerView)
		if !ok {
			return fmt.Errorf("invalid consolidation view type")
		}
		
		// Get source index
		sourceIndexView, err := containerView.Get(0)
		if err != nil {
			return err
		}
		sourceIndex, err := common.AsValidatorIndex(sourceIndexView, err)
		if err != nil {
			return err
		}
		
		// Get target index
		targetIndexView, err := containerView.Get(1)
		if err != nil {
			return err
		}
		targetIndex, err := common.AsValidatorIndex(targetIndexView, err)
		if err != nil {
			return err
		}
		
		pendingConsolidation := common.PendingConsolidation{
			SourceIndex: sourceIndex,
			TargetIndex: targetIndex,
		}

		sourceValidator, err := validators.Validator(pendingConsolidation.SourceIndex)
		if err != nil {
			return err
		}

		slashed, err := sourceValidator.Slashed()
		if err != nil {
			return err
		}

		if slashed {
			nextPendingConsolidation++
			continue
		}

		withdrawableEpoch, err := sourceValidator.WithdrawableEpoch()
		if err != nil {
			return err
		}

		if withdrawableEpoch > nextEpoch {
			break
		}

		// Calculate the consolidated balance
		sourceBalance, err := balances.GetBalance(pendingConsolidation.SourceIndex)
		if err != nil {
			return err
		}

		sourceEffectiveBalance, err := sourceValidator.EffectiveBalance()
		if err != nil {
			return err
		}

		consolidatedBalance := sourceBalance
		if consolidatedBalance > sourceEffectiveBalance {
			consolidatedBalance = sourceEffectiveBalance
		}

		// Move active balance to target
		if err := common.DecreaseBalance(balances, pendingConsolidation.SourceIndex, consolidatedBalance); err != nil {
			return err
		}
		if err := common.IncreaseBalance(balances, pendingConsolidation.TargetIndex, consolidatedBalance); err != nil {
			return err
		}

		nextPendingConsolidation++
	}

	// Update pending consolidations list
	if nextPendingConsolidation > 0 {
		remainingConsolidations := make(common.PendingConsolidations, 0)
		for i := nextPendingConsolidation; i < consolidationsCount; i++ {
			consolidationView, err := pendingConsolidations.Get(i)
			if err != nil {
				return err
			}
			// Extract consolidation from view
			containerView, ok := consolidationView.(*ContainerView)
			if !ok {
				return fmt.Errorf("invalid consolidation view type")
			}
			
			// Get source index
			sourceIndexView, err := containerView.Get(0)
			if err != nil {
				return err
			}
			sourceIndex, err := common.AsValidatorIndex(sourceIndexView, err)
			if err != nil {
				return err
			}
			
			// Get target index
			targetIndexView, err := containerView.Get(1)
			if err != nil {
				return err
			}
			targetIndex, err := common.AsValidatorIndex(targetIndexView, err)
			if err != nil {
				return err
			}
			
			consolidation := common.PendingConsolidation{
				SourceIndex: sourceIndex,
				TargetIndex: targetIndex,
			}
			remainingConsolidations = append(remainingConsolidations, consolidation)
		}
		if err := state.SetPendingConsolidations(spec, remainingConsolidations); err != nil {
			return err
		}
	}

	return nil
}

// Modified process_effective_balance_updates for Electra
func ProcessEffectiveBalanceUpdates(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Update effective balances with hysteresis
	vals, err := state.Validators()
	if err != nil {
		return err
	}
	bals, err := state.Balances()
	if err != nil {
		return err
	}

	HYSTERESIS_INCREMENT := spec.EFFECTIVE_BALANCE_INCREMENT / common.Gwei(spec.HYSTERESIS_QUOTIENT)
	DOWNWARD_THRESHOLD := HYSTERESIS_INCREMENT * common.Gwei(spec.HYSTERESIS_DOWNWARD_MULTIPLIER)
	UPWARD_THRESHOLD := HYSTERESIS_INCREMENT * common.Gwei(spec.HYSTERESIS_UPWARD_MULTIPLIER)

	for i, flat := range flats {
		index := common.ValidatorIndex(i)
		balance, err := bals.GetBalance(index)
		if err != nil {
			return err
		}

		// Get the actual validator for max effective balance calculation
		val, err := vals.Validator(index)
		if err != nil {
			return err
		}
		
		if balance+DOWNWARD_THRESHOLD < flat.EffectiveBalance ||
			flat.EffectiveBalance+UPWARD_THRESHOLD < balance {
			newEffectiveBalance := balance - balance%spec.EFFECTIVE_BALANCE_INCREMENT
			
			// [Modified in Electra:EIP7251]
			// Get the appropriate cap for this validator
			maxEffectiveBalance := GetMaxEffectiveBalance(spec, val)
			
			// Cap the new effective balance
			if newEffectiveBalance > maxEffectiveBalance {
				newEffectiveBalance = maxEffectiveBalance
			}
			if err := val.SetEffectiveBalance(newEffectiveBalance); err != nil {
				return err
			}
		}
	}
	return nil
}

// Modified process_registry_updates for Electra
func ProcessRegistryUpdates(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	currentEpoch := epc.CurrentEpoch.Epoch
	
	finalizedCheckpoint, err := state.FinalizedCheckpoint()
	if err != nil {
		return err
	}
	vals, err := state.Validators()
	if err != nil {
		return err
	}

	exitEpoch := spec.ComputeActivationExitEpoch(currentEpoch)

	// Process activation eligibility, ejections, and activations
	for i := common.ValidatorIndex(0); i < common.ValidatorIndex(len(flats)); i++ {
		flat := &flats[i]
		val, err := vals.Validator(i)
		if err != nil {
			return err
		}

		// Check activation eligibility
		if isEligible, err := isEligibleForActivationQueue(val, spec); err != nil {
			return err
		} else if isEligible {
			if err := val.SetActivationEligibilityEpoch(currentEpoch + 1); err != nil {
				return err
			}
		} else if flat.IsActive(currentEpoch) && flat.EffectiveBalance <= spec.EJECTION_BALANCE {
			// Initiate exit for validators with insufficient balance
			if err := InitiateValidatorExit(ctx, spec, epc, state.(*BeaconStateView), i); err != nil {
				return err
			}
		} else if flat.ActivationEligibilityEpoch <= finalizedCheckpoint.Epoch && flat.ActivationEpoch == common.FAR_FUTURE_EPOCH {
			// Activate eligible validators
			if err := val.SetActivationEpoch(exitEpoch); err != nil {
				return err
			}
		}
	}

	return nil
}

// Modified process_slashings for Electra
func ProcessSlashings(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, flats []common.FlatValidator, state common.BeaconState) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	currentEpoch := epc.CurrentEpoch.Epoch
	totalBalance := epc.TotalActiveStake

	slashingsMux, err := state.Slashings()
	if err != nil {
		return err
	}
	totalSlashings, err := slashingsMux.Total()
	if err != nil {
		return err
	}

	adjustedTotalSlashingBalance := totalSlashings * common.Gwei(spec.PROPORTIONAL_SLASHING_MULTIPLIER_BELLATRIX)
	if adjustedTotalSlashingBalance > totalBalance {
		adjustedTotalSlashingBalance = totalBalance
	}

	bals, err := state.Balances()
	if err != nil {
		return err
	}

	increment := spec.EFFECTIVE_BALANCE_INCREMENT
	penaltyPerEffectiveBalanceIncrement := adjustedTotalSlashingBalance / (totalBalance / increment)

	for i := common.ValidatorIndex(0); i < common.ValidatorIndex(len(flats)); i++ {
		flat := &flats[i]
		if flat.Slashed && currentEpoch+spec.EPOCHS_PER_SLASHINGS_VECTOR/2 == flat.WithdrawableEpoch {
			effectiveBalanceIncrements := flat.EffectiveBalance / increment
			penalty := penaltyPerEffectiveBalanceIncrement * effectiveBalanceIncrements
			if err := common.DecreaseBalance(bals, i, penalty); err != nil {
				return err
			}
		}
	}

	return nil
}

// Helper functions are already defined in helpers.go