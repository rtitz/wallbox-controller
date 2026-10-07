package main

import (
	"flag"
	"fmt"
	"strings"
	"time"
	"wallbox-controller/utils"
	"wallbox-controller/variables"
)

var BuildNumber = "development-build"

func main() {
	fmt.Printf("%s %s (%s/%s | Build: %s)\n\n", variables.AppName, variables.AppVersion, variables.GOOS, variables.GOARCH, BuildNumber)

	flag.StringVar(&variables.WallboxIp, "ip", variables.WallboxIp, "Wallbox IP address")
	flag.Parse()

	if err := utils.ConnectToWallbox(variables.WallboxIp); err != nil {
		fmt.Printf("Failed to connect to Wallbox: %v\n", err)
	}

	status := &utils.SafeStatus{}

	// Goroutine 1: High-Frequency Wallbox Telemetry Polling Loop.
	// Continually synchronizes the local hardware state to provide an absolute, single source of truth.
	// This captures car status changes and actual power draw independent of the grid metrics.
	go func() {
		for {
			newStatus, err := getStatus(variables.WallboxIp)
			if err != nil {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				fmt.Printf("[%s] [ERROR] Failed to refresh status: %v\n", timestamp, err)
			} else {
				// Thread-Safe Memory Lock: Safely copy the newly fetched network payload into the
				// global state container to eliminate data races with the web UI and control loops.
				status.Mu.Lock()
				status.Data = newStatus
				status.Mu.Unlock()
			}

			// Enforce a strict 1-second cadence to remain responsive without flooding the local wallbox API
			time.Sleep(1000 * time.Millisecond)
		}
	}()

	// Goroutine 2: Continuous Smart Grid Telemetry Sync via Prometheus Inverter Exporter.
	// This loop maintains the core real-time telemetry engine driving all physics and threshold calculations.
	go func() {
		for {
			allMetrics, err := utils.FetchPrometheusMetrics(variables.PrometheusMetricsUrlInverter)

			if err != nil {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				fmt.Printf("[%s] [ERROR] Failed to fetch Prometheus metrics: %v\n", timestamp, err)
			} else {
				// Extract raw values using configuration-mapped keys to isolate network names from core algorithm
				production := allMetrics[variables.MetricKeyInverterProduction]
				gridPower := allMetrics[variables.MetricKeyTotalGridPower]

				// Capture entire physical household load (including active EV charger footprint)
				if totalHouseLoad, ok := allMetrics[variables.MetricKeyTotalHouseLoad]; ok {
					variables.LiveTotalHouseConsumptionW = totalHouseLoad
				}

				// Sanity Guard: Force clean zero if inverter is in standby mode or sleeping at night
				if production == 0 {
					variables.LiveSolarProductionW = 0.0
				} else {
					variables.LiveSolarProductionW = production
				}

				// Mathematically invert the grid signature: Negative grid power means export (surplus > 0),
				// positive grid power means import from public utility (deficit / surplus < 0).
				variables.AvailableSurplusW = -gridPower
			}

			// Clock the pooling interval based on your central configuration settings
			time.Sleep(time.Duration(variables.RefreshIntervalInMillisecondsPromInverter) * time.Millisecond)
		}
	}()

	// Goroutine 3: Central Evaluation & Control Engine Loop (Core State Machine)
	// This execution frame processes the mathematical hysteresis equations sequentially,
	// formats console telemetry logs, and safely dispatches network payload writes to the hardware.
	go func() {
		// Central Execution Framework: Local state memory for the sequential tracking loop.
		// These variables persist across loop cycles to establish timeouts and stability anchors.
		// Relocated inside the Goroutine scope to ensure absolute thread-safety.
		var lastWriteTime time.Time     // Precision timestamp tracking the last physical API dispatch to enforce cooldown guards
		var lastCarState = 1            // State Machine Memory: Cached vehicle attachment mode to detect instant connection events
		var boostStartTime time.Time    // Non-volatile timer frame tracking high grid deficits before firing the 11 kW High Load Boost
		var recoveryStartTime time.Time // Non-volatile timer frame tracking genuine solar return stability before releasing a High Load Lock

		for {
			// Trigger calculation and retrieve data structure using localized frame pointers
			result := utils.CalculatePVControl(status, &lastWriteTime, &lastCarState, &boostStartTime, &recoveryStartTime)

			// Share current state boundaries globally to expose data models to the HTTP web engine
			utils.GlobalLastBoostTime = boostStartTime
			utils.GlobalLastRecoveryTime = recoveryStartTime

			variables.TargetAmperage = result.TargetAmperage
			variables.PredictedLeftoverSurplusW = result.PredictedLeftoverSurplusW

			carDesc := "Unknown"
			psmDesc := "Unknown"
			fspDesc := "3-Phase"
			var sessionKwh float64 = 0.0

			// Isolate read access to avoid network collision drifts during background state copy frames
			status.Mu.RLock()
			currentCarState := status.Data.Car
			currentPsm := status.Data.Psm
			currentFsp := status.Data.Fsp
			sessionKwh = status.Data.Wh / 1000.0
			status.Mu.RUnlock()

			// Human Readable Parsing: Map internal machine registers to clean log tags
			switch currentCarState {
			case 1:
				carDesc = "Unplugged"
			case 2:
				carDesc = "Charging"
			case 3:
				carDesc = "Waiting"
			case 4:
				carDesc = "Finished"
			}

			switch currentPsm {
			case 1:
				psmDesc = "Forced-1Ph"
			case 2:
				psmDesc = "Forced-3Ph"
			default:
				psmDesc = "Automatic"
			}

			// Invert binary relay flags (true maps to single-phase charging profile alignment)
			if currentFsp {
				fspDesc = "1-Phase"
			}

			// Format dynamic countdown monitors to supervise the Efficiency Engine delay structures
			timerLogStr := "Idle"
			if !boostStartTime.IsZero() {
				timerLogStr = fmt.Sprintf("Boost:%ds/%ds", int(time.Since(boostStartTime).Seconds()), variables.GridThresholdDelaySec)
			} else if !recoveryStartTime.IsZero() {
				timerLogStr = fmt.Sprintf("Recover:%ds/%ds", int(time.Since(recoveryStartTime).Seconds()), variables.GridThresholdDelaySec)
			}

			// Evaluate dynamic step thresholds to provide explicit visual debugging frames
			deltaUpStr := "MaxReached"
			if result.CurrentAmperage < variables.MaxAmperage {
				deltaUpStr = fmt.Sprintf("%.0fW (Need:%.0fW)", result.NextStepUpWatts-result.PotentialSolarTotal, result.NextStepUpWatts)
			}

			deltaDownStr := "MinReached"
			if result.CurrentAmperage > variables.MinAmperage {
				deltaDownStr = fmt.Sprintf("%.0fW (Need:%.0fW)", result.PotentialSolarTotal-result.NextStepDownWatts, result.NextStepDownWatts)
			}

			timestamp := time.Now().Format("2006-01-02 15:04:05")
			// Dispatch structured execution statistics to standard system console out
			fmt.Printf("[%s] [PV-Control] Curr:%d A (%.0fW) | PV-Gen:%.0fW | GridSurplus:%.0fW | PotentialSolarTotal:%.0fW | Target:%d A | Car:%s | Mode:%s | Session:%.2f kWh | Psm:%s | Relay:%s | UpIn:%s | DownIn:%s | Timers:%s\n",
				timestamp, result.CurrentAmperage, result.CalculatedWbPower, variables.LiveSolarProductionW, result.GridSurplus, result.PotentialSolarTotal, result.TargetAmperage,
				carDesc, strings.ToUpper(variables.ChargeMode), sessionKwh, psmDesc, fspDesc, deltaUpStr, deltaDownStr, timerLogStr,
			)

			// ------------------------------------------------------------------------
			// Hardware Dispatch Layer (Network Target Execution)
			// ------------------------------------------------------------------------
			if result.ShouldWriteToWallbox {
				fmt.Printf("[%s] [API DISPATCH] Pushing new Amperage setting: %d A\n", timestamp, result.TargetAmperage)
				newValues := map[string]interface{}{"amp": result.TargetAmperage}

				// Transmit payload using centralized memory pointers; auto-logs to disk upon verification
				err := utils.WriteSettings(variables.WallboxIp, newValues, status)
				if err != nil {
					fmt.Printf("[%s] [ERROR] Dispatch failed: %v\n", timestamp, err)
				} else {
					lastWriteTime = time.Now() // Reset throttling clock instantly after valid network execution
				}
			} else if result.TargetAmperage != result.CurrentAmperage {
				// Throttling Audit Trail: Explains exactly why a calculated change was suppressed
				status.Mu.RLock()
				currentCarState := status.Data.Car
				status.Mu.RUnlock()

				if variables.SetWallboxOnlyIfCarConnected && currentCarState == 1 {
					fmt.Printf("[%s] [API BLOCKED] Target %d A skipped. No vehicle connected.\n", timestamp, result.TargetAmperage)
				} else if result.ThrottledRemainingSec > 0 {
					fmt.Printf("[%s] [THROTTLED] Write target %d A blocked. Cooldown active for another %.1fs\n",
						timestamp, result.TargetAmperage, result.ThrottledRemainingSec)
				}
			}

			// Clock calculation step interval before polling hardware registers again
			time.Sleep(time.Duration(variables.CalculationIntervalMs) * time.Millisecond)
		}
	}()

	// Start frontend HTTP server (This blocks execution permanently keeping the main() thread alive)
	utils.StartWebServer(variables.WebServerPort, status)
}

// getStatus executes a dedicated network read operation against the Wallbox hardware API.
// This function wraps the low-level HTTP client transaction, automatically injecting
// configuration-mapped filter paths to minimize network transmission payloads.
func getStatus(ip string) (variables.WbStatus, error) {
	var status variables.WbStatus

	// Pass the memory pointer reference down to the decoder engine to populate the data model
	if err := utils.GetChargerStatus(ip, variables.ApiStatusFilter, &status); err != nil {
		fmt.Printf("Execution Error reading status: %v\n", err)
		return status, err
	}

	return status, nil
}
