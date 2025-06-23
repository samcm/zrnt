package electra

import (
	"context"
	
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
	blsu "github.com/protolambda/bls12-381-util"
)

// IsCompoundingWithdrawalCredential checks if the given withdrawal credentials have a compounding prefix
func IsCompoundingWithdrawalCredential(withdrawal_credentials common.Root) bool {
	return withdrawal_credentials[0] == COMPOUNDING_WITHDRAWAL_PREFIX
}

// HasCompoundingWithdrawalCredential checks if validator has an 0x02 prefixed "compounding" withdrawal credential
func HasCompoundingWithdrawalCredential(validator common.Validator) bool {
	wc, err := validator.WithdrawalCredentials()
	if err != nil {
		return false
	}
	return IsCompoundingWithdrawalCredential(wc)
}

// HasExecutionWithdrawalCredential checks if validator has a 0x01 or 0x02 prefixed withdrawal credential
func HasExecutionWithdrawalCredential(validator common.Validator) bool {
	wc, err := validator.WithdrawalCredentials()
	if err != nil {
		return false
	}
	// Check for ETH1_ADDRESS_WITHDRAWAL_PREFIX (0x01) or COMPOUNDING_WITHDRAWAL_PREFIX (0x02)
	return wc[0] == common.ETH1_ADDRESS_WITHDRAWAL_PREFIX || HasCompoundingWithdrawalCredential(validator)
}

// GetMaxEffectiveBalance gets max effective balance for validator
func GetMaxEffectiveBalance(spec *common.Spec, validator common.Validator) common.Gwei {
	// [Modified in Electra:EIP7251]
	// For validators with compounding withdrawal credentials,
	// the max effective balance is MAX_EFFECTIVE_BALANCE_ELECTRA (2048 ETH)
	// For all other validators, it is MIN_ACTIVATION_BALANCE (32 ETH)
	if HasCompoundingWithdrawalCredential(validator) {
		return spec.MAX_EFFECTIVE_BALANCE_ELECTRA
	} else {
		return spec.MIN_ACTIVATION_BALANCE
	}
}

// GetPendingBalanceToWithdraw returns the sum of pending withdrawals for a given validator index
func GetPendingBalanceToWithdraw(state *BeaconStateView, validatorIndex common.ValidatorIndex) (common.Gwei, error) {
	partialWithdrawals, err := state.PendingPartialWithdrawals()
	if err != nil {
		return 0, err
	}
	
	var pendingBalance common.Gwei
	length, err := partialWithdrawals.Length()
	if err != nil {
		return 0, err
	}
	
	for i := uint64(0); i < length; i++ {
		elem, err := partialWithdrawals.Get(i)
		if err != nil {
			return 0, err
		}
		
		// Cast to container view
		container, err := AsContainer(elem, nil)
		if err != nil {
			return 0, err
		}
		
		// Get validator index from the element
		indexView, err := container.Get(0) // validator_index is first field
		if err != nil {
			return 0, err
		}
		idx, err := common.AsValidatorIndex(indexView, nil)
		if err != nil {
			return 0, err
		}
		
		if idx == validatorIndex {
			// Get amount from the element
			amountView, err := container.Get(1) // amount is second field
			if err != nil {
				return 0, err
			}
			amount, err := common.AsGwei(amountView, nil)
			if err != nil {
				return 0, err
			}
			pendingBalance += amount
		}
	}
	
	return pendingBalance, nil
}

// GetBalanceChurnLimit returns the churn limit for the current epoch
func GetBalanceChurnLimit(spec *common.Spec, state *BeaconStateView) (common.Gwei, error) {
	validators, err := state.Validators()
	if err != nil {
		return 0, err
	}
	
	slot, err := state.Slot()
	if err != nil {
		return 0, err
	}
	epoch := spec.SlotToEpoch(slot)
	
	// Calculate total active balance
	var totalActiveBalance common.Gwei
	length, err := validators.ValidatorCount()
	if err != nil {
		return 0, err
	}
	
	for i := common.ValidatorIndex(0); uint64(i) < length; i++ {
		validator, err := validators.Validator(i)
		if err != nil {
			return 0, err
		}
		
		// Check if validator is active
		activationEpoch, err := validator.ActivationEpoch()
		if err != nil {
			return 0, err
		}
		exitEpoch, err := validator.ExitEpoch()
		if err != nil {
			return 0, err
		}
		
		if activationEpoch <= epoch && epoch < exitEpoch {
			effBal, err := validator.EffectiveBalance()
			if err != nil {
				return 0, err
			}
			totalActiveBalance += effBal
		}
	}
	
	churn := common.Gwei(uint64(totalActiveBalance) / uint64(spec.CHURN_LIMIT_QUOTIENT))
	if churn < common.Gwei(spec.MIN_PER_EPOCH_CHURN_LIMIT_ELECTRA) {
		churn = common.Gwei(spec.MIN_PER_EPOCH_CHURN_LIMIT_ELECTRA)
	}
	// Round down to nearest EFFECTIVE_BALANCE_INCREMENT
	churn = churn - churn%common.Gwei(spec.EFFECTIVE_BALANCE_INCREMENT)
	return churn, nil
}

// GetActivationExitChurnLimit returns the activation/exit churn limit for the current epoch
func GetActivationExitChurnLimit(spec *common.Spec, state *BeaconStateView) (common.Gwei, error) {
	churn, err := GetBalanceChurnLimit(spec, state)
	if err != nil {
		return 0, err
	}
	
	if churn > common.Gwei(spec.MAX_PER_EPOCH_ACTIVATION_EXIT_CHURN_LIMIT) {
		return common.Gwei(spec.MAX_PER_EPOCH_ACTIVATION_EXIT_CHURN_LIMIT), nil
	}
	return churn, nil
}

// GetConsolidationChurnLimit returns the consolidation churn limit for the current epoch
func GetConsolidationChurnLimit(spec *common.Spec, state *BeaconStateView) (common.Gwei, error) {
	balanceChurn, err := GetBalanceChurnLimit(spec, state)
	if err != nil {
		return 0, err
	}
	
	activationExitChurn, err := GetActivationExitChurnLimit(spec, state)
	if err != nil {
		return 0, err
	}
	
	// Consolidation churn is the balance churn minus activation/exit churn
	if balanceChurn > activationExitChurn {
		return balanceChurn - activationExitChurn, nil
	}
	return 0, nil
}

// IsValidDepositSignature verifies a deposit signature
func IsValidDepositSignature(spec *common.Spec, pubkey common.BLSPubkey, withdrawalCredentials common.Root, amount common.Gwei, signature common.BLSSignature) (bool, error) {
	depositMessage := &common.DepositMessage{
		Pubkey:                pubkey,
		WithdrawalCredentials: withdrawalCredentials,
		Amount:                amount,
	}
	
	domain := common.ComputeDomain(common.DOMAIN_DEPOSIT, spec.GENESIS_FORK_VERSION, common.Root{})
	
	signingRoot := common.ComputeSigningRoot(
		depositMessage.HashTreeRoot(tree.GetHashFn()),
		domain,
	)
	
	blsPub, err := pubkey.Pubkey()
	if err != nil {
		// Invalid public key - deposit is invalid but block is still valid
		return false, nil
	}
	
	blsSig, err := signature.Signature()
	if err != nil {
		// Invalid signature - deposit is invalid but block is still valid
		return false, nil
	}
	
	return blsu.Verify(blsPub, signingRoot[:], blsSig), nil
}

// AddValidatorToRegistry adds a new validator to the registry
func AddValidatorToRegistry(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, pubkey common.BLSPubkey, withdrawalCredentials common.Root, amount common.Gwei) error {
	// Use the state's AddValidator method which properly handles all the registries
	return state.AddValidator(spec, pubkey, withdrawalCredentials, amount)
}

// GetValidatorFromDeposit creates a new validator from deposit data
func GetValidatorFromDeposit(spec *common.Spec, pubkey common.BLSPubkey, withdrawalCredentials common.Root, amount common.Gwei) *phase0.ValidatorView {
	validator := phase0.Validator{
		Pubkey:                     pubkey,
		WithdrawalCredentials:      withdrawalCredentials,
		ActivationEligibilityEpoch: common.FAR_FUTURE_EPOCH,
		ActivationEpoch:            common.FAR_FUTURE_EPOCH,
		ExitEpoch:                  common.FAR_FUTURE_EPOCH,
		WithdrawableEpoch:          common.FAR_FUTURE_EPOCH,
		EffectiveBalance:           0,
		Slashed:                    false,
	}
	
	// Calculate max effective balance based on withdrawal credentials
	maxEffectiveBalance := spec.MIN_ACTIVATION_BALANCE
	if IsCompoundingWithdrawalCredential(withdrawalCredentials) {
		maxEffectiveBalance = spec.MAX_EFFECTIVE_BALANCE_ELECTRA
	}
	
	validator.EffectiveBalance = amount - amount%spec.EFFECTIVE_BALANCE_INCREMENT
	if validator.EffectiveBalance > maxEffectiveBalance {
		validator.EffectiveBalance = maxEffectiveBalance
	}
	
	return validator.View()
}