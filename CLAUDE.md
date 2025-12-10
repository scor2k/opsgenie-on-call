# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go-based CLI tool that generates on-call hours reports from OpsGenie schedules. It queries the OpsGenie API hourly over a date range and aggregates total on-call hours per person.

## Building and Running

- **Run the program**: `./run -start 2024-12-01 -end 2024-12-31 -schedule <SCHEDULE_ID>`
  - The `run` script sources `env.sh` (which sets `OPSGENIE_API_KEY`) and executes `go run .`
- **Build binary**: `go build -o opsgenie-on-call main.go`
- **Run tests**: `go test ./...` (currently no tests exist)
- **Install dependencies**: `go mod download`
- **Update dependencies**: `go mod tidy`

## Environment Configuration

The program requires the `OPSGENIE_API_KEY` environment variable. The `env.sh` file contains this key and is sourced by the `run` script.

**Important**: `env.sh` contains an actual API key and should never be committed to version control (though it currently is in this repo).

## Code Architecture

### Single-File Design

The entire application is in `main.go` (~185 lines). There is no modular structure - all logic exists in the main package.

### Key Components

1. **Data Structures** (lines 18-39):
   - `OnCallResponse`, `OnCallData`, `Parent`: Parse OpsGenie API responses
   - `PersonData`: Aggregates hours per person (Name + TotalHours)

2. **Main Flow** (lines 41-184):
   - Parse CLI flags: `-start`, `-end`, `-schedule`
   - Validate dates and API key
   - Iterate hourly from start to end date (line 80)
   - For each hour:
     - Query OpsGenie API with `flat=true` parameter (line 85)
     - Implement retry logic for HTTP 429 rate limiting (lines 98-155)
     - Add random delay (500-1000ms) between requests to avoid rate limits (lines 157-158)
   - Aggregate hours per person in `personMap` (line 77)
   - Print formatted report with totals

3. **Rate Limiting Strategy**:
   - Exponential backoff on HTTP 429 errors (starts at 2s, doubles each retry, max 5 retries)
   - Random delays (500-1000ms) between all requests to preemptively avoid rate limits
   - This approach is critical since the API can return 429 errors under load

### API Details

- **Endpoint**: `https://api.opsgenie.com/v2/schedules/{scheduleID}/on-calls`
- **Query Parameters**: `date=<RFC3339>&flat=true`
  - The `flat=true` parameter returns a flat list of on-call recipients (critical for correct aggregation)
- **Authentication**: `Authorization: GenieKey <API_KEY>` header
- **Date Format**: RFC3339 (e.g., `2024-12-01T00:00:00Z`)

### Date Handling

- All dates are parsed and processed in UTC (lines 54-63)
- End date is adjusted to include the full last day: `endDate.AddDate(0, 0, 1).Add(-time.Second)`
- Iteration happens hourly using `current.Add(time.Hour)`

## Common Modifications

- **Change output format**: Modify lines 172-183 (the print report section)
- **Adjust rate limiting**: Modify `backoff` initialization (line 102), `maxRetries` (line 100), or delay range (line 157)
- **Add additional data fields**: Update structs (lines 18-39) and API parsing (lines 136-152)
- **Change aggregation logic**: Modify the person map updates (lines 143-152)

## Dependencies

- `golang.org/x/exp`: Used only for `rand.Intn()` for random delays between API calls
- Standard library: `flag`, `fmt`, `log`, `net/http`, `time`, `encoding/json`, `io`, `os`

## Known Limitations

- No test coverage
- API key is hardcoded in `env.sh` (should use secure secret management)
- No progress indication beyond console output
- No support for multiple schedules in a single run
- Error handling terminates the program (`log.Fatal`) rather than continuing with partial results
