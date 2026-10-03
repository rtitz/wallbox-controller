package variables

import (
	"runtime"
)

// App Meta Information
var (
	AppName    = "Wallbox-Controller"
	AppVersion = "1.0.0"
)

// Network Endpoints & Sync Timers
var (
	WallboxIp                                 = "192.168.30.40"
	RefreshIntervalInMillisecondsWallbox      = 2000
	PrometheusMetricsUrlInverter              = "http://192.168.1.4:2112/metrics"
	RefreshIntervalInMillisecondsPromInverter = 1000
)

// Web Server Configurations
var (
	WebServerPort = 8080
	IPv4Only      = true
	// WebServerAutoReloadIntervalSec defines the browser refresh rate.
	// 0 means never auto-reload, values > 0 define refresh rate in seconds.
	WebServerAutoReloadIntervalSec = 10
)

// Centralized Grid and Hardware Configurations
var (
	NominalVoltage               = 230.0
	MinAmperage                  = 6       // Standard lower boundary for most EVs
	MaxAmperage                  = 16      // 16 for 11kW, 32 for 22kW
	CalculationIntervalMs        = 1000    // Loop execution rate for main algorithm
	WallboxWriteCooldownSec      = 60      // Strict physical API transmission cooldown
	SetWallboxOnlyIfCarConnected = true    // true = skip idle API writes when unplugged
	ChargeMode                   = "solar" // ChargeMode can be set to "solar" (PV Hysteresis tracking) or "max" (Force maximum grid power)

	// Efficiency Boost Configurations (Anti-Vampire Load Management)
	GridThresholdW        = 1500 // If more than (e.g. 1500W) grid import, the system automatically forces 11/22 kW to maximize charging efficiency.
	GridThresholdDelaySec = 300  // Delay in seconds before triggering the boost after high grid draw

	// NEW: Centralized Hysteresis Buffer Zone
	// A ± 400W buffer to prevent the charging rate from fluctuating too rapidly
	HysteresisBufferW = 400
)

// Live Controller State Values (Updated dynamically at runtime)
var (
	AvailableSurplusW         float64
	TargetAmperage            = MinAmperage
	PredictedLeftoverSurplusW = 0.0
)

// Runtime System Indicators
var (
	GOOS   = runtime.GOOS
	GOARCH = runtime.GOARCH
)

// WbStatus holds specific v2 API keys for the go-eCharger.
type WbStatus struct {
	Fna string    `json:"fna"` // Friendly Name
	Amp int       `json:"amp"` // Target Amperage Limit
	Car int       `json:"car"` // Car Connectivity State
	Frc int       `json:"frc"` // Force Charging State Override
	Psm int       `json:"psm"` // Phase Switch Mode
	Fsp bool      `json:"fsp"` // Force Single Phase Status
	Nrg []float64 `json:"nrg"` // Array containing Power information
	Wh  float64   `json:"wh"`  // Energy charged in Wh (Session)
}

var ApiStatusFilter = "api/status?filter=fna,amp,car,frc,psm,nrg,wh"

// ChargerParam defines an explicit structure to pass ordered control payloads
type ChargerParam struct {
	Key   string
	Value interface{}
}
