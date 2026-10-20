package utils

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"wallbox-controller/variables"
)

// Global CSS Stylesheet container - completely isolated from the layout logic
const webserverCSS = `
<style>
	body {
		/* Uses Inter first, then modern system UI fonts, falling back to a clean tech look */
    	font-family: 'Inter', -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
		background-color: #f4f7f6;
		color: #333;
		margin: 0;
		padding: 15px; /* Reduced from 30px for small screens */
		display: flex;
		flex-direction: column;
		align-items: center;
		box-sizing: border-box;
	}
	*, *:before, *:after {
		box-sizing: inherit; /* Prevents padding from causing horizontal overflow */
	}
	.card {
		background: #ffffff;
		padding: 20px; /* Scaled down padding */
		border-radius: 12px;
		box-shadow: 0 4px 15px rgba(0,0,0,0.05);
		width: 100%;
		max-width: 800px;
		display: flex;         /* Added to help manage child widths */
		flex-direction: column;
		min-width: 0;          /* Essential: lets flex items shrink/grow correctly below 800px */
	}
	h3 {
		margin-top: 0;
		color: #2c3e50;
		border-bottom: 2px solid #eaeded;
		padding-bottom: 10px;
		font-size: 1.4rem; /* Scalable font unit */
	}
	.button-group {
		margin: 20px 0;
		display: flex;
		flex-wrap: wrap; /* Allows buttons to stack vertically on narrow devices */
		gap: 10px;
	}
	button {
		flex: 1 1 calc(50% - 10px); /* Tries to keep 2 items per row, or fits full width */
		min-width: 140px; /* Prevents text squeezing */
		padding: 12px;
		font-size: 15px;
		font-weight: 600;
		border-radius: 8px;
		border: none;
		cursor: pointer;
		transition: all 0.2s ease-in-out;
		-webkit-tap-highlight-color: transparent; /* Smooth touch targets for iOS/Android */
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
		padding: 15px;
		border-radius: 8px;
		/* Uses JetBrains Mono or Fira Code first, falling back to crisp system terminal fonts */
        font-family: 'JetBrains Mono', 'Fira Code', 'SF Mono', Menlo, Monaco, Consolas, "Courier New", monospace;
		font-size: 14px; /* Slightly smaller for mobile layout viewports */
		line-height: 1.5;
		overflow-x: auto;
		white-space: pre-wrap; /* Forces long unbroken strings to wrap safely */
		word-break: break-all;
		box-shadow: inset 0 2px 8px rgba(0,0,0,0.2);

		/* Text-fitting rules */
		white-space: pre;      /* CRITICAL: Disables text wrapping entirely */
		word-break: normal;    /* Prevents words from snapping apart */
		overflow-x: auto;      /* Adds a clean swipe/scroll bar *only* if the text exceeds view */
		width: max-content;      /* Fits the box snug around your longest line (the curl command) */
		max-width: 100%;         /* Prevents it from breaking past the white card boundaries */
		min-width: 600px;        /* Keeps width stable if short values or statuses load in */
	}
	.metrics-divider {
		color: #fee900;
		font-weight: bold;
		margin-top: 5px;
		padding-top: 5px;
	}
	.section-divider {
		color: #2ecc71;
		font-weight: bold;
		border-top: 1px solid #3d4e5d;
		margin-top: 15px;
		padding-top: 15px;
	}
	.curl-divider {
		color: #3498db;
		font-weight: bold;
		border-top: 1px solid #3d4e5d;
		margin-top: 15px;
		padding-top: 15px;
	}

	/* Responsive overrides for extra small screens (e.g., phones in portrait) */
	@media (max-width: 480px) {
		body {
			padding: 10px;
		}
		.card {
			padding: 15px;
		}
		.button-group button {
			flex: 1 1 100%; /* Fully stacks the buttons vertically */
		}
	}
</style>`

type SafeStatus struct {
	Mu   sync.RWMutex
	Data variables.WbStatus
}

var (
	GlobalLastBoostTime    time.Time
	GlobalLastRecoveryTime time.Time
)

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
					_ = WriteSettings(variables.WallboxIp, newValues, status)
				} else {
					// FIX: Wenn auf SOLAR geschaltet wird, rufen wir sofort die Rechen-Engine auf,
					// um den mathematisch korrekten Startwert für genau DIESE Sekunde zu ermitteln!
					// Wir schalten NICHT mehr blind auf 6 A zurück!
					var dummyWrite time.Time
					var dummyCarState = status.Data.Car
					var dummyBoost time.Time
					var dummyRecover time.Time

					result := CalculatePVControl(status, &dummyWrite, &dummyCarState, &dummyBoost, &dummyRecover)

					newValues["amp"] = result.TargetAmperage
					fmt.Printf("--> [WEBSERVER INTELLIGENT DISPATCH] Solar mode initialized with calculated target: %d A\n", result.TargetAmperage)
					_ = WriteSettings(variables.WallboxIp, newValues, status)
				}

				time.Sleep(500 * time.Millisecond)
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
		currentAmp := status.Data.Amp
		isOnePhase := status.Data.Fsp
		carState := status.Data.Car
		status.Mu.RUnlock()

		if currentAmp == 0 {
			currentAmp = variables.MinAmperage
		}

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
		fmt.Fprintf(w, "<form action='/setmode?mode=solar' method='POST' style='flex:1;'><button type='submit' class='%s' onclick='setTimeout(function(){location.reload();}, 600);'>%s</button></form>", solarBtnClass, solarLabel)
		fmt.Fprintf(w, "<form action='/setmode?mode=max' method='POST' style='flex:1;'><button type='submit' class='%s' onclick='setTimeout(function(){location.reload();}, 600);'>%s</button></form>", maxBtnClass, maxLabel)
		fmt.Fprintf(w, "</div>")

		fmt.Fprintf(w, "<pre>")
		fmt.Fprintf(w, "<div class='metrics-divider'>--- Wallbox Controller Metrics ---</div>")
		fmt.Fprintf(w, "Operational Mode Enforced:          %s\n", strings.ToUpper(variables.ChargeMode))
		fmt.Fprintf(w, "Charger Name:                       %s\n", status.Data.Fna)
		fmt.Fprintf(w, "Current Amperage limit:             %d A\n", status.Data.Amp)

		var carStateDesc string
		switch carState {
		case 1:
			carStateDesc = "Ready (No vehicle connected)"
		case 2:
			carStateDesc = "Charging actively"
		case 3:
			carStateDesc = "Connected (Waiting / Paused / Battery full)"
		case 4:
			carStateDesc = "Charge finished (Vehicle still connected)"
		default:
			carStateDesc = fmt.Sprintf("Unknown state (%d)", carState)
		}
		fmt.Fprintf(w, "Car State:                          %s\n", carStateDesc)

		var targetMode string
		switch status.Data.Psm {
		case 1:
			targetMode = "Forced 1-Phase"
		case 2:
			targetMode = "Forced 3-Phase"
		default:
			targetMode = "Automatic"
		}
		fmt.Fprintf(w, "Phase Switch Mode (psm):            %s\n", targetMode)

		var activePhases int = 3
		if isOnePhase {
			activePhases = 1
		}
		fmt.Fprintf(w, "Active Relay State (fsp):           %d-Phase\n", activePhases)

		currentWattsCalculated := float64(status.Data.Amp) * float64(activePhases) * variables.NominalVoltage
		targetWattsCalculated := float64(variables.TargetAmperage) * float64(activePhases) * variables.NominalVoltage

		fmt.Fprintf(w, "Energy Charged (Session):           %.2f kWh\n", status.Data.Wh/1000.0)

		var dummyWrite time.Time
		var dummyCarState = carState
		var dummyBoost time.Time
		var dummyRecover time.Time
		result := CalculatePVControl(status, &dummyWrite, &dummyCarState, &dummyBoost, &dummyRecover)

		if len(status.Data.Nrg) > 11 {
			totalWattsMeasured := status.Data.Nrg[11]
			fmt.Fprintf(w, "Measured Consumption:               %.2f kW\n", totalWattsMeasured/1000.0)

			calculatedAmps := 0.0
			if totalWattsMeasured > 0 {
				calculatedAmps = totalWattsMeasured / (float64(activePhases) * variables.NominalVoltage)
			}

			currentL1 := calculatedAmps
			currentL2 := calculatedAmps
			currentL3 := calculatedAmps
			if activePhases == 1 {
				currentL2 = 0.0
				currentL3 = 0.0
			}

			fmt.Fprintf(w, "Line Load Currents:                 L1: %.1f A | L2: %.1f A | L3: %.1f A\n", currentL1, currentL2, currentL3)
		} else {
			fmt.Fprintf(w, "Measured Consumption:               Calculating...\n")
			fmt.Fprintf(w, "Line Load Currents:                 L1: -- A | L2: -- A | L3: -- A\n")
		}

		if variables.WallboxApiWriteToEeprom == true {
			fmt.Fprintf(w, "EEPROM Write Cooldown:              %d s (Minimum Safety Window)\n", variables.WallboxWriteCooldownSec)
			/*if cooldownRemaining > 0 {
				fmt.Fprintf(w, "EEPROM Write Cooldown:               Active (%ds remaining)\n", cooldownRemaining)
			} else {
				fmt.Fprintf(w, "EEPROM Write Cooldown:               Ready\n")
			}*/
		}

		fmt.Fprintf(w, "<div class='section-divider'>--- Live Grid Management ---</div>")
		if currentWattsCalculated != targetWattsCalculated {
			if variables.ChargeMode == "max" {
				fmt.Fprintf(w, "Current set Ampere value:           %.0f W (New target Ampere value: %.0f W - Bypassing Solar Control)\n", currentWattsCalculated, float64(variables.MaxAmperage*activePhases)*variables.NominalVoltage)
			} else {
				fmt.Fprintf(w, "Current set Ampere value:           %.0f W (New target Ampere value: %.0f W)\n", currentWattsCalculated, targetWattsCalculated)
			}
		} else {
			fmt.Fprintf(w, "Current set Ampere value:           %.0f W\n", currentWattsCalculated)
		}

		var availableSurplusWHuman float64
		if variables.AvailableSurplusW == -0 { // If negative 0 print it as 0
			availableSurplusWHuman = 0
		} else {
			availableSurplusWHuman = variables.AvailableSurplusW
		}
		fmt.Fprintf(w, "Available Solar Surplus:            %.0f W\n", availableSurplusWHuman)
		fmt.Fprintf(w, "Current PV Solar Production:        %.0f W\n", variables.LiveSolarProductionW)
		fmt.Fprintf(w, "Calculated Net House Load (no EV):  %.0f W\n", result.CalculatedHouseLoadWatts)
		fmt.Fprintf(w, "True Household Solar Potential:     %.0f W\n", result.PotentialSolarTotal)

		if carState == 2 {
			if status.Data.Amp < variables.MaxAmperage {
				fmt.Fprintf(w, "Required Power to Step Up (+1A):    %.0f W (Threshold Target: %.0f W)\n", result.NextStepUpWatts-result.PotentialSolarTotal, result.NextStepUpWatts)
			} else {
				fmt.Fprintf(w, "Required Power to Step Up (+1A):    Maximum Amperage Reached\n")
			}

			if status.Data.Amp > variables.MinAmperage {
				fmt.Fprintf(w, "Allowed Drop before Step Down:      %.0f W (Threshold Target: %.0f W)\n", result.PotentialSolarTotal-result.NextStepDownWatts, result.NextStepDownWatts)
			} else {
				fmt.Fprintf(w, "Allowed Drop before Step Down:      Minimum Amperage Reached\n")
			}
		} else {
			fmt.Fprintf(w, "Required Power to Step Up (+1A):    N/A\n")
			fmt.Fprintf(w, "Allowed Drop before Step Down:      N/A\n")
		}

		timerLogStr := "Idle"
		if !GlobalLastBoostTime.IsZero() {
			timerLogStr = fmt.Sprintf("Boost Pending (%ds / %ds)", int(time.Since(GlobalLastBoostTime).Seconds()), variables.GridThresholdDelaySec)
		} else if !GlobalLastRecoveryTime.IsZero() {
			timerLogStr = fmt.Sprintf("Solar Recovery Pending (%ds / %ds)", int(time.Since(GlobalLastRecoveryTime).Seconds()), variables.GridThresholdDelaySec)
		}
		fmt.Fprintf(w, "Efficiency Engine Timers State:     %s\n", timerLogStr)
		fmt.Fprintf(w, "Solar Hysteresis Buffer:            %d W (Symmetrical Guard)\n", variables.SolarHysteresisBandW)
		fmt.Fprintf(w, "High Grid Import Rule:              > %d W for %ds → Force Max Charge", variables.GridThresholdW, variables.GridThresholdDelaySec)

		currentHost := r.Host
		if currentHost == "" {
			currentHost = fmt.Sprintf("127.0.0.1:%d", port)
		}

		fmt.Fprintf(w, "<div class='curl-divider'>--- Remote Control Commands Framework ---</div>")
		fmt.Fprintf(w, "Trigger Solar Mode (Auto):\n")
		fmt.Fprintf(w, "curl -X POST \"http://%s/setmode?mode=solar\"\n\n", currentHost)
		fmt.Fprintf(w, "Trigger Max Mode (%.0f kW):\n", maxKWCalculated)
		fmt.Fprintf(w, "curl -X POST \"http://%s/setmode?mode=max\"\n", currentHost)

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
