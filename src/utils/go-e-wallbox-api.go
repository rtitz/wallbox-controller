package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

func ConnectToWallbox(ip string) error {
	// Placeholder implementation for connecting to the Wallbox
	fmt.Printf("Attempting to connect to Wallbox at %s ...\n\n", ip)

	// Simulate successful connection
	return nil
}

// GetChargerStatus handles the complete GET transaction and decodes the JSON payload.
// It accepts any destination pointer matching your model definitions.
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

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("json decoder failed: %w", err)
	}

	return nil
}

// SetChargerValues loops through all configurations and applies them one by one
// to ensure the go-eCharger firmware processes each update successfully.
func SetChargerValues(ipAddress string, params map[string]interface{}) error {
	client := &http.Client{Timeout: 5 * time.Second}

	for key, value := range params {
		// 1. Build the clean, single-parameter URL
		rawURL := fmt.Sprintf("http://%s/api/set", ipAddress)
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("failed to parse URL: %w", err)
		}

		// 2. Format the value as valid API v2 JSON
		jsonValue, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value for key %s: %w", key, err)
		}

		q := u.Query()
		q.Set(key, string(jsonValue))
		u.RawQuery = q.Encode()

		// 3. Execute the specific parameter request
		resp, err := client.Get(u.String())
		if err != nil {
			return fmt.Errorf("http request failed for key %s: %w", key, err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to set %s (Status %d): %s", key, resp.StatusCode, string(body))
		}

		//fmt.Printf("Successfully updated [%s]: %s\n", key, string(body))
	}

	return nil
}

// WriteSettings handles the API payload transmission
func WriteSettings(ip string, settings map[string]interface{}) error {
	if err := SetChargerValues(ip, settings); err != nil {
		return err
	}
	return nil
}
