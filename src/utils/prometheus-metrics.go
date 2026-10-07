package utils

import (
	"bufio"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FetchPrometheusMetrics connects to the specified metrics endpoint,
// streams the flat-text payload, and parses the fields into a fast map[string]float64.
// This function avoids heavy reflection or external parsing libraries to maximize efficiency.
func FetchPrometheusMetrics(url string) (map[string]float64, error) {
	// Establish a tight timeout boundary to prevent network hangs from blocking the core daemon
	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error fetching Prometheus URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode)
	}

	metrics := make(map[string]float64)
	scanner := bufio.NewScanner(resp.Body)

	// Stream and parse line-by-line to reduce memory pressure during high telemetry volumes
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Standard OpenMetrics compliance: Ignore empty lines and documentation comments (# HELP / # TYPE)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Separate string fields (Format: <metric_name>[{labels}] <value>)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		metricName := fields[0]
		valueStr := fields[1]

		// Label Strip Guard: Truncate everything starting from the bracket '{' opening sequence
		// to collapse labeled multi-dimensional metrics into a single aggregated map key entry.
		if idx := strings.Index(metricName, "{"); idx != -1 {
			metricName = metricName[:idx]
		}

		// Parse the value string into a standardized float64 numeric model footprint
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			// Quietly drop invalid, non-numeric lines or stale formatting
			continue
		}

		// Insert or overwrite the parsed telemetry float metric cell into the return dataset map
		metrics[metricName] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning text output: %w", err)
	}

	return metrics, nil
}
