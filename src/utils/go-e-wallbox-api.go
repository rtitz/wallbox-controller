package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
	"wallbox-controller/variables"
)

// This thread-local variable guarantees instant synchronization.
// It bypasses the slow 2-second HTTP network polling interval entirely.
var lastWrittenAmperage int = 0

func ConnectToWallbox(ip string) error {
	fmt.Printf("Attempting to connect to Wallbox at %s ...\n\n", ip)
	return nil
}

// GetChargerStatus handles the complete GET transaction and decodes the JSON payload.
func GetChargerStatus(ipAddress string, filterPath string, target interface{}) error {
	apiUrl := fmt.Sprintf("http://%s/%s", ipAddress, filterPath)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiUrl)
	if err != nil {
		return fmt.Errorf("http call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected remote status code: %d", resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

// SetChargerValues loops through parameters and applies them safely.
func SetChargerValues(ipAddress string, params map[string]interface{}, status *SafeStatus) error {
	client := &http.Client{Timeout: 5 * time.Second}

	// 1. GLOBAL AMPERE GUARD: Evaluate BEFORE looping through keys
	if value, exists := params["amp"]; exists {
		if targetAmp, ok := value.(int); ok {
			// If the target already matches our memory tracking frame, remove it from the map payload!
			if targetAmp == lastWrittenAmperage {
				delete(params, "amp")
			}
		}
	}

	// 2. QUICK LEAK CHECK: If 'amp' was the only setting and we removed it, exit early without network activity
	if len(params) == 0 {
		return nil
	}

	for key, value := range params {
		// Proceed with network dispatch if value actually changed
		rawURL := fmt.Sprintf("http://%s/api/set", ipAddress)
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("failed to parse URL: %w", err)
		}

		jsonValue, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value for key %s: %w", key, err)
		}

		q := u.Query()
		q.Set(key, string(jsonValue))
		u.RawQuery = q.Encode()

		resp, err := client.Get(u.String())
		if err != nil {
			return fmt.Errorf("http request failed for key %s: %w", key, err)
		}

		// Create an internal scope scope defer to handle immediate multi-key loop closures cleanly
		func() {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				fmt.Errorf("failed to set %s (Status %d): %s", key, resp.StatusCode, string(body))
			}
		}()

		// SUCCESS: Update our local tracking cache block safely
		if key == "amp" {
			if targetAmp, ok := value.(int); ok {
				lastWrittenAmperage = targetAmp
			}
		}
	}

	return nil
}

// WriteSettings handles the API payload transmission and triggers the file logger upon success
func WriteSettings(ip string, settings map[string]interface{}, status *SafeStatus) error {
	// 1. EXTRACT AND VALIDATE TARGET AMPERE VALUE FROM THE REQUEST
	if targetAmp, ok := settings["amp"].(int); ok {
		status.Mu.RLock()
		currentAmp := status.Data.Amp
		status.Mu.RUnlock()

		// 2. ABSOLUTE GUARD: If the hardware is already at the target, drop the write entirely
		if targetAmp == currentAmp {
			// Initialize sync matching so immediate fallback sequences check correctly
			lastWrittenAmperage = targetAmp

			// If 'amp' is the only thing we want to write, block execution right here
			if len(settings) == 1 {
				return nil
			}
		}
	}

	// Pass the safe status container down to evaluate the EEPROM safety check
	if err := SetChargerValues(ip, settings, status); err != nil {
		return err
	}

	status.Mu.RLock()
	isOnePhase := status.Data.Fsp
	status.Mu.RUnlock()

	activePhases := 3
	if isOnePhase {
		activePhases = 1
	}

	// Only print a physical hardware log line if the 'amp' parameter actually traveled down the wire
	if targetAmp, ok := settings["amp"].(int); ok {
		if targetAmp == lastWrittenAmperage {
			LogApiWrite(targetAmp, activePhases)
		}
	}

	return nil
}

// LogApiWrite appends every successful hardware API write to a localized flat file framework.
func LogApiWrite(targetAmp int, activePhases int) {
	if !variables.WriteLogEnabled {
		return
	}

	dirPath := filepath.Dir(variables.WriteLogFilePath)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		fmt.Printf("[%s] [LOG ERROR] Failed to provision directory tree structures %s: %v\n",
			time.Now().Format("2006-01-02 15:04:05"), dirPath, err)
		return
	}

	file, err := os.OpenFile(variables.WriteLogFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("[%s] [LOG ERROR] Could not open write log file: %v\n", time.Now().Format("2006-01-02 15:04:05"), err)
		return
	}
	defer file.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	calculatedKw := (float64(targetAmp) * float64(activePhases) * 230.0) / 1000.0

	logLine := fmt.Sprintf("[%s] [HARDWARE WRITE] Target: %d A | Phases: %d (%.2f kW)\n",
		timestamp, targetAmp, activePhases, calculatedKw)

	if _, err := file.WriteString(logLine); err != nil {
		fmt.Printf("[%s] [LOG ERROR] Could not write to log file: %v\n", timestamp, err)
	}
}
