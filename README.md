# Wallbox Controller

A thread-safe Go daemon for dynamic Photovoltaic (PV) surplus charging. The controller bridges local smart grid infrastructure by pooling live telemetry from a **Prometheus Inverter Metrics exporter** and dynamically regulating a **go-eCharger Gemini** via its local API v2 framework.

The architecture focuses on maximizing solar self-consumption, protecting charging hardware contactors, and preserving vehicle battery health during volatile weather periods.

---

## 🚀 Key Features

* **Symmetrical Deadband Hysteresis:** Eliminates relay and contactor chatter during passing clouds using a fully configurable mathematical guard band.
* **Efficiency Boost Engine:** Detects massive household consumption (e.g., heat pumps, cooktops) and automatically switches the charger to 11 kW (16 A) to minimize EV charging overhead and vehicle vampire drain.
* **Pre-Conditioning Recovery:** Automatically forces maximum capability (16 A) when the vehicle transitions to `Charge Finished`, ensuring morning cabin pre-heating runs 100% off the grid without draining the car battery.
* **Thread-Safe Memory Pool:** Implements high-speed asynchronous synchronization using Go's `sync.RWMutex` to isolate background network telemetry from the frontend HTTP Layout Engine.
* **Unified Web UI & Remote API:** Provides a clean browser card layout and a stateless POST API wrapper for scriptable remote control (e.g., Home Assistant, Cron jobs).

---
## 📐 Algorithmic Core Math

### 1. Symmetrical Hysteresis Loop
The tracking engine calculates the total available power footprint dynamically every second:

\[\text{PotentialSolarTotal} = \text{GridSurplus} + \text{CalculatedWbPower}\]

To step up or down, the algorithm evaluates the capacity against the `SolarHysteresisBandW` threshold:

* **Step Up Criterion:** 
  \[\text{TargetAmps} = \text{int}\left(\frac{\text{PotentialSolarTotal} - \text{SolarHysteresisBandW}}{\text{WattPerAmpStep}}\right)\]
  *(Only dispatched if GridSurplus > 50W to prevent immediate rubber-banding)*

* **Step Down Criterion:** 
  \[\text{TargetAmps} = \text{int}\left(\frac{\text{PotentialSolarTotal} + \text{SolarHysteresisBandW}}{\text{WattPerAmpStep}}\right)\]
  *(Triggered only when grid import breaches the lower deadband boundary)*

### 2. Guard Logic and State Machine

```text
       [ Vehicle State Check ]
            /         \
     [Car: 1/3/4]    [Car: 2 (Charging)]

          |                   |
   [Set Boundaries]    [Deficit Audit] ----> PotentialSolarTotal < -Threshold?

          |                   |                             | (Sustained 5m)
  (Unplugged -> 6A)    [Solar Tracking]             [EFFICIENCY BOOST]
  (Finished  -> 16A)          |                             |
                       (Normal Hysteresis)           (Lock to 16A / 11kW)
```

---
## 🛠️ Configuration Parameter Reference

All operational thresholds are managed inside `variables/variables.go` using pure integer configurations:

```go
NominalVoltage               = 230.0 // Local grid phase voltage baseline
MinAmperage                  = 6     // Floor limit (approx 4.1 kW on 3-Phase)
MaxAmperage                  = 16    // Ceiling limit (approx 11.0 kW on 3-Phase)
WallboxWriteCooldownSec      = 60    // Minimum transmission window to protect EEPROM
GridThresholdW               = 1500  // Deficit ceiling before forcing 11 kW boost
GridThresholdDelaySec        = 300   // Time delay window (5 mins) to filter short spikes
SolarHysteresisBandW         = 400   // Symmetrical buffer zone to stabilize stepping
```

---

## 🖥️ Terminal Interface Telemetry

The application streams highly structured runtime execution logs down to the standard OS console output:

```text
[2026-10-03 17:01:58] [PV-Control] Curr:6 A (4140W) | GridSurplus:-1450W | PotentialSolarTotal:2690W | Target:6 A | Car:Charging | Mode:SOLAR | Session:18.50 kWh | Psm:Automatic | Relay:3-Phase | UpIn:2440W (Need:5130W) | DownIn:MinReached | Timers:Boost:12s/300s
```

---

## 🌐 Remote API Endpoint Schema

The internal web framework blocks standard browser header `GET` commands inside action routing scopes to ensure operational safety. State parameters must be passed using an explicit **`POST` HTTP Method**:

### Force Maximum Output Mode (11 kW / 16 A)
```bash
curl -X POST "http://<CONTROLLER_IP>:8084/setmode?mode=max"
```

### Re-Enable Automatic Solar Tracking Loop
```bash
curl -X POST "http://<CONTROLLER_IP>:8084/setmode?mode=solar"
```

---

## ⚙️ Compiling and Deployment Execution

1. Ensure a working installation of Go (`>= 1.27`) is present on your host machine.
2. Clone this repository framework layout to your operating directory path.
3. Open your terminal shell configuration and execute the native compiler:

```bash
go run .
```

To build a standalone optimized binary artifact signature for distribution deployment fields:

```bash
go build -ldflags="-s -w" -o wallbox-controller .
```
