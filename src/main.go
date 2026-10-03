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

	// Goroutine 1: Refresh Wallbox Data from API
	go func() {
		for {
			newStatus, err := getStatus(variables.WallboxIp)
			if err != nil {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				fmt.Printf("[%s] [ERROR] Failed to refresh status: %v\n", timestamp, err)
			} else {
				status.Mu.Lock()
				status.Data = newStatus
				status.Mu.Unlock()
			}
			time.Sleep(1000 * time.Millisecond) // Static rapid API sync
		}
	}()

	go func() {
		for {
			allMetrics, err := utils.FetchPrometheusMetrics(variables.PrometheusMetricsUrlInverter)
			if err != nil {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				fmt.Printf("[%s] [ERROR] Failed to fetch Prometheus metrics: %v\n", timestamp, err)
			} else {
				gridPower := allMetrics["total_grid_power_watts"]

				// Negative gridPower = Feeding into grid (Positive AvailableSurplusW)
				// Positive gridPower = Drawing from grid (Negative AvailableSurplusW)
				variables.AvailableSurplusW = -gridPower
			}
			time.Sleep(time.Duration(variables.RefreshIntervalInMillisecondsPromInverter) * time.Millisecond)
		}
	}()

	// Main control loop running sequentially in main() thread
	var lastWriteTime time.Time
	var lastCarState = 1            // Standby if default
	var boostStartTime time.Time    // Tracker for high grid draw delay (Step up)
	var recoveryStartTime time.Time // Tracker for clear solar return delay (Step down)

	// Console output loop for real-time monitoring of PV control decisions
	go func() {
		for {
			// Trigger calculation and retrieve data structure
			result := utils.CalculatePVControl(status, &lastWriteTime, &lastCarState, &boostStartTime, &recoveryStartTime)

			variables.TargetAmperage = result.TargetAmperage
			variables.PredictedLeftoverSurplusW = result.PredictedLeftoverSurplusW

			// Fetch human-readable car, psm, and fsp states from the thread-safe structure
			carDesc := "Unknown"
			psmDesc := "Unknown"
			fspDesc := "3-Phase"
			var sessionKwh float64 = 0.0

			status.Mu.RLock()
			currentCarState := status.Data.Car
			currentPsm := status.Data.Psm
			currentFsp := status.Data.Fsp
			sessionKwh = status.Data.Wh / 1000.0
			status.Mu.RUnlock()

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

			if currentFsp {
				fspDesc = "1-Phase"
			}

			// Parse active timer state contexts cleanly
			timerLogStr := "Idle"
			if !boostStartTime.IsZero() {
				timerLogStr = fmt.Sprintf("Boost:%ds/%ds", int(time.Since(boostStartTime).Seconds()), variables.GridThresholdDelaySec)
			} else if !recoveryStartTime.IsZero() {
				timerLogStr = fmt.Sprintf("Recover:%ds/%ds", int(time.Since(recoveryStartTime).Seconds()), variables.GridThresholdDelaySec)
			}

			// Calculate distance deltas to step up or step down
			deltaUpStr := "MaxReached"
			if result.CurrentAmperage < variables.MaxAmperage {
				deltaUpStr = fmt.Sprintf("%.0fW (Need:%.0fW)", result.NextStepUpWatts-result.PotentialSolarTotal, result.NextStepUpWatts)
			}

			deltaDownStr := "MinReached"
			if result.CurrentAmperage > variables.MinAmperage {
				deltaDownStr = fmt.Sprintf("%.0fW (Need:%.0fW)", result.PotentialSolarTotal-result.NextStepDownWatts, result.NextStepDownWatts)
			}

			timestamp := time.Now().Format("2006-01-02 15:04:05")

			// RENDER EXTENDED LOG: Appended dynamic step boundaries and active ticking timers
			fmt.Printf("[%s] [PV-Control] Curr:%d A (%.0fW) | GridSurplus:%.0fW | PotentialSolarTotal:%.0fW | Target:%d A | Car:%s | Mode:%s | Session:%.2f kWh | Psm:%s | Relay:%s | UpIn:%s | DownIn:%s | Timers:%s\n",
				timestamp, result.CurrentAmperage, result.CalculatedWbPower, result.GridSurplus, result.PotentialSolarTotal, result.TargetAmperage,
				carDesc, strings.ToUpper(variables.ChargeMode), sessionKwh, psmDesc, fspDesc, deltaUpStr, deltaDownStr, timerLogStr,
			)

			if result.ShouldWriteToWallbox {
				fmt.Printf("[%s] [API DISPATCH] Pushing new Amperage setting: %d A\n", timestamp, result.TargetAmperage)
				newValues := map[string]interface{}{"amp": result.TargetAmperage}
				err := utils.WriteSettings(variables.WallboxIp, newValues)
				if err != nil {
					fmt.Printf("[%s] [ERROR] Dispatch failed: %v\n", timestamp, err)
				} else {
					lastWriteTime = time.Now()
				}
			} else if result.TargetAmperage != result.CurrentAmperage {
				// Differentiate why it was blocked for accurate logging
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

			// Dynamic sleep timer bound to variables.go configuration
			time.Sleep(time.Duration(variables.CalculationIntervalMs) * time.Millisecond)
		}
	}()

	// Start Web server (blocking main thread)
	utils.StartWebServer(variables.WebServerPort, status)
}

func getStatus(ip string) (variables.WbStatus, error) {
	var status variables.WbStatus
	if err := utils.GetChargerStatus(ip, variables.ApiStatusFilter, &status); err != nil {
		fmt.Printf("Execution Error reading status: %v\n", err)
		return status, err
	}
	return status, nil
}

func printStatus(status variables.WbStatus) {
	fmt.Printf("Charger Name: %s\n", status.Fna)
	fmt.Printf("Current Amperage limit: %d A\n", status.Amp)
	fmt.Printf("Car State: %d\n", status.Car)

	var targetMode string
	switch status.Psm {
	case 1:
		targetMode = "Forced 1-Phase"
	case 2:
		targetMode = "Forced 3-Phase"
	default:
		targetMode = "Automatic"
	}
	fmt.Printf("Phase Switch Mode (psm): %s\n", targetMode)

	var activePhases int = 3
	if status.Fsp {
		activePhases = 1
	}
	fmt.Printf("Active Connected Relay State (fsp): %d-Phase\n", activePhases)

	if len(status.Nrg) > 15 {
		fmt.Printf("Real-time Power Consumption: %.2f kW\n", status.Nrg[15]/1000.0)
		fmt.Printf("Line Load currents: L1: %.1fA | L2: %.1fA | L3: %.1fA\n", status.Nrg[6], status.Nrg[7], status.Nrg[8])
	}
}
