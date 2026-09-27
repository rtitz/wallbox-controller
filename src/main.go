package main

import (
	"flag"
	"fmt"
	"wallbox-controller/utils"
	"wallbox-controller/variables"
)

var BuildNumber = "development-build"

func main() {
	// Parse base URL and format parameters
	fmt.Printf("%s %s (%s/%s | Build: %s)\n\n", variables.AppName, variables.AppVersion, variables.GOOS, variables.GOARCH, BuildNumber)

	flag.StringVar(&variables.WallboxIp, "ip", variables.WallboxIp, "Wallbox IP address (e.g., 192.168.30.40)")
	flag.Parse()

	if err := utils.ConnectToWallbox(variables.WallboxIp); err != nil {
		fmt.Printf("Failed to connect to Wallbox: %v\n", err)
	}

	// 1. READ STATUS via the new isolated function
	var status variables.GoEStatus
	if err := utils.GetChargerStatus(variables.WallboxIp, variables.ApiStatusFilter, &status); err != nil {
		fmt.Printf("Execution Error reading status: %v\n", err)
		return
	}
	printStatus(status)

	// 2. WRITE VALUE via your single transaction helper function
	settingsToChange := map[string]interface{}{
		"amp": 6,
		//"alw": true,
	}
	// Pass the IP variable directly into your execution block
	fmt.Printf("Targeting Wallbox at IP: %s\n", variables.WallboxIp)
	if err := utils.SetChargerValues(variables.WallboxIp, settingsToChange); err != nil {
		fmt.Printf("Execution Error: %v\n", err)
	}

	//fmt.Printf("Request to %s completed with status code: %d\n", apiUrl, resp.StatusCode)

}

func printStatus(status variables.GoEStatus) {
	// Output your metrics
	fmt.Printf("Charger Name: %s\n", status.Fna)
	fmt.Printf("Current Amperage limit: %d A\n", status.Amp)
	fmt.Printf("Car State: %d\n", status.Car)

	// Phase Analysis output
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
