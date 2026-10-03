package utils

import (
	"time"
	"wallbox-controller/variables"
)

// ControlResult holds the computed metrics to be printed and acted upon in main
type ControlResult struct {
	ActivePhases              int
	CurrentAmperage           int
	CalculatedWbPower         float64
	GridSurplus               float64
	PotentialSolarTotal       float64
	TargetAmperage            int
	PredictedLeftoverSurplusW float64
	ShouldWriteToWallbox      bool
	ThrottledRemainingSec     float64
	IsStatusOverride          bool
}

// CalculatePVControl executes the complete charging logic and returns the data to main.
func CalculatePVControl(status *SafeStatus, lastWriteTime *time.Time, lastCarState *int, boostStartTime *time.Time, recoveryStartTime *time.Time) ControlResult {
	var result ControlResult

	status.Mu.RLock()
	currentAmp := status.Data.Amp
	isOnePhase := status.Data.Fsp
	carState := status.Data.Car
	status.Mu.RUnlock()

	// Soft fallbacks for uninitialized background states
	if currentAmp == 0 {
		currentAmp = variables.MinAmperage
	}
	if carState == 0 {
		carState = 1
	}

	if currentAmp < variables.MinAmperage || currentAmp > variables.MaxAmperage {
		currentAmp = variables.MinAmperage
	}

	result.CurrentAmperage = currentAmp
	result.ActivePhases = 3
	if isOnePhase {
		result.ActivePhases = 1
	}

	result.CalculatedWbPower = float64(result.CurrentAmperage) * float64(result.ActivePhases) * variables.NominalVoltage
	result.GridSurplus = variables.AvailableSurplusW
	result.PotentialSolarTotal = result.GridSurplus + result.CalculatedWbPower
	result.PredictedLeftoverSurplusW = result.GridSurplus

	stateChanged := false
	if *lastCarState != carState {
		stateChanged = true
		*lastCarState = carState
	}

	// Mode 1: Force Max Mode is globally active via Web UI
	if variables.ChargeMode == "max" {
		result.TargetAmperage = variables.MaxAmperage
		if currentAmp != variables.MaxAmperage {
			result.ShouldWriteToWallbox = true
			result.IsStatusOverride = true
		}
		return result
	}

	// Connection boundaries check
	if variables.SetWallboxOnlyIfCarConnected {
		if carState == 1 {
			result.TargetAmperage = variables.MinAmperage
			if currentAmp != variables.MinAmperage {
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
			}
			return result
		} else if carState == 3 || carState == 4 {
			result.TargetAmperage = variables.MaxAmperage
			if currentAmp != variables.MaxAmperage {
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
			}
			return result
		}
	}

	// ------------------------------------------------------------------------
	// Efficiency Boost Engine (Sustained Grid Import / Export Management)
	// ------------------------------------------------------------------------
	requiredDelay := time.Duration(variables.GridThresholdDelaySec) * time.Second
	convertedThreshold := float64(variables.GridThresholdW)

	if carState == 2 {
		// CONDITION A: High Grid Import -> Trigger Maximum Efficiency Boost after sustained delay
		if currentAmp == variables.MinAmperage && result.GridSurplus < -convertedThreshold {
			*recoveryStartTime = time.Time{}

			if boostStartTime.IsZero() {
				*boostStartTime = time.Now()
			}

			if time.Since(*boostStartTime) >= requiredDelay {
				result.TargetAmperage = variables.MaxAmperage
				result.PredictedLeftoverSurplusW = result.GridSurplus - (float64(variables.MaxAmperage-variables.MinAmperage) * variables.NominalVoltage * float64(result.ActivePhases))
				if currentAmp != variables.MaxAmperage {
					result.ShouldWriteToWallbox = true
					result.IsStatusOverride = true
				}
				return result
			}
		} else if currentAmp == variables.MaxAmperage && result.GridSurplus >= 0 {
			// CONDITION B: Clear Solar Return -> Fall back down to Solar Mode after sustained delay
			*boostStartTime = time.Time{}

			if recoveryStartTime.IsZero() {
				*recoveryStartTime = time.Now()
			}

			if time.Since(*recoveryStartTime) >= requiredDelay {
				// Safety delay window fully elapsed! Reset timer and force target down to MinAmperage
				// so the standard solar hysteresis loop can safely take over in the next frame
				*recoveryStartTime = time.Time{}
				result.TargetAmperage = variables.MinAmperage
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
				return result
			} else {
				// Hold maximum amperage limit locked safely until the recovery delay elapses completely
				result.TargetAmperage = variables.MaxAmperage
				return result
			}
		} else {
			if result.GridSurplus >= 0 {
				*boostStartTime = time.Time{}
			}
			if result.GridSurplus < -convertedThreshold {
				*recoveryStartTime = time.Time{}
			}
		}

		// CRITICAL LOCK: If we have successfully boosted to MaxAmperage, but there is still net grid import (GridSurplus < 0),
		// we FORCE the system to hold 16 A. This completely blocks the standard solar hysteresis loop below from stepping down!
		if currentAmp == variables.MaxAmperage && result.GridSurplus < 0 {
			result.TargetAmperage = variables.MaxAmperage
			return result
		}
	} else {
		*boostStartTime = time.Time{}
		*recoveryStartTime = time.Time{}
	}

	// ------------------------------------------------------------------------
	// Standard Universal Solar Hysteresis Logic
	// ------------------------------------------------------------------------
	wattPerAmpStep := variables.NominalVoltage * float64(result.ActivePhases)
	convertedBuffer := float64(variables.HysteresisBufferW)

	targetAmpsStepUp := int((result.PotentialSolarTotal - convertedBuffer) / wattPerAmpStep)
	targetAmpsStepDown := int((result.PotentialSolarTotal + convertedBuffer) / wattPerAmpStep)

	newAmp := currentAmp

	if targetAmpsStepUp > currentAmp {
		newAmp = targetAmpsStepUp
		if newAmp > variables.MaxAmperage {
			newAmp = variables.MaxAmperage
		}
		if newAmp < variables.MinAmperage {
			newAmp = variables.MinAmperage
		}
		powerToSpend := float64(newAmp-currentAmp) * wattPerAmpStep
		result.PredictedLeftoverSurplusW = result.GridSurplus - powerToSpend

	} else if targetAmpsStepDown < currentAmp {
		newAmp = targetAmpsStepDown
		if newAmp > variables.MaxAmperage {
			newAmp = variables.MaxAmperage
		}
		if newAmp < variables.MinAmperage {
			newAmp = variables.MinAmperage
		}
		powerSaved := float64(currentAmp-newAmp) * wattPerAmpStep
		result.PredictedLeftoverSurplusW = result.GridSurplus + powerSaved
	}

	result.TargetAmperage = newAmp

	if newAmp != currentAmp {
		cooldownDuration := time.Duration(variables.WallboxWriteCooldownSec) * time.Second
		if time.Since(*lastWriteTime) >= cooldownDuration || stateChanged {
			result.ShouldWriteToWallbox = true
		} else {
			remaining := cooldownDuration - time.Since(*lastWriteTime)
			result.ThrottledRemainingSec = remaining.Seconds()
		}
	}

	return result
}

// WriteSettings handles the API payload transmission
func WriteSettings(ip string, settings map[string]interface{}) error {
	if err := SetChargerValues(ip, settings); err != nil {
		return err
	}
	return nil
}
