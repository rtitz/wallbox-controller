package main

import (
	"flag"
	"fmt"
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
	var lastCarState = 1 // Standby if default

	go func() {
		for {
			// Trigger calculation and retrieve data structure
			result := utils.CalculatePVControl(status, &lastWriteTime, &lastCarState)

			variables.TargetAmperage = result.TargetAmperage
			variables.PredictedLeftoverSurplusW = result.PredictedLeftoverSurplusW

			// Generate the current timestamp for logging purposes
			timestamp := time.Now().Format("2006-01-02 15:04:05")

			// Log the current PV control status in a single line for easy reading
			fmt.Printf("[%s] [PV-Control] Ph:%d | Curr:%d A (%.0fW) | GridSurplus:%.0fW | PotentialSolarTotal:%.0fW | Target:%d A\n",
				timestamp, result.ActivePhases, result.CurrentAmperage, result.CalculatedWbPower,
				result.GridSurplus, result.PotentialSolarTotal, result.TargetAmperage)

			// Execute API action if permitted by throttling and connection variables
			if result.ShouldWriteToWallbox {
				fmt.Printf("[%s] [API DISPATCH] Pushing new Amperage setting: %d A\n", timestamp, result.TargetAmperage)
				newValues := map[string]interface{}{
					"amp": result.TargetAmperage,
				}

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
