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

// CalculatePVControl executes the complete hysteresis logic and returns the data to main.
func CalculatePVControl(status *SafeStatus, lastWriteTime *time.Time, lastCarState *int) ControlResult {
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

	// BACK TO THE WORKING FORMULA:
	// Calculate mathematical target power footprint based ON SETTINGS (Amps * Phases * 230V)
	// This is exactly what worked perfectly before!
	result.CalculatedWbPower = float64(result.CurrentAmperage) * float64(result.ActivePhases) * variables.NominalVoltage
	result.GridSurplus = variables.AvailableSurplusW

	// Total available solar power combines the grid snapshot and what the wallbox is CURRENTLY holding
	result.PotentialSolarTotal = result.GridSurplus + result.CalculatedWbPower
	result.PredictedLeftoverSurplusW = result.GridSurplus

	stateChanged := false
	if *lastCarState != carState {
		stateChanged = true
		*lastCarState = carState
	}

	// If Force Max Mode is active, bypass solar tracking entirely
	if variables.ChargeMode == "max" {
		result.TargetAmperage = variables.MaxAmperage
		if currentAmp != variables.MaxAmperage {
			result.ShouldWriteToWallbox = true
			result.IsStatusOverride = true
		}
		return result
	}

	// Only enforce hard state freezes if configured to lock when unplugged
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
	// Universal Solar Hysteresis Logic (Home Assistant Port)
	// ------------------------------------------------------------------------
	wattPerAmpStep := variables.NominalVoltage * float64(result.ActivePhases)

	// Step thresholds using the stable mathematical pool
	zielAmpsHoch := int((result.PotentialSolarTotal - 400.0) / wattPerAmpStep)
	zielAmpsRunter := int((result.PotentialSolarTotal + 400.0) / wattPerAmpStep)

	newAmp := currentAmp

	if zielAmpsHoch > currentAmp {
		newAmp = zielAmpsHoch
		if newAmp > variables.MaxAmperage {
			newAmp = variables.MaxAmperage
		}
		if newAmp < variables.MinAmperage {
			newAmp = variables.MinAmperage
		}
		powerToSpend := float64(newAmp-currentAmp) * wattPerAmpStep
		result.PredictedLeftoverSurplusW = result.GridSurplus - powerToSpend

	} else if zielAmpsRunter < currentAmp {
		newAmp = zielAmpsRunter
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

	// Evaluate write throttling
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
