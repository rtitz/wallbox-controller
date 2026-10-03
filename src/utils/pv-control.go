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

	// Telemetry Fields for Advanced System Visibility
	TimerStatus       string
	NextStepUpWatts   float64
	NextStepDownWatts float64
}

// CalculatePVControl executes the complete hysteresis logic and returns the data to main.
func CalculatePVControl(status *SafeStatus, lastWriteTime *time.Time, lastCarState *int, boostStartTime *time.Time, recoveryStartTime *time.Time) ControlResult {
	var result ControlResult

	status.Mu.RLock()
	currentAmp := status.Data.Amp
	isOnePhase := status.Data.Fsp
	carState := status.Data.Car
	status.Mu.RUnlock()

	// Soft fallbacks for uninitialized background network states
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

	// Calculate mathematical target power footprint based on settings (Amps * Phases * 230V)
	result.CalculatedWbPower = float64(result.CurrentAmperage) * float64(result.ActivePhases) * variables.NominalVoltage
	result.GridSurplus = variables.AvailableSurplusW

	// Total available solar power combines the grid snapshot and what the wallbox is currently holding
	result.PotentialSolarTotal = result.GridSurplus + result.CalculatedWbPower
	result.PredictedLeftoverSurplusW = result.GridSurplus

	stateChanged := false
	if *lastCarState != carState {
		stateChanged = true
		*lastCarState = carState
	}

	// Calculate dynamic next step thresholds for human auditing
	wattPerAmpStep := variables.NominalVoltage * float64(result.ActivePhases)
	hysteresisBuffer := float64(variables.SolarHysteresisBandW)

	result.NextStepUpWatts = (float64(currentAmp+1) * wattPerAmpStep) + hysteresisBuffer
	result.NextStepDownWatts = (float64(currentAmp) * wattPerAmpStep) - hysteresisBuffer
	result.TimerStatus = "Idle"

	// Mode 1: Force Max Mode is globally active via Web UI
	if variables.ChargeMode == "max" {
		result.TargetAmperage = variables.MaxAmperage
		if currentAmp != variables.MaxAmperage {
			result.ShouldWriteToWallbox = true
			result.IsStatusOverride = true
		}
		return result
	}

	// Hard connection boundaries and pre-conditioning recovery checks
	if variables.SetWallboxOnlyIfCarConnected {
		if carState == 1 {
			result.TargetAmperage = variables.MinAmperage
			if currentAmp != variables.MinAmperage {
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
			}
			return result
		} else if carState == 4 || carState == 3 {
			// Automatically force 11 kW if charge finished (for morning heating) or waiting
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
		// CONDITION A: High Total Grid Import -> Trigger 11 kW Boost after sustained delay
		// If the entire house (including wallbox) draws more than 1500W from the grid, the timer starts.
		if result.GridSurplus < -convertedThreshold {
			*recoveryStartTime = time.Time{}

			if boostStartTime.IsZero() {
				*boostStartTime = time.Now()
			}

			if time.Since(*boostStartTime) >= requiredDelay {
				result.TargetAmperage = variables.MaxAmperage
				result.PredictedLeftoverSurplusW = result.GridSurplus - (float64(variables.MaxAmperage-variables.MinAmperage) * variables.NominalVoltage * float64(result.ActivePhases))
				if currentAmp != variables.MaxAmperage {
					result.ShouldWriteToWallbox = true
					result.IsStatusOverride = true // Bypass standard timers for immediate acceleration
				}
				return result
			}
		} else if currentAmp == variables.MaxAmperage && result.PotentialSolarTotal >= (float64(variables.MinAmperage)*float64(result.ActivePhases)*variables.NominalVoltage)+hysteresisBuffer {
			// CONDITION B: Genuine Solar Return -> Fall back down after sustained delay
			*boostStartTime = time.Time{}

			if recoveryStartTime.IsZero() {
				*recoveryStartTime = time.Now()
			}

			if time.Since(*recoveryStartTime) >= requiredDelay {
				*recoveryStartTime = time.Time{}
				result.TargetAmperage = variables.MinAmperage
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
				return result
			} else {
				result.TargetAmperage = variables.MaxAmperage
				return result
			}
		} else {
			if result.GridSurplus >= 0 {
				*boostStartTime = time.Time{}
			}
			if result.PotentialSolarTotal < -convertedThreshold {
				*recoveryStartTime = time.Time{}
			}
		}

		// CRITICAL LOCK: Hold 16 A firmly until real solar potential returns to positive
		if currentAmp == variables.MaxAmperage && result.PotentialSolarTotal < (float64(variables.MinAmperage)*float64(result.ActivePhases)*variables.NominalVoltage) {
			result.TargetAmperage = variables.MaxAmperage
			return result
		}
	} else {
		*boostStartTime = time.Time{}
		*recoveryStartTime = time.Time{}
	}

	// ------------------------------------------------------------------------
	// Universal Solar Hysteresis Logic (Symmetrical Deadband)
	// ------------------------------------------------------------------------
	targetAmpsStepUp := int((result.PotentialSolarTotal - hysteresisBuffer) / wattPerAmpStep)
	targetAmpsStepDown := int((result.PotentialSolarTotal + hysteresisBuffer) / wattPerAmpStep)

	newAmp := currentAmp

	if targetAmpsStepUp > currentAmp {
		newAmp = targetAmpsStepUp
		if newAmp > variables.MaxAmperage {
			newAmp = variables.MaxAmperage
		}
		powerToSpend := float64(newAmp-currentAmp) * wattPerAmpStep
		result.PredictedLeftoverSurplusW = result.GridSurplus - powerToSpend

	} else if targetAmpsStepDown < currentAmp {
		newAmp = targetAmpsStepDown
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
