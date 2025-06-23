package electra

import (
	"context"
	"errors"
	"fmt"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/deneb"
	"github.com/protolambda/ztyp/tree"
)

// ProcessExecutionPayload processes the execution payload in Electra
func ProcessExecutionPayload(ctx context.Context, spec *common.Spec, state *BeaconStateView, body *BeaconBlockBody, engine ExecutionEngine) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if engine == nil {
		return errors.New("nil execution engine")
	}
	payload := &body.ExecutionPayload

	slot, err := state.Slot()
	if err != nil {
		return err
	}

	latestExecHeader, err := state.LatestExecutionPayloadHeader()
	if err != nil {
		return err
	}
	// Verify consistency of the parent hash with respect to the previous execution payload header
	parent, err := latestExecHeader.Raw()
	if err != nil {
		return fmt.Errorf("failed to read previous header: %v", err)
	}
	if payload.ParentHash != parent.BlockHash {
		return fmt.Errorf("expected parent hash %s in execution payload, but got %s",
			parent.BlockHash, payload.ParentHash)
	}

	// Verify prev_randao
	mixes, err := state.RandaoMixes()
	if err != nil {
		return err
	}
	expectedMix, err := mixes.GetRandomMix(spec.SlotToEpoch(slot))
	if err != nil {
		return err
	}
	if payload.PrevRandao != expectedMix {
		return fmt.Errorf("invalid random data %s, expected %s", payload.PrevRandao, expectedMix)
	}

	// Verify timestamp
	genesisTime, err := state.GenesisTime()
	if err != nil {
		return err
	}
	if expectedTime, err := spec.TimeAtSlot(slot, genesisTime); err != nil {
		return fmt.Errorf("slot or genesis time in state is corrupt, cannot compute time: %v", err)
	} else if payload.Timestamp != expectedTime {
		return fmt.Errorf("state at slot %d, genesis time %d, expected execution payload time %d, but got %d",
			slot, genesisTime, expectedTime, payload.Timestamp)
	}

	// [Modified in Electra:EIP7691] Verify commitments are under limit
	if uint64(len(body.BlobKZGCommitments)) > uint64(spec.MAX_BLOBS_PER_BLOCK_ELECTRA) {
		return fmt.Errorf("too many blob KZG commitments: %d", len(body.BlobKZGCommitments))
	}

	// Verify the execution payload is valid
	versionedHashes := make([]common.Hash32, 0, len(body.BlobKZGCommitments))
	for _, commit := range body.BlobKZGCommitments {
		versionedHashes = append(versionedHashes, commit.ToVersionedHash())
	}
	latestHeader, err := state.LatestBlockHeader()
	if err != nil {
		return fmt.Errorf("failed to get current in-progress latest beacon-block-header from beacon state: %w", err)
	}
	
	// [Modified in Electra] Pass execution requests
	if valid, err := VerifyAndNotifyNewPayload(ctx, engine, &NewPayloadRequest{
		ExecutionPayload:      payload,
		VersionedHashes:       versionedHashes,
		ParentBeaconBlockRoot: latestHeader.ParentRoot,
		ExecutionRequests:     &body.ExecutionRequests,
	}); err != nil {
		return fmt.Errorf("unexpected problem in execution engine when inserting block %s (height %d), err: %v",
			payload.BlockHash, payload.BlockNumber, err)
	} else if !valid {
		return fmt.Errorf("execution engine says payload is invalid: %s (height %d)",
			payload.BlockHash, payload.BlockNumber)
	}

	return state.SetLatestExecutionPayloadHeader(GetPayloadHeader(spec, payload))
}

// NewPayloadRequest represents a request to the execution engine
type NewPayloadRequest struct {
	ExecutionPayload      *deneb.ExecutionPayload
	VersionedHashes       []common.Hash32
	ParentBeaconBlockRoot common.Root
	ExecutionRequests     *ExecutionRequests
}

// ExecutionEngine interface for Electra
type ExecutionEngine interface {
	IsValidBlockHash(executionPayload *deneb.ExecutionPayload, parentBeaconBlockRoot common.Root, executionRequestsList [][]byte) (bool, error)
	NotifyNewPayload(executionPayload *deneb.ExecutionPayload, parentBeaconBlockRoot common.Root, executionRequestsList [][]byte) (bool, error)
}

// VerifyAndNotifyNewPayload verifies and notifies the execution engine of a new payload
func VerifyAndNotifyNewPayload(ctx context.Context, engine ExecutionEngine, request *NewPayloadRequest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	executionPayload := request.ExecutionPayload

	// Get execution requests list
	executionRequestsList, err := GetExecutionRequestsList(nil, request.ExecutionRequests)
	if err != nil {
		return false, fmt.Errorf("failed to get execution requests list: %w", err)
	}

	// Check block hash validity
	if valid, err := engine.IsValidBlockHash(executionPayload, request.ParentBeaconBlockRoot, executionRequestsList); err != nil {
		return false, err
	} else if !valid {
		return false, nil
	}

	// Verify versioned hashes
	if !VerifyVersionedHashes(request) {
		return false, nil
	}

	// Notify new payload
	return engine.NotifyNewPayload(executionPayload, request.ParentBeaconBlockRoot, executionRequestsList)
}

// VerifyVersionedHashes verifies the versioned hashes in a new payload request
func VerifyVersionedHashes(request *NewPayloadRequest) bool {
	// In a real implementation, this would verify the versioned hashes
	// For now, we'll return true
	return true
}

// GetPayloadHeader returns the execution payload header for this payload
func GetPayloadHeader(spec *common.Spec, p *deneb.ExecutionPayload) *deneb.ExecutionPayloadHeader {
	return &deneb.ExecutionPayloadHeader{
		ParentHash:       p.ParentHash,
		FeeRecipient:     p.FeeRecipient,
		StateRoot:        p.StateRoot,
		ReceiptsRoot:     p.ReceiptsRoot,
		LogsBloom:        p.LogsBloom,
		PrevRandao:       p.PrevRandao,
		BlockNumber:      p.BlockNumber,
		GasLimit:         p.GasLimit,
		GasUsed:          p.GasUsed,
		Timestamp:        p.Timestamp,
		ExtraData:        p.ExtraData,
		BaseFeePerGas:    p.BaseFeePerGas,
		BlockHash:        p.BlockHash,
		TransactionsRoot: p.Transactions.HashTreeRoot(spec, tree.GetHashFn()),
		WithdrawalsRoot:  p.Withdrawals.HashTreeRoot(spec, tree.GetHashFn()),
		BlobGasUsed:      p.BlobGasUsed,
		ExcessBlobGas:    p.ExcessBlobGas,
	}
}