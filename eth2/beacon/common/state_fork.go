package common

import "reflect"

// IsElectraState returns true if the state is an Electra state
func IsElectraState(state BeaconState) bool {
	// Use reflection to check the concrete type
	// This is not ideal but avoids circular imports
	stateName := reflect.TypeOf(state).String()
	return stateName == "*electra.BeaconStateView"
}