package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type SpeedTestResult struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Ping      struct {
		Latency float64 `json:"latency"`
	} `json:"ping"`
	Download struct {
		Bandwidth int64 `json:"bandwidth"`
	} `json:"download"`
	Upload struct {
		Bandwidth int64 `json:"bandwidth"`
	} `json:"upload"`
}

type FormattedSpeedTest struct {
	Timestamp    string  `json:"timestamp"`
	PingMs       float64 `json:"ping_ms"`
	DownloadMbps float64 `json:"download_mbps"`
	UploadMbps   float64 `json:"upload_mbps"`
}

func (f *FormattedSpeedTest) toCSV() []string {
	return []string{
		f.Timestamp,
		strconv.FormatFloat(f.PingMs, 'f', 2, 64),
		strconv.FormatFloat(f.DownloadMbps, 'f', 2, 64),
		strconv.FormatFloat(f.UploadMbps, 'f', 2, 64),
	}
}

func ensureCSVFile(filename string) (*os.File, error) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		file, err := os.Create(filename)
		if err != nil {
			return nil, fmt.Errorf("error creating CSV file: %w", err)
		}
		writer := csv.NewWriter(file)
		header := []string{"timestamp", "ping_ms", "download_mbps", "upload_mbps"}
		if err := writer.Write(header); err != nil {
			file.Close()
			return nil, fmt.Errorf("error writing CSV header: %w", err)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			file.Close()
			return nil, fmt.Errorf("error flushing CSV writer: %w", err)
		}
		return file, nil
	}
	return os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
}

func parseSpeedTestOutput(output []byte) (*FormattedSpeedTest, error) {
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try to parse each line as a JSON object
		var result SpeedTestResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			continue
		}

		// Only process "result" type entries
		if result.Type != "result" {
			continue
		}

		// Convert bandwidth from bytes/s to Mbps
		downloadMbps := float64(result.Download.Bandwidth) * 8 / 1_000_000
		uploadMbps := float64(result.Upload.Bandwidth) * 8 / 1_000_000

		return &FormattedSpeedTest{
			Timestamp:    result.Timestamp.Format(time.RFC3339),
			PingMs:       result.Ping.Latency,
			DownloadMbps: downloadMbps,
			UploadMbps:   uploadMbps,
		}, nil
	}

	return nil, fmt.Errorf("no valid speed test result found in output")
}

func runSpeedTest() (*FormattedSpeedTest, error) {
	speedtest := exec.Command("speedtest", "--progress=no", "--format=json")
	output, err := speedtest.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("error running speedtest: %w\nOutput: %s", err, string(output))
	}

	return parseSpeedTestOutput(output)
}

func runSpeedTestWithRetry(maxRetries int, retryDelay time.Duration) (*FormattedSpeedTest, error) {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			log.Printf("Retry attempt %d/%d after error: %v", i+1, maxRetries, lastErr)
			time.Sleep(retryDelay)
		}

		result, err := runSpeedTest()
		if err == nil {
			if i > 0 {
				log.Printf("Successfully completed speed test after %d retries", i)
			}
			return result, nil
		}
		lastErr = err
		log.Printf("Speed test attempt failed: %v", err)
	}
	return nil, fmt.Errorf("failed after %d retries, last error: %v", maxRetries, lastErr)
}

func main() {
	// Set up logging
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting speedtest monitoring service...")

	// Initialize CSV file
	csvFile, err := ensureCSVFile("output.csv")
	if err != nil {
		log.Fatalf("Failed to initialize CSV file: %v", err)
	}
	defer csvFile.Close()
	csvWriter := csv.NewWriter(csvFile)
	defer csvWriter.Flush()

	// Create a ticker that triggers every 30 minutes (to avoid overloading)
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Run first test immediately with retry logic
	if result, err := runSpeedTestWithRetry(3, 1*time.Minute); err != nil {
		log.Printf("Error after retries: %v", err)
	} else {
		// Log JSON to console
		jsonResult, _ := json.MarshalIndent(result, "", "    ")
		log.Printf("Speed test results:\n%s", string(jsonResult))

		// Write to CSV
		if err := csvWriter.Write(result.toCSV()); err != nil {
			log.Printf("Error writing to CSV: %v", err)
		}
		csvWriter.Flush()
	}

	// Main loop
	for {
		select {
		case <-ticker.C:
			if result, err := runSpeedTestWithRetry(3, 1*time.Minute); err != nil {
				log.Printf("Error after retries: %v", err)
			} else {
				// Log JSON to console
				jsonResult, _ := json.MarshalIndent(result, "", "    ")
				log.Printf("Speed test results:\n%s", string(jsonResult))

				// Write to CSV
				if err := csvWriter.Write(result.toCSV()); err != nil {
					log.Printf("Error writing to CSV: %v", err)
				}
				csvWriter.Flush()
			}
		case sig := <-sigChan:
			log.Printf("Received signal %v, shutting down...", sig)
			return
		}
	}
}
