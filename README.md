# Wallbox Controller

A thread-safe Go daemon for dynamic Photovoltaic (PV) surplus charging. The controller bridges local smart grid infrastructure by pooling live telemetry from a **Prometheus Inverter Metrics exporter** and dynamically regulating a **go-eCharger Gemini** via its local API v2 framework.

The architecture focuses on maximizing solar self-consumption, protecting charging hardware contactors, and preserving vehicle battery health during volatile weather periods.

---

## 🚀 Key Features

* **Solar hysteresis:** Adjusts charging current in one-amp increments or larger, with a configurable wattage deadband to reduce changes during fluctuating solar production.
* **Zero-solar boost:** While the car is charging, exactly zero reported solar production starts a delay timer. Once the delay expires, the target becomes the configured maximum current. This rule does not use the configured `GridThresholdW` value or measured grid import.
* **Solar recovery:** From maximum current, sufficient net solar potential sustained for the configured delay returns the target to minimum current; ordinary solar hysteresis then adjusts it as conditions change.
* **Measured load estimate:** Estimates non-wallbox house load by subtracting measured wallbox power (`Nrg[11]`) from the Prometheus house-consumption metric, then subtracts that estimate from solar production.
* **Thread-safe status snapshot:** Uses a `sync.RWMutex` to protect wallbox status shared by the polling loop, control loop, and web server.
* **Web UI and mode endpoint:** Provides a browser dashboard and a `POST /setmode` endpoint for switching between solar and maximum-current modes.

---

## 📐 Algorithmic Core Math

### 1. Symmetrical Hysteresis Loop
The control loop recalculates its target every two seconds by default. It assumes the Prometheus house-consumption metric includes the wallbox:

\[\text{CalculatedHouseLoadWatts} = \text{TotalHouseConsumption (Prometheus)} - \text{RealWbPowerMeasured (Wallbox)}\]
\[\text{PotentialSolarTotal} = \text{LiveSolarProductionW} - \text{CalculatedHouseLoadWatts}\]

`CalculatedWbPower` is an estimate from configured amperage, active phases, and nominal voltage. The house-load calculation instead uses the measured wallbox power from `Nrg[11]`. Grid-meter surplus is reported separately; the control thresholds use `PotentialSolarTotal`.

During active charging (`Car State: 2`), the algorithm derives candidate amperage steps from net solar potential and the hysteresis band:

* **Step Up Criterion:** 
  \[\text{TargetAmps} = \text{int}\left(\frac{\text{PotentialSolarTotal} - \text{SolarHysteresisBandW}}{\text{WattPerAmpStep}}\right)\]

* **Step Down Criterion:** 
  \[\text{TargetAmps} = \text{int}\left(\frac{\text{PotentialSolarTotal} + \text{SolarHysteresisBandW}}{\text{WattPerAmpStep}}\right)\]

The step-up candidate is applied only when it exceeds the current setting; the step-down candidate is applied only when it is below the current setting. Targets are clamped to the configured minimum and maximum, and a single calculation can change multiple amps. Outside the deadband, the controller does not adjust current using this solar rule unless the car is actively charging.

### 2. Control Priority and State Machine

```text
1. Max mode: target the configured maximum current.
2. If car-connected-only policy is enabled (default): unplugged -> minimum;
  waiting/finished -> hold current, or maximum if that option is enabled.
3. While charging: exactly 0 W solar sustained for GridThresholdDelaySec -> maximum.
  From maximum, enough net solar sustained for the same delay -> minimum.
4. Otherwise, apply ordinary solar hysteresis while charging.
```

The boost condition is `LiveSolarProductionW == 0`; `GridThresholdW` is not used by the control function, despite its name and the dashboard label. Waiting/finished states are set to maximum only when `SetMaxAmperageWhenConnectedAndNotCharging` is enabled (it is disabled by default). Ordinary hysteresis writes are subject to the write cooldown, except when the car state changes. Max-mode and connection-policy branches return before that cooldown check.

---

## 🛠️ Configuration Parameter Reference

The main control values are defined in `src/variables/variables.go`:

```go
NominalVoltage               = 230.0 // Local grid phase voltage baseline
MinAmperage                  = 6     // Floor limit (approx 4.1 kW on 3-Phase)
MaxAmperage                  = 16    // Ceiling limit (approx 11.0 kW on 3-Phase)
CalculationIntervalMs       = 2000  // Control-loop interval (2 seconds)
WallboxWriteCooldownSec     = 60    // Cooldown for ordinary hysteresis writes
GridThresholdW              = 1500  // Currently not used by the control function
GridThresholdDelaySec       = 300   // Delay for zero-solar boost and solar recovery
SolarHysteresisBandW        = 400   // Deadband for solar current adjustments
SetWallboxOnlyIfCarConnected = true // Apply connection-state overrides
SetMaxAmperageWhenConnectedAndNotCharging = false // Optional max while waiting/finished
```

Maximum current is a current limit, not a fixed power level: at 16 A, 230 V, and three phases, the estimate is about 11.0 kW; actual power depends on active phases and vehicle limits. `GridThresholdW` is currently displayed by the web dashboard but does not affect the control logic.

---

## 🖥️ Terminal Interface Telemetry

The application prints a control summary to standard output on each control-loop iteration. `PotentialSolarTotal` is solar production minus estimated non-wallbox house load; `GridSurplus` comes from the grid-power metric.

```text
[2026-10-04 10:28:56] [PV-Control] Curr:16 A (11040W) | PV-Gen:2745W | GridSurplus:328W | PotentialSolarTotal:315W | Target:16 A | Car:Finished | Mode:SOLAR | Session:31.65 kWh | Psm:Automatic | Relay:3-Phase | UpIn:MaxReached | DownIn:-10325W (Need:10640W) | Timers:Idle
```

---

## 🌐 Remote API Endpoint Schema

The web UI exposes one mode-switch endpoint. It accepts `POST` requests with a `mode` query parameter and redirects to the dashboard afterward:

### Force Maximum Current
```bash
curl -X POST "http://<CONTROLLER_IP>:8084/setmode?mode=max"
```

### Re-enable Solar Tracking
```bash
curl -X POST "http://<CONTROLLER_IP>:8084/setmode?mode=solar"
```

Maximum mode immediately requests the configured maximum amperage. Switching back to solar mode calculates and writes a current target before the regular control loop continues.

---

## ⚙️ Compiling and Native Execution

1. Install Go 1.27.4, as specified by `src/go.mod` and the container build.
2. From the `src/` directory, run the daemon:

```bash
go run .
```

To compile a standalone, cross-platform optimized binary artifact signature for production deployment fields:

```bash
go build -ldflags="-s -w" -o wallbox-controller .
```

---

## 🦭 Podman Container Deployment (Rootless Framework)

Since Podman runs inherently rootless and daemonless, it is the ideal target engine for secure smart home infrastructure. The application can be compiled, isolated, and persisted entirely within unprivileged user spaces.

### Method A: Orchestration via Podman Compose (Recommended)
This uses the repository's `compose.yaml`, which publishes port 8084, mounts a host directory at `/app/log`, sets the timezone, and configures container log rotation. Update the Prometheus URL in that file to an address reachable from the container.

```bash
# Initialize, compile, and launch the complete stack in detached background mode
podman compose up -d --build --force-recreate

# Recompile the source code and apply internal changes seamlessly on the fly to specific container
podman compose build wallbox-controller && podman compose up -d
```

### Method B: Manual Standalone Container CLI
If you prefer running a single standalone container without using orchestration wrappers:

#### 1. Build the Container Image locally
Execute the container builder inside your root repository directory (where the `Containerfile` / `Dockerfile` resides):
```bash
podman build -t wallbox-controller:latest -f Containerfile .
```

#### 2. Execute the Standalone Rootless Container Daemon
*Note on Network Modes:* For native Linux nodes (e.g., Ubuntu Server), using host network mapping (`--net=host`) is highly recommended to eliminate virtualization routing overhead for local Prometheus and go-eCharger API requests. For development tasks on macOS nodes, swap `--net=host` with explicitly bound port signatures: `-p 8084:8084`.

```bash
podman run -d \
  --name wallbox-controller \
  --net=host \
  -v ./data/log:/app/log:Z \
  --userns=keep-id \
  --log-driver=k8s-file \
  --log-opt max-size=10mb \
  --log-opt max-file=3 \
  --restart unless-stopped \
  --tz=Europe/Berlin \
  wallbox-controller:latest
```

---

## 📊 Container Telemetry & Automation Operations

### 1. Verify Active Telemetry Streams
To audit the dynamic tracking calculations and physics boundaries evaluated inside the running container workspace:
```bash
podman logs -f wallbox-controller
```

### 2. Follow Physical Hardware Write Logs on the Host Disk
```bash
tail -f data/log/wallbox_writes.log
```

### 3. Enable Systemd Persistence across Host Reboots
To ensure Podman resurrects your tracking daemon immediately after a host OS reboot—even if your user session is completely logged out—generate a native user-space systemd unit file framework:

```bash
mkdir -p ~/.config/systemd/user/
podman generate systemd --name wallbox-controller --files --new
mv container-wallbox-controller.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now container-wallbox-controller.service
```

---

### Configuration Environment Variables

String environment overrides are supported for `WALLBOX_IP`, `PROMETHEUS_METRICS_URL_INVERTER`, and `WRITE_LOG_FILE_PATH`. The current generic environment helper does not parse numeric or boolean values: setting the other environment variables in this table can cause the application to panic. Change those values in `src/variables/variables.go` until typed parsing is implemented.

| Environment Variable | Description | Expected Type | Default Value |
| :--- | :--- | :--- | :--- |
| **`WALLBOX_IP`** | Static network destination IP address of your physical go-eCharger hardware. | String | `192.168.30.40` |
| **`PROMETHEUS_METRICS_URL_INVERTER`** | Endpoint URL where the inverter's Prometheus telemetry metrics are scraped. | String | `http://127.0.0.1:2112/metrics` |
| **`WEB_SERVER_PORT`** | Active network port binding configuration for the internal web engine framework. | Integer | `8084` |
| **`WEB_SERVER_IPV4_ONLY`** | With `true`, listens on `0.0.0.0`; with `false`, listens on the unspecified address. | Boolean | `true` |
| **`WEB_SERVER_AUTO_RELOAD_INTERVAL_SEC`** | Controls how frequently the browser frontend card layout refreshes its status metrics. | Integer | `10` |
| **`MIN_AMPERE`** | Minimum amperage target. | Integer | `6` |
| **`MAX_AMPERE`** | Maximum amperage target (16 A is about 11 kW at three phases and 230 V). | Integer | `16` |
| **`WRITE_LOG_ENABLED`** | Toggles whether API execution parameters are tracked locally on the host storage disk. | Boolean | `true` |
| **`WRITE_LOG_FILE_PATH`** | Destination filesystem pathway map for saving the physical engine transition records. | String | `log/wallbox_writes.log` |
