package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

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

// Structs for whoisoncall command

// List schedules API
type SchedulesResponse struct {
	Data      []Schedule `json:"data"`
	Took      float64    `json:"took"`
	RequestID string     `json:"requestId"`
}

type Schedule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Timezone string `json:"timezone"`
}

// Next on-call API
type NextOnCallResponse struct {
	Data      NextOnCallData `json:"data"`
	Took      float64        `json:"took"`
	RequestID string         `json:"requestId"`
}

type NextOnCallData struct {
	Parent           Parent   `json:"_parent"`
	OnCallRecipients []string `json:"onCallRecipients"`
}

// Timeline API (for shift end detection)
type TimelineResponse struct {
	Data      TimelineData `json:"data"`
	Took      float64      `json:"took"`
	RequestID string       `json:"requestId"`
}

type TimelineData struct {
	FinalTimeline Timeline `json:"finalTimeline"`
}

type Timeline struct {
	Rotations []TimelineRotation `json:"rotations"`
}

type TimelineRotation struct {
	Periods []RotationPeriod `json:"periods"`
}

type RotationPeriod struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

// Display struct
type ScheduleStatus struct {
	ScheduleID    string
	ScheduleName  string
	CurrentOnCall []string
	NextOnCall    []string
	ShiftEndsAt   time.Time
	ShiftEndsSoon bool // true if ends within 1 hour
}

// Helper functions

func createHTTPClient() *http.Client {
	return &http.Client{
		Timeout: time.Second * 30,
	}
}

func makeAPIRequestWithRetry(client *http.Client, url, apiKey string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "GenieKey "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	maxRetries := 5
	retries := 0
	backoff := time.Second * 2

	for {
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		// Handle rate limiting
		if resp.StatusCode == http.StatusTooManyRequests {
			if retries >= maxRetries {
				return nil, fmt.Errorf("exceeded maximum retries due to rate limiting")
			}
			log.Printf("Rate limited. Retrying in %v...", backoff)
			retries++
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		// Check for non-200 status codes
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API response status: %s, body: %s", resp.Status, string(body))
		}

		return body, nil
	}
}

func printUsage() {
	fmt.Println("OpsGenie On-Call Tool")
	fmt.Println("\nUsage:")
	fmt.Println("  opsgenie-on-call <command> [flags]")
	fmt.Println("\nCommands:")
	fmt.Println("  oncall        Generate on-call report for a schedule over a date range")
	fmt.Println("  whoisoncall   Show current on-call person for schedules (uses default filter)")
	fmt.Println("\noncall flags:")
	fmt.Println("  -start      Start date (YYYY-MM-DD)")
	fmt.Println("  -end        End date (YYYY-MM-DD)")
	fmt.Println("  -schedule   OpsGenie Schedule ID (UUID)")
	fmt.Println("\nwhoisoncall flags:")
	fmt.Println("  -filter    Comma-separated list of schedule names/IDs (default: key schedules)")
	fmt.Println("             Use -filter \"\" to show all schedules")
	fmt.Println("\nExamples:")
	fmt.Println("  opsgenie-on-call oncall -start 2024-12-01 -end 2024-12-31 -schedule abc-123")
	fmt.Println("  opsgenie-on-call whoisoncall")
	fmt.Println("  opsgenie-on-call whoisoncall -filter \"\"")
	fmt.Println("  opsgenie-on-call whoisoncall -filter \"Production,Database\"")
	fmt.Println("\nEnvironment Variables:")
	fmt.Println("  OPSGENIE_API_KEY    OpsGenie API key (required)")
}

func runOnCallCommand(args []string) {
	// Create flag set for oncall subcommand
	oncallFlags := flag.NewFlagSet("oncall", flag.ExitOnError)
	startDateStr := oncallFlags.String("start", "", "Start date (YYYY-MM-DD)")
	endDateStr := oncallFlags.String("end", "", "End date (YYYY-MM-DD)")
	scheduleID := oncallFlags.String("schedule", "", "OpsGenie Schedule ID (UUID)")

	oncallFlags.Parse(args)

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
	client := createHTTPClient()

	// Initialize map to hold person data
	personMap := make(map[string]*PersonData)

	// Iterate over each hour in the date range
	for current := startDate; !current.After(endDate); current = current.Add(time.Hour) {
		// Format date to RFC3339
		formattedDate := current.Format(time.RFC3339)

		// Build API request URL with flat=true
		url := fmt.Sprintf("https://api.opsgenie.com/v2/schedules/%s/on-calls?date=%s&flat=true",
			*scheduleID, formattedDate)

		body, err := makeAPIRequestWithRetry(client, url, apiKey)
		if err != nil {
			log.Fatalf("API request failed: %v", err)
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

// Functions for whoisoncall command

func fetchAllSchedules(client *http.Client, apiKey string) ([]Schedule, error) {
	url := "https://api.opsgenie.com/v2/schedules"
	body, err := makeAPIRequestWithRetry(client, url, apiKey)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch schedules: %w", err)
	}

	var schedulesResp SchedulesResponse
	err = json.Unmarshal(body, &schedulesResp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse schedules response: %w", err)
	}

	return schedulesResp.Data, nil
}

func matchesFilter(schedule Schedule, filters []string) bool {
	if len(filters) == 0 {
		return true
	}

	scheduleName := strings.ToLower(schedule.Name)
	scheduleID := strings.ToLower(schedule.ID)

	for _, filter := range filters {
		filterLower := strings.ToLower(strings.TrimSpace(filter))
		// Exact match for schedule name or substring match for ID
		if scheduleName == filterLower || strings.Contains(scheduleID, filterLower) {
			return true
		}
	}
	return false
}

func checkShiftEndsSoon(client *http.Client, apiKey, scheduleID string, now time.Time) (time.Time, bool) {
	// Request timeline from now to +2 hours
	url := fmt.Sprintf(
		"https://api.opsgenie.com/v2/schedules/%s/timeline?date=%s&interval=2&intervalUnit=hours",
		scheduleID,
		now.Format(time.RFC3339),
	)

	body, err := makeAPIRequestWithRetry(client, url, apiKey)
	if err != nil {
		return time.Time{}, false
	}

	var timeline TimelineResponse
	err = json.Unmarshal(body, &timeline)
	if err != nil {
		return time.Time{}, false
	}

	// Check periods in finalTimeline
	for _, rotation := range timeline.Data.FinalTimeline.Rotations {
		for _, period := range rotation.Periods {
			periodStart, err1 := time.Parse(time.RFC3339, period.StartDate)
			periodEnd, err2 := time.Parse(time.RFC3339, period.EndDate)

			if err1 != nil || err2 != nil {
				continue
			}

			// Check if this is the current period
			if (periodStart.Before(now) || periodStart.Equal(now)) && periodEnd.After(now) {
				duration := periodEnd.Sub(now)
				if duration <= time.Hour {
					return periodEnd, true
				}
				return periodEnd, false
			}
		}
	}

	return time.Time{}, false
}

func fetchScheduleStatus(client *http.Client, apiKey string, schedule Schedule) *ScheduleStatus {
	status := &ScheduleStatus{
		ScheduleID:   schedule.ID,
		ScheduleName: schedule.Name,
	}

	now := time.Now().UTC()

	// Fetch current on-call
	currentURL := fmt.Sprintf("https://api.opsgenie.com/v2/schedules/%s/on-calls?flat=true&date=%s",
		schedule.ID, now.Format(time.RFC3339))

	body, err := makeAPIRequestWithRetry(client, currentURL, apiKey)
	if err != nil {
		log.Printf("Warning: Failed to fetch on-call for schedule %s: %v", schedule.Name, err)
		status.CurrentOnCall = []string{"(error fetching)"}
		return status
	}

	var onCallResp OnCallResponse
	err = json.Unmarshal(body, &onCallResp)
	if err != nil {
		log.Printf("Warning: Failed to parse on-call response for schedule %s: %v", schedule.Name, err)
		status.CurrentOnCall = []string{"(parse error)"}
		return status
	}

	if len(onCallResp.Data.OnCallRecipients) == 0 {
		status.CurrentOnCall = []string{"No one on call"}
	} else {
		status.CurrentOnCall = onCallResp.Data.OnCallRecipients
	}

	// Check shift timing
	shiftEnd, endsSoon := checkShiftEndsSoon(client, apiKey, schedule.ID, now)
	status.ShiftEndsAt = shiftEnd
	status.ShiftEndsSoon = endsSoon

	// Fetch next on-call if shift ends soon
	if endsSoon {
		nextURL := fmt.Sprintf("https://api.opsgenie.com/v2/schedules/%s/next-on-calls?flat=true",
			schedule.ID)
		nextBody, err := makeAPIRequestWithRetry(client, nextURL, apiKey)
		if err != nil {
			log.Printf("Warning: Failed to fetch next on-call for schedule %s: %v", schedule.Name, err)
		} else {
			var nextResp NextOnCallResponse
			err = json.Unmarshal(nextBody, &nextResp)
			if err != nil {
				log.Printf("Warning: Failed to parse next on-call response for schedule %s: %v", schedule.Name, err)
			} else {
				status.NextOnCall = nextResp.Data.OnCallRecipients
			}
		}
	}

	return status
}

func fetchAllScheduleStatuses(client *http.Client, apiKey string, schedules []Schedule) []*ScheduleStatus {
	// Limit concurrent requests to avoid rate limiting
	semaphore := make(chan struct{}, 3)
	results := make(chan *ScheduleStatus, len(schedules))
	var wg sync.WaitGroup

	for _, schedule := range schedules {
		wg.Add(1)
		go func(sched Schedule) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			status := fetchScheduleStatus(client, apiKey, sched)
			results <- status

			// Small delay to avoid rate limiting
			time.Sleep(time.Millisecond * 100)
		}(schedule)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var statuses []*ScheduleStatus
	for status := range results {
		statuses = append(statuses, status)
	}

	return statuses
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatRecipients(recipients []string) string {
	if len(recipients) == 0 {
		return ""
	}
	// Strip @behavox.com from emails to save space
	var cleanedRecipients []string
	for _, recipient := range recipients {
		cleaned := strings.TrimSuffix(recipient, "@behavox.com")
		cleanedRecipients = append(cleanedRecipients, cleaned)
	}
	return strings.Join(cleanedRecipients, ", ")
}

func cleanScheduleName(name string) string {
	// Remove common suffixes to make names cleaner
	name = strings.TrimSuffix(name, " Schedule")
	name = strings.TrimSuffix(name, " schedule")
	name = strings.TrimSuffix(name, "_schedule")
	return name
}

func printScheduleStatusTable(statuses []*ScheduleStatus) {
	// Sort by schedule name
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].ScheduleName < statuses[j].ScheduleName
	})

	// Print header
	fmt.Printf("%-40s %-50s %-50s\n", "Team Name", "Current On-Call", "Next On-Call")
	fmt.Println(strings.Repeat("=", 140))

	for _, status := range statuses {
		cleanName := cleanScheduleName(status.ScheduleName)
		scheduleName := truncate(cleanName, 38)
		currentOnCall := formatRecipients(status.CurrentOnCall)

		nextOnCall := ""
		if status.ShiftEndsSoon && len(status.NextOnCall) > 0 {
			timeRemaining := time.Until(status.ShiftEndsAt)
			minutes := int(timeRemaining.Minutes())
			nextRecipients := formatRecipients(status.NextOnCall)
			nextOnCall = fmt.Sprintf("%s (in %dm)", nextRecipients, minutes)
		}

		fmt.Printf("%-40s %-50s %-50s\n", scheduleName, currentOnCall, nextOnCall)
	}
}

func runWhoIsOnCallCommand(args []string) {
	// Create flag set for whoisoncall subcommand
	whoisFlags := flag.NewFlagSet("whoisoncall", flag.ExitOnError)
	filterFlag := whoisFlags.String("filter", "", "Comma-separated list of schedule names or IDs to filter")

	whoisFlags.Parse(args)

	// Parse filter or use default
	var filters []string

	// Check if filter flag was explicitly set
	filterProvided := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "-filter") {
			filterProvided = true
			break
		}
	}

	if filterProvided && *filterFlag == "" {
		// User explicitly passed -filter "" to show all schedules
		filters = []string{}
	} else if *filterFlag != "" {
		// User provided specific filters
		filters = strings.Split(*filterFlag, ",")
	} else {
		// Default filter
		filters = []string{
			"Archiving Team Schedule",
			"DIP Ingestion schedule",
			"DIP Processing schedule",
			"L1 - Customer Support",
			"NextGen SRE Team_schedule",
			"Pathfinder_schedule",
			"Quantum A-Team schedule",
			"Quantum S-Team schedule",
		}
	}

	// Get API key from environment variable
	apiKey := os.Getenv("OPSGENIE_API_KEY")
	if apiKey == "" {
		log.Fatal("OPSGENIE_API_KEY environment variable not set.")
	}

	// Create HTTP client
	client := createHTTPClient()

	// Fetch all schedules
	schedules, err := fetchAllSchedules(client, apiKey)
	if err != nil {
		log.Fatalf("Failed to fetch schedules: %v", err)
	}

	// Filter schedules
	var filteredSchedules []Schedule
	for _, schedule := range schedules {
		if matchesFilter(schedule, filters) {
			filteredSchedules = append(filteredSchedules, schedule)
		}
	}

	if len(filteredSchedules) == 0 {
		fmt.Println("No schedules found matching the filter criteria.")
		return
	}

	// Fetch statuses for all filtered schedules
	statuses := fetchAllScheduleStatuses(client, apiKey, filteredSchedules)

	// Print results
	printScheduleStatusTable(statuses)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]

	switch subcommand {
	case "oncall":
		runOnCallCommand(os.Args[2:])
	case "whoisoncall":
		runWhoIsOnCallCommand(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}
