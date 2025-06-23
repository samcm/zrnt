package main

import (
	"fmt"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/configs"
)

func main() {
	spec := configs.Minimal
	
	fmt.Printf("MIN_PER_EPOCH_CHURN_LIMIT_ELECTRA: %d\n", spec.MIN_PER_EPOCH_CHURN_LIMIT_ELECTRA)
	fmt.Printf("MAX_PER_EPOCH_ACTIVATION_EXIT_CHURN_LIMIT: %d\n", spec.MAX_PER_EPOCH_ACTIVATION_EXIT_CHURN_LIMIT)
	fmt.Printf("CHURN_LIMIT_QUOTIENT: %d\n", spec.CHURN_LIMIT_QUOTIENT)
	fmt.Printf("EFFECTIVE_BALANCE_INCREMENT: %d\n", spec.EFFECTIVE_BALANCE_INCREMENT)
	
	// Expected value from test
	expected := common.Gwei(0xee6b28000)
	fmt.Printf("\nExpected DepositBalanceToConsume: %d (%d ETH)\n", expected, expected/1e9)
}