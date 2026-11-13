package utils

import (
	"math"
	"time"
	"wallbox-controller/variables"
)

// ControlResult contains the calculated target, write decision, and values used
// to explain that decision in logs and telemetry. It does not write to the wallbox.
type ControlResult struct {
	ActivePhases              int     // 1 or 3, based on the wallbox's single-phase status
	CurrentAmperage           int     // Validated configured amperage read from the wallbox
	CalculatedWbPower         float64 // Estimated charger power: amperage * phases * nominal voltage
	GridSurplus               float64 // Grid-meter surplus; separate from PotentialSolarTotal
	PotentialSolarTotal       float64 // Solar production minus estimated non-wallbox house load
	TargetAmperage            int     // Amperage this calculation recommends
	PredictedLeftoverSurplusW float64 // Grid surplus adjusted for the recommended amperage change
	ShouldWriteToWallbox      bool    // Whether the caller should attempt a wallbox write
	ThrottledRemainingSec     float64 // Cooldown time left when a normal hysteresis change is held
	IsStatusOverride          bool    // Target was selected by a max, connection, boost, or recovery rule

	// Values used by the UI and logs to explain the current hysteresis state.
	TimerStatus              string
	NextStepUpWatts          float64
	NextStepDownWatts        float64
	CalculatedHouseLoadWatts float64 // Estimated house load after subtracting measured wallbox power
}

// CalculatePVControl snapshots wallbox status, calculates an amperage target, and
// updates the caller-owned car-state and boost/recovery timers. The caller owns
// those values so they persist between calculations. This function only reports
// whether a write is needed; the caller performs the network operation.
func CalculatePVControl(status *SafeStatus, lastWriteTime *time.Time, lastCarState *int, boostStartTime *time.Time, recoveryStartTime *time.Time) ControlResult {
	var result ControlResult

	// Take one consistent snapshot of the wallbox fields used below, then release
	// the lock before doing calculations. Nrg[11] is total real-time power in deciwatts.
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

	// Use the configured minimum for missing or out-of-range amperage. Treat an
	// unset car state as unplugged so connection policies cannot assume a car exists.
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
	// Fsp identifies single-phase operation; otherwise this controller assumes three phases.
	result.ActivePhases = 3
	if isOnePhase {
		result.ActivePhases = 1
	}

	// This is an estimate based on the configured amperage, not the measured Nrg power above.
	result.CalculatedWbPower = float64(result.CurrentAmperage) * float64(result.ActivePhases) * variables.NominalVoltage
	// Keep grid-meter surplus as its own metric: the control thresholds below use
	// solar production minus estimated house load, not this grid measurement.
	result.GridSurplus = variables.AvailableSurplusW

	// The house-load metric is assumed to include the wallbox. Subtract its measured
	// power to estimate the other household load, and floor the estimate at zero.
	result.CalculatedHouseLoadWatts = math.Max(0, variables.LiveTotalHouseConsumptionW-realWbPowerMeasured)

	// Net solar potential is the production left after serving the estimated house load.
	result.PotentialSolarTotal = variables.LiveSolarProductionW - result.CalculatedHouseLoadWatts
	result.PredictedLeftoverSurplusW = result.GridSurplus

	// One additional amp is estimated to use nominal voltage on every active phase.
	// These thresholds include the symmetric hysteresis band shown to the user.
	wattPerAmpStep := variables.NominalVoltage * float64(result.ActivePhases)
	hysteresisBuffer := float64(variables.SolarHysteresisBandW)

	result.NextStepUpWatts = (float64(currentAmp+1) * wattPerAmpStep) + hysteresisBuffer
	result.NextStepDownWatts = (float64(currentAmp) * wattPerAmpStep) - hysteresisBuffer
	result.TimerStatus = "Idle"

	// Remember connection transitions. A transition bypasses the normal write
	// cooldown later, allowing a changed car state to be acted on immediately.
	stateChanged := false
	if *lastCarState != carState {
		stateChanged = true
		*lastCarState = carState
	}

	// Max mode takes precedence over every solar and connection rule. Clear pending
	// timers so they cannot resume later with stale elapsed time.
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

	// When enabled, apply connection policy before solar control: unplugged cars
	// are returned to minimum, while waiting/finished cars hold their setting or
	// are set to maximum when SetMaxAmperageWhenConnectedAndNotCharging is enabled.
	if variables.SetWallboxOnlyIfCarConnected {
		switch carState {
		case 1: // 1 = unplugged
			*boostStartTime = time.Time{}
			*recoveryStartTime = time.Time{}
			result.TargetAmperage = variables.MinAmperage
			if currentAmp != variables.MinAmperage {
				result.ShouldWriteToWallbox = true
				result.IsStatusOverride = true
			}
			return result
		case 4, 3: // 3 = connected but waiting, 4 = charge finished
			*boostStartTime = time.Time{}
			*recoveryStartTime = time.Time{}
			result.TargetAmperage = currentAmp
			if variables.SetMaxAmperageWhenConnectedAndNotCharging {
				result.TargetAmperage = variables.MaxAmperage
				if currentAmp != variables.MaxAmperage {
					result.ShouldWriteToWallbox = true
					result.IsStatusOverride = true
				}
			}
			return result
		}
	}

	// ------------------------------------------------------------------------
	// Boost and solar-recovery timers. Despite the GridThreshold names, this logic
	// does not read GridThresholdW or GridSurplus; it uses solar production and
	// PotentialSolarTotal directly.
	// ------------------------------------------------------------------------
	requiredDelay := time.Duration(variables.GridThresholdDelaySec) * time.Second
	calculatedTargetAmp := currentAmp

	if carState == 2 { // 2 = charging
		// Boost only after solar production is exactly zero for the configured delay.
		// Any pending recovery is canceled while this condition is active.
		if variables.LiveSolarProductionW == 0.0 {
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
			// Recovery starts only from maximum amperage and only when net solar can
			// cover minimum charging power plus the hysteresis band. Hold maximum
			// until that condition has remained true for the full delay.
			*boostStartTime = time.Time{}

			if recoveryStartTime.IsZero() {
				*recoveryStartTime = time.Now()
			}

			if time.Since(*recoveryStartTime) >= requiredDelay {
				*recoveryStartTime = time.Time{}
				calculatedTargetAmp = variables.MinAmperage

				if currentAmp != variables.MinAmperage {
					result.ShouldWriteToWallbox = true
					result.IsStatusOverride = true
				}
			} else {
				calculatedTargetAmp = variables.MaxAmperage
			}
		} else {
			// Neither timer condition applies (including nonzero production that is
			// insufficient for recovery), so discard both pending delays.
			*boostStartTime = time.Time{}
			*recoveryStartTime = time.Time{}
		}

		// At maximum amperage with exactly zero solar, skip normal hysteresis so
		// this rule cannot immediately reduce the boost target.
		if currentAmp == variables.MaxAmperage && variables.LiveSolarProductionW == 0.0 {
			result.TargetAmperage = variables.MaxAmperage
			return result
		}
	} else {
		*boostStartTime = time.Time{}
		*recoveryStartTime = time.Time{}
	}

	// ------------------------------------------------------------------------
	// Ordinary solar hysteresis. It runs only while charging and only when the
	// boost/recovery logic has not already selected a different target.
	// ------------------------------------------------------------------------
	if calculatedTargetAmp == currentAmp && carState == 2 { // 2 = charging
		// Convert net solar potential into candidate whole-amp targets on either
		// side of the deadband. This can move by multiple amps in one calculation.
		targetAmpsStepUp := int((result.PotentialSolarTotal - hysteresisBuffer) / wattPerAmpStep)
		targetAmpsStepDown := int((result.PotentialSolarTotal + hysteresisBuffer) / wattPerAmpStep)

		if targetAmpsStepUp > currentAmp {
			calculatedTargetAmp = targetAmpsStepUp
			if calculatedTargetAmp > variables.MaxAmperage {
				calculatedTargetAmp = variables.MaxAmperage
			}
			// Estimate remaining grid surplus after the extra charging power is used.
			powerToSpend := float64(calculatedTargetAmp-currentAmp) * wattPerAmpStep
			result.PredictedLeftoverSurplusW = result.GridSurplus - powerToSpend

		} else if targetAmpsStepDown < currentAmp {
			calculatedTargetAmp = targetAmpsStepDown
			if calculatedTargetAmp < variables.MinAmperage {
				calculatedTargetAmp = variables.MinAmperage
			}
			// Reducing charging frees approximately this much power to the grid.
			powerSaved := float64(currentAmp-calculatedTargetAmp) * wattPerAmpStep
			result.PredictedLeftoverSurplusW = result.GridSurplus + powerSaved
		}
	}

	result.TargetAmperage = calculatedTargetAmp

	if calculatedTargetAmp != currentAmp {
		// Only ordinary hysteresis changes are rate-limited. A car-state transition
		// bypasses this cooldown; override branches above return before this gate.
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
