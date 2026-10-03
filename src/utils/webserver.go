package utils

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"wallbox-controller/variables"
)

// Global CSS Stylesheet container - completely isolated from the layout logic
const webserverCSS = `
<style>
	body {
		font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
		background-color: #f4f7f6;
		color: #333;
		margin: 0;
		padding: 30px;
		display: flex;
		flex-direction: column;
		align-items: center;
	}
	.card {
		background: #ffffff;
		padding: 30px;
		border-radius: 12px;
		box-shadow: 0 4px 15px rgba(0,0,0,0.05);
		width: 100%;
		max-width: 650px;
	}
	h3 {
		margin-top: 0;
		color: #2c3e50;
		border-bottom: 2px solid #eaeded;
		padding-bottom: 10px;
	}
	.button-group {
		margin: 20px 0 30px 0;
		display: flex;
		gap: 15px;
	}
	button {
		flex: 1;
		padding: 12px 20px;
		font-size: 15px;
		font-weight: 600;
		border-radius: 8px;
		border: none;
		cursor: pointer;
		transition: all 0.2s ease-in-out;
	}
	.btn-inactive {
		background-color: #e0e6ed;
		color: #7f8c8d;
	}
	.btn-inactive:hover {
		background-color: #d1dbe5;
		color: #2c3e50;
	}
	.btn-solar-active {
		background-color: #2ecc71;
		color: #ffffff;
		box-shadow: 0 4px 10px rgba(46, 204, 113, 0.3);
	}
	.btn-max-active {
		background-color: #3498db;
		color: #ffffff;
		box-shadow: 0 4px 10px rgba(52, 152, 219, 0.3);
	}
	pre {
		background-color: #1e272e;
		color: #f8f9fa;
		padding: 20px;
		border-radius: 8px;
		font-family: 'Courier New', Courier, monospace;
		font-size: 14px;
		line-height: 1.6;
		overflow-x: auto;
		box-shadow: inset 0 2px 8px rgba(0,0,0,0.2);
	}
	.section-divider {
		color: #0be881;
		font-weight: bold;
		border-top: 1px solid #3d4e5d;
		margin-top: 15px;
		padding-top: 15px;
	}
</style>
`

type SafeStatus struct {
	Mu   sync.RWMutex
	Data variables.WbStatus
}

func StartWebServer(port int, status *SafeStatus) {

	// Handle button action form posts
	http.HandleFunc("/setmode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			modeParam := r.URL.Query().Get("mode")
			if modeParam == "solar" || modeParam == "max" {
				variables.ChargeMode = modeParam
				fmt.Printf("[WEBSERVER] Dynamic switch executed. Charge Mode set to: %s\n", variables.ChargeMode)

				newValues := map[string]interface{}{}
				if variables.ChargeMode == "max" {
					newValues["amp"] = variables.MaxAmperage
					fmt.Printf("--> [WEBSERVER INSTANT DISPATCH] Forcing max current immediately: %d A\n", variables.MaxAmperage)
				} else {
					newValues["amp"] = variables.MinAmperage
					fmt.Printf("--> [WEBSERVER INSTANT DISPATCH] Reverting to solar base current immediately: %d A\n", variables.MinAmperage)
				}

				_ = WriteSettings(variables.WallboxIp, newValues)
			}
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if variables.WebServerAutoReloadIntervalSec > 0 {
			fmt.Fprintf(w, "<meta http-equiv=\"refresh\" content=\"%d\">\n", variables.WebServerAutoReloadIntervalSec)
		}

		fmt.Fprint(w, webserverCSS)

		status.Mu.RLock()
		defer status.Mu.RUnlock()

		maxKWCalculated := (float64(variables.MaxAmperage) * 3.0 * variables.NominalVoltage) / 1000.0

		fmt.Fprintf(w, "<div class='card'>")
		fmt.Fprintf(w, "<h3>Wallbox Control Dashboard</h3>")

		solarBtnClass := "btn-inactive"
		maxBtnClass := "btn-inactive"
		solarLabel := "Solar Mode (Auto)"
		maxLabel := fmt.Sprintf("Force Max Mode (%.0f kW)", maxKWCalculated)

		if variables.ChargeMode == "solar" {
			solarBtnClass = "btn-solar-active"
			solarLabel = "● " + solarLabel
		} else {
			maxBtnClass = "btn-max-active"
			maxLabel = "● " + maxLabel
		}

		fmt.Fprintf(w, "<div class='button-group'>")
		fmt.Fprintf(w, "<form action='/setmode?mode=solar' method='POST' style='flex:1;'><button type='submit' class='%s'>%s</button></form>", solarBtnClass, solarLabel)
		fmt.Fprintf(w, "<form action='/setmode?mode=max' method='POST' style='flex:1;'><button type='submit' class='%s'>%s</button></form>", maxBtnClass, maxLabel)
		fmt.Fprintf(w, "</div>")

		fmt.Fprintf(w, "<pre>")
		fmt.Fprintf(w, "--- Wallbox Controller Metrics ---\n")
		fmt.Fprintf(w, "Operational Mode Enforced: %s\n", strings.ToUpper(variables.ChargeMode))
		fmt.Fprintf(w, "Charger Name:              %s\n", status.Data.Fna)
		fmt.Fprintf(w, "Current Amperage limit:    %d A\n", status.Data.Amp)

		var carStateDesc string
		switch status.Data.Car {
		case 1:
			carStateDesc = "Ready (No vehicle connected)"
		case 2:
			carStateDesc = "Charging actively"
		case 3:
			carStateDesc = "Connected (Waiting / Paused / Battery full)"
		case 4:
			carStateDesc = "Charge finished (Vehicle still connected)"
		default:
			carStateDesc = fmt.Sprintf("Unknown state (%d)", status.Data.Car)
		}
		fmt.Fprintf(w, "Car State:                 %s\n", carStateDesc)

		var targetMode string
		switch status.Data.Psm {
		case 1:
			targetMode = "Forced 1-Phase"
		case 2:
			targetMode = "Forced 3-Phase"
		default:
			targetMode = "Automatic"
		}
		fmt.Fprintf(w, "Phase Switch Mode (psm):   %s\n", targetMode)

		var activePhases int = 3
		if status.Data.Fsp {
			activePhases = 1
		}
		fmt.Fprintf(w, "Active Relay State (fsp):  %d-Phase\n", activePhases)

		currentWattsCalculated := float64(status.Data.Amp) * float64(activePhases) * variables.NominalVoltage
		targetWattsCalculated := float64(variables.TargetAmperage) * float64(activePhases) * variables.NominalVoltage

		fmt.Fprintf(w, "Energy Charged (Session):  %.2f kWh\n", status.Data.Wh/1000.0)

		// Index [11] Watts in float64
		if len(status.Data.Nrg) > 11 {
			totalWattsMeasured := status.Data.Nrg[11] // KORREKTUR: [11] hinzugefügt!
			fmt.Fprintf(w, "Measured Consumption:      %.2f kW\n", totalWattsMeasured/1000.0)

			// Calculate amperage per phase: Watt / (Phases * 230V)
			calculatedAmps := 0.0
			if totalWattsMeasured > 0 {
				calculatedAmps = totalWattsMeasured / (float64(activePhases) * variables.NominalVoltage)
			}

			// Since the car charges symmetrically, we output the calculated real-time current for all active phases
			currentL1 := calculatedAmps
			currentL2 := calculatedAmps
			currentL3 := calculatedAmps
			if activePhases == 1 {
				currentL2 = 0.0
				currentL3 = 0.0
			}

			fmt.Fprintf(w, "Line Load Currents:        L1: %.1f A | L2: %.1f A | L3: %.1f A\n",
				currentL1, currentL2, currentL3)
		} else {
			fmt.Fprintf(w, "Measured Consumption:      Calculating...\n")
			fmt.Fprintf(w, "Line Load Currents:        L1: -- A | L2: -- A | L3: -- A\n")
		}

		fmt.Fprintf(w, "<div class='section-divider'>--- Live Grid Management ---</div>")
		fmt.Fprintf(w, "Available Solar Surplus:   %.0f W\n", variables.AvailableSurplusW)

		if variables.ChargeMode == "max" {
			fmt.Fprintf(w, "Current set Ampere value:  %.0f W / New target Ampere value: %.0f W (Bypassing Solar Control)\n", currentWattsCalculated, float64(variables.MaxAmperage*activePhases)*variables.NominalVoltage)
			fmt.Fprintf(w, "Available Solar Surplus after Wallbox adjustment: N/A (Max Mode Forced)\n")
		} else {
			fmt.Fprintf(w, "Current set Ampere value:  %.0f W / New target Ampere value: %.0f W\n", currentWattsCalculated, targetWattsCalculated)
			fmt.Fprintf(w, "Available Solar Surplus after Wallbox adjustment:  %.0f W\n", variables.PredictedLeftoverSurplusW)
		}
		fmt.Fprintf(w, "</pre>")
		fmt.Fprintf(w, "</div>")
	})

	var listenAddr string
	if variables.IPv4Only {
		listenAddr = fmt.Sprintf("0.0.0.0:%d", port)
	} else {
		listenAddr = fmt.Sprintf(":%d", port)
	}

	fmt.Printf("Webserver starting on %s...\n", listenAddr)
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		fmt.Printf("Critical error starting the webserver: %v\n", err)
	}
}
