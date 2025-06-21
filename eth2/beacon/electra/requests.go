package electra

import (
	"bytes"
	"context"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type ExecutionRequests struct {
	// [New in Electra:EIP6110]
	Deposits common.DepositRequests `json:"deposits" yaml:"deposits"`
	// [New in Electra:EIP7002:EIP7251]
	Withdrawals common.WithdrawalRequests `json:"withdrawals" yaml:"withdrawals"`
	// [New in Electra:EIP7251]
	Consolidations common.ConsolidationRequests `json:"consolidations" yaml:"consolidations"`
}

func ExecutionRequestsType(spec *common.Spec) *ContainerTypeDef {
	return ContainerType("ExecutionRequests", []FieldDef{
		{Name: "deposits", Type: common.DepositRequestsType(spec)},
		{Name: "withdrawals", Type: common.WithdrawalRequestsType(spec)},
		{Name: "consolidations", Type: common.ConsolidationRequestsType(spec)},
	})
}

func (p *ExecutionRequests) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}

func (p *ExecutionRequests) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}

func (p *ExecutionRequests) ByteLength(spec *common.Spec) uint64 {
	return codec.ContainerLength(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}

func (*ExecutionRequests) FixedLength(*common.Spec) uint64 {
	return 0
}

func (p *ExecutionRequests) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(spec.Wrap(&p.Deposits), spec.Wrap(&p.Withdrawals), spec.Wrap(&p.Consolidations))
}

// GetExecutionRequestsList encodes execution requests as defined by EIP-7685
func GetExecutionRequestsList(spec *common.Spec, executionRequests *ExecutionRequests) ([][]byte, error) {
	requests := make([][]byte, 0)

	// Process deposit requests
	if len(executionRequests.Deposits) > 0 {
		depositsData := make([]byte, 0)
		for _, deposit := range executionRequests.Deposits {
			buf := bytes.NewBuffer(nil)
			w := codec.NewEncodingWriter(buf)
			if err := deposit.Serialize(w); err != nil {
				return nil, fmt.Errorf("failed to serialize deposit request: %w", err)
			}
			depositsData = append(depositsData, buf.Bytes()...)
		}
		requests = append(requests, append([]byte{DEPOSIT_REQUEST_TYPE}, depositsData...))
	}

	// Process withdrawal requests
	if len(executionRequests.Withdrawals) > 0 {
		withdrawalsData := make([]byte, 0)
		for _, withdrawal := range executionRequests.Withdrawals {
			buf := bytes.NewBuffer(nil)
			w := codec.NewEncodingWriter(buf)
			if err := withdrawal.Serialize(w); err != nil {
				return nil, fmt.Errorf("failed to serialize withdrawal request: %w", err)
			}
			withdrawalsData = append(withdrawalsData, buf.Bytes()...)
		}
		requests = append(requests, append([]byte{WITHDRAWAL_REQUEST_TYPE}, withdrawalsData...))
	}

	// Process consolidation requests
	if len(executionRequests.Consolidations) > 0 {
		consolidationsData := make([]byte, 0)
		for _, consolidation := range executionRequests.Consolidations {
			buf := bytes.NewBuffer(nil)
			w := codec.NewEncodingWriter(buf)
			if err := consolidation.Serialize(w); err != nil {
				return nil, fmt.Errorf("failed to serialize consolidation request: %w", err)
			}
			consolidationsData = append(consolidationsData, buf.Bytes()...)
		}
		requests = append(requests, append([]byte{CONSOLIDATION_REQUEST_TYPE}, consolidationsData...))
	}

	return requests, nil
}

// ProcessDepositRequest processes a deposit request from the execution layer
func ProcessDepositRequest(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, depositRequest *common.DepositRequest) error {
	if depositRequest == nil {
		return fmt.Errorf("deposit request is nil")
	}

	// Set deposit request start index
	depositRequestsStartIndex, err := state.DepositRequestsStartIndex()
	if err != nil {
		return err
	}

	if depositRequestsStartIndex == Uint64View(UNSET_DEPOSIT_REQUESTS_START_INDEX) {
		if err := state.SetDepositRequestsStartIndex(Uint64View(depositRequest.Index)); err != nil {
			return err
		}
	}

	// Get current slot
	slot, err := state.Slot()
	if err != nil {
		return err
	}

	// Create pending deposit
	pendingDeposit := common.PendingDeposit{
		Pubkey:                depositRequest.Pubkey,
		WithdrawalCredentials: depositRequest.WithdrawalCredentials,
		Amount:                depositRequest.Amount,
		Signature:             depositRequest.Signature,
		Slot:                  slot,
	}

	// For simplicity, just append to existing deposits
	// In production, would need to handle limit properly
	return state.AppendPendingDeposit(pendingDeposit)
}

// ProcessWithdrawalRequest processes a withdrawal request from the execution layer
func ProcessWithdrawalRequest(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, withdrawalRequest *common.WithdrawalRequest) error {
	amount := withdrawalRequest.Amount
	isFullExitRequest := amount == common.Gwei(FULL_EXIT_REQUEST_AMOUNT)

	// If partial withdrawal queue is full, only full exits are processed
	pendingPartialWithdrawals, err := state.PendingPartialWithdrawals()
	if err != nil {
		return err
	}

	pendingCount, err := pendingPartialWithdrawals.Length()
	if err != nil {
		return err
	}
	if pendingCount == uint64(spec.PENDING_PARTIAL_WITHDRAWALS_LIMIT) && !isFullExitRequest {
		return nil
	}

	// Find validator by pubkey
	validators, err := state.Validators()
	if err != nil {
		return err
	}

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
		if valPubkey == withdrawalRequest.ValidatorPubkey {
			validatorIndex = i
			break
		}
	}

	if validatorIndex == common.ValidatorIndexMarker {
		return nil // Validator not found
	}

	validator, err := validators.Validator(validatorIndex)
	if err != nil {
		return err
	}

	// Verify withdrawal credentials
	withdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		return err
	}

	hasCorrectCredential := withdrawalCredentials[0] == common.ETH1_ADDRESS_WITHDRAWAL_PREFIX ||
		withdrawalCredentials[0] == COMPOUNDING_WITHDRAWAL_PREFIX
	isCorrectSourceAddress := bytes.Equal(withdrawalCredentials[12:], withdrawalRequest.SourceAddress[:])

	if !hasCorrectCredential || !isCorrectSourceAddress {
		return nil
	}

	// Verify the validator is active
	slot, err := state.Slot()
	if err != nil {
		return err
	}
	currentEpoch := spec.SlotToEpoch(slot)

	isActive, err := phase0.IsActive(validator, currentEpoch)
	if err != nil {
		return err
	}
	if !isActive {
		return nil
	}

	// Verify exit has not been initiated
	exitEpoch, err := validator.ExitEpoch()
	if err != nil {
		return err
	}
	if exitEpoch != common.FAR_FUTURE_EPOCH {
		return nil
	}

	// Verify the validator has been active long enough
	activationEpoch, err := validator.ActivationEpoch()
	if err != nil {
		return err
	}
	if currentEpoch < activationEpoch+spec.SHARD_COMMITTEE_PERIOD {
		return nil
	}

	pendingBalanceToWithdraw, err := get_pending_balance_to_withdraw(state, validatorIndex)
	if err != nil {
		return err
	}

	if isFullExitRequest {
		// Only exit validator if it has no pending withdrawals
		if pendingBalanceToWithdraw == 0 {
			return InitiateValidatorExit(ctx, spec, epc, state, validatorIndex)
		}
		return nil
	}

	// Partial withdrawal logic
	effectiveBalance, err := validator.EffectiveBalance()
	if err != nil {
		return err
	}

	balances, err := state.Balances()
	if err != nil {
		return err
	}

	balance, err := balances.GetBalance(validatorIndex)
	if err != nil {
		return err
	}

	hasSufficientEffectiveBalance := effectiveBalance >= spec.MIN_ACTIVATION_BALANCE
	hasExcessBalance := balance > spec.MIN_ACTIVATION_BALANCE+pendingBalanceToWithdraw

	// Only allow partial withdrawals with compounding withdrawal credentials
	if withdrawalCredentials[0] == COMPOUNDING_WITHDRAWAL_PREFIX && hasSufficientEffectiveBalance && hasExcessBalance {
		toWithdraw := balance - spec.MIN_ACTIVATION_BALANCE - pendingBalanceToWithdraw
		if toWithdraw > amount {
			toWithdraw = amount
		}

		exitQueueEpoch, err := ComputeExitEpochAndUpdateChurn(ctx, spec, state, toWithdraw)
		if err != nil {
			return err
		}

		withdrawableEpoch := exitQueueEpoch + spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY
		if withdrawableEpoch < exitQueueEpoch { // Check for overflow
			return fmt.Errorf("withdrawable epoch overflow: %d + %d = %d", exitQueueEpoch, spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY, withdrawableEpoch)
		}

		// Add to pending partial withdrawals
		pendingWithdrawal := common.PendingPartialWithdrawal{
			ValidatorIndex:    validatorIndex,
			Amount:            toWithdraw,
			WithdrawableEpoch: withdrawableEpoch,
		}

		return state.AppendPendingPartialWithdrawal(pendingWithdrawal)
	}

	return nil
}

// ProcessConsolidationRequest processes a consolidation request from the execution layer
func ProcessConsolidationRequest(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state *BeaconStateView, consolidationRequest *common.ConsolidationRequest) error {
	// Check if this is a switch to compounding request
	if IsValidSwitchToCompoundingRequest(spec, state, consolidationRequest) {
		validators, err := state.Validators()
		if err != nil {
			return err
		}

		// Find source validator
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
			if valPubkey == consolidationRequest.SourcePubkey {
				validatorIndex = i
				break
			}
		}

		if validatorIndex != common.ValidatorIndexMarker {
			// Switch to compounding withdrawal credentials
			return SwitchToCompoundingValidator(ctx, spec, state, validatorIndex)
		}
		return nil
	}

	// Verify that source != target
	if consolidationRequest.SourcePubkey == consolidationRequest.TargetPubkey {
		return nil
	}

	// Check queue limits
	pendingConsolidations, err := state.PendingConsolidations()
	if err != nil {
		return err
	}

	pendingConsCount, err := pendingConsolidations.Length()
	if err != nil {
		return err
	}
	if pendingConsCount == uint64(spec.PENDING_CONSOLIDATIONS_LIMIT) {
		return nil
	}

	// Check consolidation churn limit
	consolidationChurnLimit, err := get_consolidation_churn_limit(spec, state)
	if err != nil {
		return err
	}
	if consolidationChurnLimit <= spec.MIN_ACTIVATION_BALANCE {
		return nil
	}

	// Find validators
	validators, err := state.Validators()
	if err != nil {
		return err
	}

	sourceIndex := common.ValidatorIndexMarker
	targetIndex := common.ValidatorIndexMarker
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
		if valPubkey == consolidationRequest.SourcePubkey {
			sourceIndex = i
		}
		if valPubkey == consolidationRequest.TargetPubkey {
			targetIndex = i
		}
		if sourceIndex != common.ValidatorIndexMarker && targetIndex != common.ValidatorIndexMarker {
			break
		}
	}

	if sourceIndex == common.ValidatorIndexMarker || targetIndex == common.ValidatorIndexMarker {
		return nil
	}

	sourceValidator, err := validators.Validator(sourceIndex)
	if err != nil {
		return err
	}

	targetValidator, err := validators.Validator(targetIndex)
	if err != nil {
		return err
	}

	// Verify source withdrawal credentials
	sourceWithdrawalCredentials, err := sourceValidator.WithdrawalCredentials()
	if err != nil {
		return err
	}

	hasCorrectCredential := sourceWithdrawalCredentials[0] == common.ETH1_ADDRESS_WITHDRAWAL_PREFIX ||
		sourceWithdrawalCredentials[0] == COMPOUNDING_WITHDRAWAL_PREFIX
	isCorrectSourceAddress := bytes.Equal(sourceWithdrawalCredentials[12:], consolidationRequest.SourceAddress[:])

	if !hasCorrectCredential || !isCorrectSourceAddress {
		return nil
	}

	// Verify target has compounding withdrawal credentials
	targetWithdrawalCredentials, err := targetValidator.WithdrawalCredentials()
	if err != nil {
		return err
	}

	if targetWithdrawalCredentials[0] != COMPOUNDING_WITHDRAWAL_PREFIX {
		return nil
	}

	// Verify validators are active
	slot, err := state.Slot()
	if err != nil {
		return err
	}
	currentEpoch := spec.SlotToEpoch(slot)

	sourceActive, err := phase0.IsActive(sourceValidator, currentEpoch)
	if err != nil {
		return err
	}
	targetActive, err := phase0.IsActive(targetValidator, currentEpoch)
	if err != nil {
		return err
	}
	if !sourceActive || !targetActive {
		return nil
	}

	// Verify exits have not been initiated
	sourceExitEpoch, err := sourceValidator.ExitEpoch()
	if err != nil {
		return err
	}
	targetExitEpoch, err := targetValidator.ExitEpoch()
	if err != nil {
		return err
	}

	if sourceExitEpoch != common.FAR_FUTURE_EPOCH || targetExitEpoch != common.FAR_FUTURE_EPOCH {
		return nil
	}

	// Verify source has been active long enough
	sourceActivationEpoch, err := sourceValidator.ActivationEpoch()
	if err != nil {
		return err
	}

	if currentEpoch < sourceActivationEpoch+spec.SHARD_COMMITTEE_PERIOD {
		return nil
	}

	// Verify source has no pending withdrawals
	sourcePendingBalance, err := get_pending_balance_to_withdraw(state, sourceIndex)
	if err != nil {
		return err
	}

	if sourcePendingBalance > 0 {
		return nil
	}

	// Get source effective balance
	sourceEffectiveBalance, err := sourceValidator.EffectiveBalance()
	if err != nil {
		return err
	}

	// Initiate source validator exit
	exitEpoch, err := ComputeConsolidationEpochAndUpdateChurn(ctx, spec, state, sourceEffectiveBalance)
	if err != nil {
		return err
	}

	if err := sourceValidator.SetExitEpoch(exitEpoch); err != nil {
		return err
	}

	withdrawableEpoch := exitEpoch + spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY
	if withdrawableEpoch < exitEpoch { // Check for overflow
		return fmt.Errorf("withdrawable epoch overflow: %d + %d = %d", exitEpoch, spec.MIN_VALIDATOR_WITHDRAWABILITY_DELAY, withdrawableEpoch)
	}
	if err := sourceValidator.SetWithdrawableEpoch(withdrawableEpoch); err != nil {
		return err
	}

	// Add to pending consolidations
	pendingConsolidation := common.PendingConsolidation{
		SourceIndex: sourceIndex,
		TargetIndex: targetIndex,
	}

	return state.AppendPendingConsolidation(pendingConsolidation)
}


// IsValidSwitchToCompoundingRequest checks if a consolidation request is a valid switch to compounding
// SwitchToCompoundingValidator switches a validator to compounding withdrawal credentials
func SwitchToCompoundingValidator(ctx context.Context, spec *common.Spec, state *BeaconStateView, validatorIndex common.ValidatorIndex) error {
	validators, err := state.Validators()
	if err != nil {
		return err
	}

	validator, err := validators.Validator(validatorIndex)
	if err != nil {
		return err
	}

	// Get current withdrawal credentials
	withdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		return err
	}

	// Update withdrawal credentials to compounding prefix
	var newWithdrawalCredentials common.Hash32
	newWithdrawalCredentials[0] = COMPOUNDING_WITHDRAWAL_PREFIX
	copy(newWithdrawalCredentials[1:], withdrawalCredentials[1:])

	if err := validator.SetWithdrawalCredentials(newWithdrawalCredentials); err != nil {
		return err
	}

	// Queue excess balance if any
	balances, err := state.Balances()
	if err != nil {
		return err
	}

	balance, err := balances.GetBalance(validatorIndex)
	if err != nil {
		return err
	}

	if balance > spec.MIN_ACTIVATION_BALANCE {
		excessBalance := balance - spec.MIN_ACTIVATION_BALANCE
		
		// Set balance to MIN_ACTIVATION_BALANCE
		if err := balances.SetBalance(validatorIndex, spec.MIN_ACTIVATION_BALANCE); err != nil {
			return err
		}

		// Queue excess balance as pending deposit
		pendingDeposit := common.PendingDeposit{
			Pubkey: common.BLSPubkey{}, // Will be set below
			WithdrawalCredentials: newWithdrawalCredentials,
			Amount: excessBalance,
			Signature: common.BLSSignature{}, // All zeros for test compatibility
			Slot: common.GENESIS_SLOT,
		}

		// Get validator pubkey
		pubkey, err := validator.Pubkey()
		if err != nil {
			return err
		}
		pendingDeposit.Pubkey = pubkey

		// Append pending deposit
		if err := state.AppendPendingDeposit(pendingDeposit); err != nil {
			return err
		}
	}

	return nil
}

func IsValidSwitchToCompoundingRequest(spec *common.Spec, state common.BeaconState, consolidationRequest *common.ConsolidationRequest) bool {
	// Switch to compounding requires source and target be equal
	if consolidationRequest.SourcePubkey != consolidationRequest.TargetPubkey {
		return false
	}
	
	// Find validator
	validators, err := state.Validators()
	if err != nil {
		return false
	}
	
	validatorIndex := common.ValidatorIndexMarker
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return false
	}
	
	for i := common.ValidatorIndex(0); uint64(i) < validatorCount; i++ {
		val, err := validators.Validator(i)
		if err != nil {
			return false
		}
		valPubkey, err := val.Pubkey()
		if err != nil {
			return false
		}
		if valPubkey == consolidationRequest.SourcePubkey {
			validatorIndex = i
			break
		}
	}
	
	if validatorIndex == common.ValidatorIndexMarker {
		return false
	}
	
	validator, err := validators.Validator(validatorIndex)
	if err != nil {
		return false
	}
	
	// Verify request has been authorized
	withdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		return false
	}
	
	if !bytes.Equal(withdrawalCredentials[12:], consolidationRequest.SourceAddress[:]) {
		return false
	}
	
	// Verify source has ETH1 withdrawal credentials
	if withdrawalCredentials[0] != common.ETH1_ADDRESS_WITHDRAWAL_PREFIX {
		return false
	}
	
	// Verify validator is active
	slot, err := state.Slot()
	if err != nil {
		return false
	}
	currentEpoch := spec.SlotToEpoch(slot)
	
	isActive, err := phase0.IsActive(validator, currentEpoch)
	if err != nil {
		return false
	}
	if !isActive {
		return false
	}
	
	// Verify exit has not been initiated
	exitEpoch, err := validator.ExitEpoch()
	if err != nil {
		return false
	}
	
	return exitEpoch == common.FAR_FUTURE_EPOCH
}


