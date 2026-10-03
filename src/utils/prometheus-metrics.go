package utils

import (
	"bufio"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FetchPrometheusMetrics fetches the specified URL, parses all simple
// metrics, and returns them as a map[string]float64 to main().
func FetchPrometheusMetrics(url string) (map[string]float64, error) {
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

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments or empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Separate fields (metric name and value)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		metricName := fields[0]
		valueStr := fields[1]

		// Remove optional label braces {...} from the metric name
		// if present (e.g., for promhttp_metric_handler_requests_total)
		if idx := strings.Index(metricName, "{"); idx != -1 {
			metricName = metricName[:idx]
		}

		// Convert value to float64
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			// If a line cannot be converted, skip it
			continue
		}

		// Insert into the map
		metrics[metricName] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning text output: %w", err)
	}

	return metrics, nil
}
