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

	// Goroutine 1: Refresh Wallbox Data from API (The absolute truth during the night)
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
			time.Sleep(1000 * time.Millisecond)
		}
	}()

	// Goroutine 2: Refresh Data directly from Prometheus (Authentic Grid tracking 24/7)
	go func() {
		for {
			allMetrics, err := utils.FetchPrometheusMetrics(variables.PrometheusMetricsUrlInverter)

			if err != nil {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				fmt.Printf("[%s] [ERROR] Failed to fetch Prometheus metrics: %v\n", timestamp, err)
			} else {
				production := allMetrics["inverter_ac_power_watts"]
				gridPower := allMetrics["total_grid_power_watts"]

				// FIX: LiveSolarProductionW reads 0W naturally at night when the inverter sleeps.
				// We don't overwrite the dynamic grid meter readings with fallback dummies anymore!
				if production == 0 || allMetrics["total_solar_power_watts"] == 0 {
					variables.LiveSolarProductionW = 0.0
				} else {
					variables.LiveSolarProductionW = production
				}

				// The house meter (total_grid_power_watts) stays active and fluid 24/7.
				// Negative gridPower = Feeding into grid / Positive gridPower = Drawing from grid
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

			utils.GlobalLastBoostTime = boostStartTime
			utils.GlobalLastRecoveryTime = recoveryStartTime

			variables.TargetAmperage = result.TargetAmperage
			variables.PredictedLeftoverSurplusW = result.PredictedLeftoverSurplusW

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

			timerLogStr := "Idle"
			if !boostStartTime.IsZero() {
				timerLogStr = fmt.Sprintf("Boost:%ds/%ds", int(time.Since(boostStartTime).Seconds()), variables.GridThresholdDelaySec)
			} else if !recoveryStartTime.IsZero() {
				timerLogStr = fmt.Sprintf("Recover:%ds/%ds", int(time.Since(recoveryStartTime).Seconds()), variables.GridThresholdDelaySec)
			}

			deltaUpStr := "MaxReached"
			if result.CurrentAmperage < variables.MaxAmperage {
				deltaUpStr = fmt.Sprintf("%.0fW (Need:%.0fW)", result.NextStepUpWatts-result.PotentialSolarTotal, result.NextStepUpWatts)
			}

			deltaDownStr := "MinReached"
			if result.CurrentAmperage > variables.MinAmperage {
				deltaDownStr = fmt.Sprintf("%.0fW (Need:%.0fW)", result.PotentialSolarTotal-result.NextStepDownWatts, result.NextStepDownWatts)
			}

			timestamp := time.Now().Format("2006-01-02 15:04:05")

			fmt.Printf("[%s] [PV-Control] Curr:%d A (%.0fW) | PV-Gen:%.0fW | GridSurplus:%.0fW | PotentialSolarTotal:%.0fW | Target:%d A | Car:%s | Mode:%s | Session:%.2f kWh | Psm:%s | Relay:%s | UpIn:%s | DownIn:%s | Timers:%s\n",
				timestamp, result.CurrentAmperage, result.CalculatedWbPower, variables.LiveSolarProductionW, result.GridSurplus, result.PotentialSolarTotal, result.TargetAmperage,
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

			time.Sleep(time.Duration(variables.CalculationIntervalMs) * time.Millisecond)
		}
	}()

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
