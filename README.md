# OpsGenie On-Call Report Generator

This program generates a report of on-call hours for each person in an OpsGenie schedule over a specified date range.

## Usage

To run the program, use the following command:

```
./run -start 2024-12-01 -end 2024-12-31 -schedule e7b1c218-a13a-4c4a-a787-6cf2f6464962
```

## Environment Variables

The program requires the `OPSGENIE_API_KEY` environment variable to be set with your OpsGenie API key.

Example:

```
export OPSGENIE_API_KEY=your_api_key_here
```

## Command-Line Arguments

- `-start`: Start date (YYYY-MM-DD)
- `-end`: End date (YYYY-MM-DD)
- `-schedule`: OpsGenie Schedule ID (UUID)

## How It Works

The program pulls data from the OpsGenie API for each hour within the specified date range. It uses the `flat=true` parameter to get a flat list of on-call recipients for each hour.

To prevent hitting the API rate limit (HTTP 429 errors), the program implements a retry mechanism with exponential backoff. Additionally, a random delay between 500ms and 1000ms is added between API calls to further reduce the likelihood of rate limiting.

## Example

```
./run -start 2024-12-01 -end 2024-12-31 -schedule e7b1c218-a13a-4c4a-a787-6cf2f6464962
```