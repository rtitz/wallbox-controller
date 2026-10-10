# Wallbox Controller

A thread-safe Go daemon for dynamic Photovoltaic (PV) surplus charging. The controller bridges local smart grid infrastructure by pooling live telemetry from a **Prometheus Inverter Metrics exporter** and dynamically regulating a **go-eCharger Gemini** via its local API v2 framework.

The architecture focuses on maximizing solar self-consumption, protecting charging hardware contactors, and preserving vehicle battery health during volatile weather periods.

---

## 🚀 Key Features

* **Symmetrical Deadband Hysteresis:** Eliminates relay and contactor chatter during passing clouds using a fully configurable mathematical guard band.
* **Efficiency Boost Engine:** Detects massive absolute grid draw (e.g., heat pumps, cooktops, or pure night conditions) and automatically switches the charger to 11 kW (16 A) after 5 minutes to minimize EV charging overhead and vehicle vampire drain.
* **Pre-Conditioning Recovery:** Automatically forces maximum capability (16 A) when the vehicle transitions to `Charge Finished` or `Connected (Waiting)`, ensuring morning cabin pre-heating runs 100% off the grid without draining the car battery.
* **Unyielding Physics Core:** Calculates actual household potential based on live, physisch gemessener Wallbox-Leistung (`Nrg[11]`) instead of theoretical estimations, eliminating data corruption during balancing frames.
* **Thread-Safe Memory Pool:** Implements high-speed asynchronous synchronization using Go's `sync.RWMutex` to isolate background network telemetry from the frontend HTTP Layout Engine.
* **Unified Web UI & Remote API:** Provides a clean browser card layout and a stateless POST API wrapper for scriptable remote control (e.g., Home Assistant, Cron jobs).

---

## 📐 Algorithmic Core Math

### 1. Symmetrical Hysteresis Loop
The tracking engine calculates the true house net load and available solar potential dynamically every second:

\[\text{CalculatedHouseLoadWatts} = \text{TotalHouseConsumption (Prometheus)} - \text{RealWbPowerMeasured (Wallbox)}\]
\[\text{PotentialSolarTotal} = \text{LiveSolarProductionW} - \text{CalculatedHouseLoadWatts}\]

To step up or down during active charging (`Car State: 2`), the algorithm evaluates capacity against the centralized threshold:

* **Step Up Criterion:** 
  \[\text{TargetAmps} = \text{int}\left(\frac{\text{PotentialSolarTotal} - \text{SolarHysteresisBandW}}{\text{WattPerAmpStep}}\right)\]

* **Step Down Criterion:** 
  \[\text{TargetAmps} = \text{int}\left(\frac{\text{PotentialSolarTotal} + \text{SolarHysteresisBandW}}{\text{WattPerAmpStep}}\right)\]

If the vehicle is not actively charging (`Car State != 2`), bounds and deltas evaluate logically to `N/A` or `0` to secure absolute backend data integrity.

### 2. Guard Logic and State Machine

```text
       [ Vehicle State Check ]
            /         \
     [Car: 1/3/4]    [Car: 2 (Charging)]

          |                   |
   [Set Boundaries]    [Deficit Audit] ----> Total Grid Draw > 1500W?

          |                   |                             | (Sustained 5m)
  (Unplugged -> 6A)    [Solar Tracking]             [EFFICIENCY BOOST]
  (Finished  -> 16A)          |                             |
                       (Normal Hysteresis)           (Lock to 16A / 11kW)
```

---

## 🛠️ Configuration Parameter Reference

All operational thresholds are managed inside `variables/variables.go` using pure configuration parameters:

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

The application streams highly structured, tag- and nachtsichere runtime execution logs down to the standard OS console output:

```text
[2026-10-04 10:28:56] [PV-Control] Curr:16 A (11040W) | PV-Gen:2745W | GridSurplus:328W | PotentialSolarTotal:315W | Target:16 A | Car:Finished | Mode:SOLAR | Session:31.65 kWh | Psm:Automatic | Relay:3-Phase | UpIn:MaxReached | DownIn:-10325W (Need:10640W) | Timers:Idle
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

## ⚙️ Compiling and Native Execution

1. Ensure a working installation of Go (`>= 1.27.1`) is present on your host machine.
2. Open your terminal shell inside the `src/` directory framework layout and execute the native daemon:

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
This approach automatically provisions persistent volume layout mapping (`log/`) with correct SELinux/AppArmor security contexts (`:Z`), applies timezone synchronization, and configures automated log rotation boundaries.

```bash
# Initialize, compile, and launch the complete stack in detached background mode
podman-compose up -d --build --force-recreate ; podman image prune -f

# Recompile the source code and apply internal changes seamlessly on the fly to specific container
podman-compose build wallbox-controller && podman-compose up -d
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
  --userns_mode=keep-id \
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
