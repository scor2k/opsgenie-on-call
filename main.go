package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"io"

	"golang.org/x/exp/rand"
)

// Structs to parse OpsGenie Who is on Call API responses
type OnCallResponse struct {
	Data      OnCallData `json:"data"`
	Took      float64    `json:"took"`
	RequestID string     `json:"requestId"`
}

type OnCallData struct {
	Parent           Parent   `json:"_parent"`
	OnCallRecipients []string `json:"onCallRecipients"`
}

type Parent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// Struct to hold aggregated data per person
type PersonData struct {
	Name       string
	TotalHours float64
}

func main() {
	// Parse command-line arguments
	startDateStr := flag.String("start", "", "Start date (YYYY-MM-DD)")
	endDateStr := flag.String("end", "", "End date (YYYY-MM-DD)")
	scheduleID := flag.String("schedule", "", "OpsGenie Schedule ID (UUID)")
	flag.Parse()

	// Validate required arguments
	if *startDateStr == "" || *endDateStr == "" || *scheduleID == "" {
		log.Fatal("Start date, End date, and Schedule ID must be provided.")
	}

	// Parse start and end dates in UTC
	startDate, err := time.Parse("2006-01-02", *startDateStr)
	if err != nil {
		log.Fatalf("Invalid start date format: %v", err)
	}
	startDate = startDate.UTC()
	endDate, err := time.Parse("2006-01-02", *endDateStr)
	if err != nil {
		log.Fatalf("Invalid end date format: %v", err)
	}
	endDate = endDate.UTC().AddDate(0, 0, 1).Add(-time.Second) // End of the end date

	// Get API key from environment variable
	apiKey := os.Getenv("OPSGENIE_API_KEY")
	if apiKey == "" {
		log.Fatal("OPSGENIE_API_KEY environment variable not set.")
	}

	// Initialize HTTP client
	client := &http.Client{
		Timeout: time.Second * 30,
	}

	// Initialize map to hold person data
	personMap := make(map[string]*PersonData)

	// Iterate over each hour in the date range
	for current := startDate; !current.After(endDate); current = current.Add(time.Hour) {
		// Format date to RFC3339
		formattedDate := current.Format(time.RFC3339)

		// Build API request URL with flat=true
		url := fmt.Sprintf("https://api.opsgenie.com/v2/schedules/%s/on-calls?date=%s&flat=true",
			*scheduleID, formattedDate)

		// Create new HTTP request
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			log.Fatalf("Failed to create request: %v", err)
		}

		// Set headers
		req.Header.Set("Authorization", "GenieKey "+apiKey)
		req.Header.Set("Content-Type", "application/json")

		// Implement retry mechanism for 429 errors
		var resp *http.Response
		maxRetries := 5
		retries := 0
		backoff := time.Second * 2

		for {
			// Make the API call
			resp, err = client.Do(req)
			if err != nil {
				log.Fatalf("API request failed: %v", err)
			}

			// Read response body
			body, err := io.ReadAll(io.Reader(resp.Body))
			if err != nil {
				log.Fatalf("Failed to read response body: %v", err)
			}
			resp.Body.Close()

			// Handle rate limiting
			if resp.StatusCode == http.StatusTooManyRequests {
				log.Printf("Rate limited. Retrying in %v...", backoff)
				if retries >= maxRetries {
					log.Fatalf("Exceeded maximum retries due to rate limiting.")
				}
				retries++
				time.Sleep(backoff)
				backoff *= 2
				continue
			}

			// Check for non-200 status codes
			if resp.StatusCode != http.StatusOK {
				log.Fatalf("API response status: %s, body: %s", resp.Status, string(body))
			}

			// Parse JSON response
			var onCallResp OnCallResponse
			err = json.Unmarshal(body, &onCallResp)
			if err != nil {
				log.Fatalf("Failed to parse JSON: %v", err)
			}

			// Process each on-call recipient
			for _, recipient := range onCallResp.Data.OnCallRecipients {
				userName := recipient
				if userName == "" {
					continue
				}
				if _, exists := personMap[userName]; !exists {
					personMap[userName] = &PersonData{Name: userName, TotalHours: 0}
				}
				personMap[userName].TotalHours += 1.0
			}

			break
		}

		delay := time.Duration(rand.Intn(500)+500) * time.Millisecond
		time.Sleep(delay)
		fmt.Printf("\rProcessed date: %s", formattedDate)
	}

	// Initialize totals
	var totalHours float64
	for _, pdata := range personMap {
		totalHours += pdata.TotalHours
	}

	totalDays := totalHours / 24
	totalWeeks := totalDays / 7

	// Print report
	fmt.Println("\n\nOn-Call Report")
	fmt.Println("==============")
	fmt.Printf("Period: %s to %s\n\n", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
	fmt.Printf("%-40s %-15s\n", "Name", "Total Hours")
	fmt.Println("-------------------------------------------------------------")
	for _, pdata := range personMap {
		fmt.Printf("%-40s %-15.2f\n", pdata.Name, pdata.TotalHours)
	}
	fmt.Println("\n-------------------------------------------------------------")
	fmt.Printf("Total Hours: %.2f\n", totalHours)
	fmt.Printf("Total Days: %.2f\n", totalDays)
	fmt.Printf("Total 7-Day Weeks: %.2f\n", totalWeeks)
}
