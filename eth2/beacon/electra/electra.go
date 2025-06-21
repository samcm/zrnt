package electra

// Constants from consensus-specs/specs/electra/beacon-chain.md

// Misc
const (
	// UNSET_DEPOSIT_REQUESTS_START_INDEX indicates no start index has been assigned
	UNSET_DEPOSIT_REQUESTS_START_INDEX uint64 = ^uint64(0) // 2**64 - 1
	
	// FULL_EXIT_REQUEST_AMOUNT is the withdrawal amount used to signal a full validator exit
	FULL_EXIT_REQUEST_AMOUNT uint64 = 0
)

// Withdrawal prefixes
const (
	// COMPOUNDING_WITHDRAWAL_PREFIX is the withdrawal credential prefix for a compounding validator
	COMPOUNDING_WITHDRAWAL_PREFIX byte = 0x02
)

// Execution layer triggered requests
const (
	DEPOSIT_REQUEST_TYPE       byte = 0x00
	WITHDRAWAL_REQUEST_TYPE    byte = 0x01
	CONSOLIDATION_REQUEST_TYPE byte = 0x02
)

// Preset values are defined in the common.Spec struct
// These constants are provided here for reference only

// Additional Electra-specific constants
const (
	// get_committee_indices helper
	MAX_COMMITTEES_PER_SLOT_ELECTRA = 64
)