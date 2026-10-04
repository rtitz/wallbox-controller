package utils

import (
	"math"
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
	TimerStatus              string
	NextStepUpWatts          float64
	NextStepDownWatts        float64
	CalculatedHouseLoadWatts float64 // NEW: Real-time dynamic house baseload
}

// CalculatePVControl executes the complete hysteresis logic based on true physical loads.
func CalculatePVControl(status *SafeStatus, lastWriteTime *time.Time, lastCarState *int, boostStartTime *time.Time, recoveryStartTime *time.Time) ControlResult {
	var result ControlResult

	status.Mu.RLock()
	currentAmp := status.Data.Amp
	isOnePhase := status.Data.Fsp
	carState := status.Data.Car

	// Real live power currently converted by the car hardware (Index 11 in Deciwatt)
	realWbPowerMeasured := 0.0
	if len(status.Data.Nrg) > 11 {
		realWbPowerMeasured = status.Data.Nrg[11] / 10.0
	}
	status.Mu.RUnlock()

	// Soft fallbacks
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

	// UNFEHLBAR: Real House Load = Total House Consumption (Prometheus) - Active Wallbox Power
	// If Prometheus is fetching stale data at night, the formula naturally balances via the wallbox payload.
	result.CalculatedHouseLoadWatts = math.Max(0, variables.LiveTotalHouseConsumptionW-realWbPowerMeasured)

	// True Household Solar Potential = What the solar array generates minus what the house currently burns
	result.PotentialSolarTotal = variables.LiveSolarProductionW - result.CalculatedHouseLoadWatts
	result.PredictedLeftoverSurplusW = result.GridSurplus

	// Calculate dynamic next step thresholds
	wattPerAmpStep := variables.NominalVoltage * float64(result.ActivePhases)
	hysteresisBuffer := float64(variables.SolarHysteresisBandW)

	result.NextStepUpWatts = (float64(currentAmp+1) * wattPerAmpStep) + hysteresisBuffer
	result.NextStepDownWatts = (float64(currentAmp) * wattPerAmpStep) - hysteresisBuffer
	result.TimerStatus = "Idle"

	stateChanged := false
	if *lastCarState != carState {
		stateChanged = true
		*lastCarState = carState
	}

	// Mode 1: Force Max Mode is globally active via Web UI
	if variables.ChargeMode == "max" {
		*boostStartTime = time.Time{}
		*recoveryStartTime = time.Time{}
		result.TargetAmperage = variables.MaxAmperage
		if currentAmp != variables.MaxAmperage {
			result.ShouldWriteToWallbox = true
			result.IsStatusOverride = true
		}
		return result
	}

	// Hard connection boundaries
	if variables.SetWallboxOnlyIfCarConnected {
		if carState == 1 {
			*boostStartTime = time.Time{}
			*recoveryStartTime = time.Time{}
			result.TargetAmperage = variables.MinAmperage
			if currentAmp != variables.MinAmperage {
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
			}
			return result
		} else if carState == 4 || carState == 3 {
			*boostStartTime = time.Time{}
			*recoveryStartTime = time.Time{}
			result.TargetAmperage = variables.MaxAmperage
			if currentAmp != variables.MaxAmperage {
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
			}
			return result
		}
	}

	// ------------------------------------------------------------------------
	// Efficiency Boost Engine (Sustained Grid Import Management)
	// ------------------------------------------------------------------------
	requiredDelay := time.Duration(variables.GridThresholdDelaySec) * time.Second
	convertedThreshold := float64(variables.GridThresholdW)

	calculatedTargetAmp := currentAmp

	if carState == 2 {
		// CONDITION A: High Total Grid Import -> Trigger Boost
		// Only tracks if it is pitch black night (PV Gen == 0) and the grid deficit is heavy.
		if result.GridSurplus < -convertedThreshold && variables.LiveSolarProductionW == 0.0 {
			*recoveryStartTime = time.Time{}

			if currentAmp == variables.MaxAmperage {
				*boostStartTime = time.Time{}
			} else {
				if boostStartTime.IsZero() {
					*boostStartTime = time.Now()
				}
				if time.Since(*boostStartTime) >= requiredDelay {
					calculatedTargetAmp = variables.MaxAmperage
					if currentAmp != variables.MaxAmperage {
						result.ShouldWriteToWallbox = true
						result.IsStatusOverride = true
					}
				}
			}
		} else if currentAmp == variables.MaxAmperage && result.PotentialSolarTotal >= (float64(variables.MinAmperage)*float64(result.ActivePhases)*variables.NominalVoltage)+hysteresisBuffer {
			// CONDITION B: Genuine Solar Return -> Fall back down after sustained delay
			*boostStartTime = time.Time{}

			if recoveryStartTime.IsZero() {
				*recoveryStartTime = time.Now()
			}

			if time.Since(*recoveryStartTime) >= requiredDelay {
				*recoveryStartTime = time.Time{}
				calculatedTargetAmp = variables.MinAmperage
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
			} else {
				calculatedTargetAmp = variables.MaxAmperage
			}
		} else {
			if result.GridSurplus >= 0 || variables.LiveSolarProductionW > 0.0 {
				*boostStartTime = time.Time{}
			}
			if result.PotentialSolarTotal < -convertedThreshold {
				*recoveryStartTime = time.Time{}
			}
		}

		// CRITICAL LOCK: Hold 16 A firmly ONLY if it's pitch black night.
		if currentAmp == variables.MaxAmperage && variables.LiveSolarProductionW == 0.0 {
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
	if calculatedTargetAmp == currentAmp && carState == 2 {
		targetAmpsStepUp := int((result.PotentialSolarTotal - hysteresisBuffer) / wattPerAmpStep)
		targetAmpsStepDown := int((result.PotentialSolarTotal + hysteresisBuffer) / wattPerAmpStep)

		if targetAmpsStepUp > currentAmp {
			calculatedTargetAmp = targetAmpsStepUp
			if calculatedTargetAmp > variables.MaxAmperage {
				calculatedTargetAmp = variables.MaxAmperage
			}
			powerToSpend := float64(calculatedTargetAmp-currentAmp) * wattPerAmpStep
			result.PredictedLeftoverSurplusW = result.GridSurplus - powerToSpend

		} else if targetAmpsStepDown < currentAmp {
			calculatedTargetAmp = targetAmpsStepDown
			if calculatedTargetAmp < variables.MinAmperage {
				calculatedTargetAmp = variables.MinAmperage
			}
			powerSaved := float64(currentAmp-calculatedTargetAmp) * wattPerAmpStep
			result.PredictedLeftoverSurplusW = result.GridSurplus + powerSaved
		}
	}

	result.TargetAmperage = calculatedTargetAmp

	if calculatedTargetAmp != currentAmp {
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
