package variables

import (
	"runtime"
)

// App Meta Information
var (
	AppName    = "Wallbox-Controller"
	AppVersion = "1.0.5"
)

// Wallbox Network Configuration
var (
	WallboxIp                            = "192.168.30.40"
	RefreshIntervalInMillisecondsWallbox = 2000
)

// Inverter metrics
var (
	PrometheusMetricsUrlInverter              = "http://192.168.1.4:2112/metrics"
	RefreshIntervalInMillisecondsPromInverter = 2000

	// Prometheus Metric Key Mapping Configuration
	// Tying the exact exporter signatures to variables for effortless infrastructure changes
	MetricKeyInverterProduction = "inverter_ac_power_watts"
	MetricKeyTotalGridPower     = "total_grid_power_watts"
	MetricKeyTotalHouseLoad     = "total_house_consumption_watts"
)

// Web Server Configurations
var (
	WebServerPort                  = 8084
	IPv4Only                       = true
	WebServerAutoReloadIntervalSec = 10 // Browser UI card refresh frequency baseline in seconds
)

// Centralized Grid and Hardware Configurations
var (
	NominalVoltage               = 230.0
	MinAmperage                  = 6    // Standard lower boundary for most EVs
	MaxAmperage                  = 16   // 16 for 11kW, 32 for 22kW
	CalculationIntervalMs        = 2000 // Loop execution rate for main algorithm
	WallboxWriteCooldownSec      = 60   // Strict physical API transmission cooldown
	SetWallboxOnlyIfCarConnected = true // true = skip idle API writes when unplugged
	//WallboxApiWriteToEeprom      = false   // true = write to EEPROM (persistent), false = write only to volatile RAM (amx)
	ChargeMode = "solar" // "solar" (PV Hysteresis tracking) or "max" (Force maximum grid power)

	// Efficiency Boost Configurations (Anti-Vampire Load Management)
	GridThresholdW        = 1500 // If more than (e.g. 1500W) grid import, the system automatically forces 11/22 kW to maximize charging efficiency.
	GridThresholdDelaySec = 300  // Delay in seconds before triggering the boost after high grid draw

	// SolarHysteresisBandW defines the deadband zone (in Watts) for charging rate adjustments.
	// Prevents rapid relay/contactor oscillation during passing clouds.
	SolarHysteresisBandW = 400

	// Logging for write actions to the Wallbox API
	WriteLogEnabled  = true
	WriteLogFilePath = "log/wallbox_writes.log" // Relocated directory hierarchy perfect for external volume persistence mapping
)

// Live Controller State Values (Updated dynamically at runtime)
var (
	AvailableSurplusW          float64
	LiveSolarProductionW       float64
	LiveTotalHouseConsumptionW float64
	TargetAmperage             = MinAmperage
	PredictedLeftoverSurplusW  = 0.0
)

// Runtime System Indicators
var (
	GOOS   = runtime.GOOS
	GOARCH = runtime.GOARCH
)

// WbStatus holds specific v2 API keys for the go-eCharger.
type WbStatus struct {
	Fna string    `json:"fna"` // Friendly Name
	Amp int       `json:"amp"` // Persistent Target Amperage Limit (EEPROM write)
	Amx int       `json:"amx"` // Non-persistent Target Amperage Limit (volatile RAM write)
	Car int       `json:"car"` // Car Connectivity State (1 = unplugged, 2 = charging, 3 = connected but waiting, 4 = charge finished)
	Frc int       `json:"frc"` // Force Charging State Override
	Psm int       `json:"psm"` // Phase Switch Mode
	Fsp bool      `json:"fsp"` // Force Single Phase Status
	Nrg []float64 `json:"nrg"` // Array containing Power information (Index 11 = Total active real-time power)
	Wh  float64   `json:"wh"`  // Energy charged in Wh (Session)
}

// ApiStatusFilter builds the streamlined REST GET request signature.
// UPDATED: Appended the critical 'amx' register tracking field.
var ApiStatusFilter = "api/status?filter=fna,amp,amx,car,frc,psm,nrg,wh"

// ChargerParam defines an explicit structure to pass ordered control payloads
type ChargerParam struct {
	Key   string
	Value interface{}
}
