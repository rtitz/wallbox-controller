package variables

import (
	"runtime"
)

var (
	AppName       = "Wallbox-Controller"
	AppVersion    = "1.0.0"
	DataSourceDir = "./data"
	WallboxIp     = "192.168.30.40"
)

var (
	GOOS   = runtime.GOOS
	GOARCH = runtime.GOARCH
)

// GoEStatus holds a few common v2 API keys for demonstration.
// Refer to the official docs for the hundreds of available keys.
type GoEStatus struct {
	Fna string    `json:"fna"` // Friendly Name
	Amp int       `json:"amp"` // Target Amperage Limit
	Car int       `json:"car"` // Car Connectivity State
	Frc int       `json:"frc"` // Force Charging State Override
	Psm int       `json:"psm"` // Phase Switch Mode (0=Auto, 1=1-Phase, 2=3-Phase)
	Fsp bool      `json:"fsp"` // Force Single Phase Status (true=1-Phase active)
	Nrg []float64 `json:"nrg"` // Array containing Power information
	Dws float64   `json:"dws"` // Current Session energy delivered in Wh
}

var ApiStatusFilter = "api/status?filter=fna,amp,car,frc,psm,nrg,dws"

// Define an explicit struct to hold an ordered setting parameter
type ChargerParam struct {
	Key   string
	Value interface{}
}
