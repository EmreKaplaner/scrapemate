// File: /Users/emre/Desktop/scrapemate-main/mock/test/mock_test.go
package main

import (
	"context"
	"fmt"
	"os"

	// This import path should match your module + subfolder "mock".
	// If your go.mod says "module github.com/EmreKaplaner/scrapemate-main",
	// then you'd do:
	mockfetcher "github.com/EmreKaplaner/scrapemate/mock"

	"github.com/EmreKaplaner/scrapemate"
	gomock "go.uber.org/mock/gomock"

	// We need to import Playwright for playwright.Page
	"github.com/playwright-community/playwright-go"
)

// myTestJob is a trivial struct implementing scrapemate.IJob.
// Note: The BrowserActions signature must match exactly what scrapemate expects.
type myTestJob struct {
	scrapemate.Job
}

// This must accept playwright.Page, not interface{}.
func (j *myTestJob) BrowserActions(ctx context.Context, page playwright.Page) scrapemate.Response {
	return scrapemate.Response{
		StatusCode: 200,
		Body:       []byte("Real BrowserActions not called in mock!"),
	}
}

func main() {
	ctrl := gomock.NewController(nil)
	defer ctrl.Finish()

	// Create the mock instance for HTTPFetcher
	fetcher := mockfetcher.NewMockHTTPFetcher(ctrl)

	// Define an expectation for Fetch:
	// When Fetch is called with any context & job, return a success response.
	fetcher.EXPECT().
		Fetch(gomock.Any(), gomock.Any()).
		Return(scrapemate.Response{
			StatusCode: 200,
			Body:       []byte("Hello from the mock!"),
		}).
		AnyTimes()

	// Use the mock fetcher
	job := &myTestJob{}
	resp := fetcher.Fetch(context.Background(), job)
	fmt.Printf("Mocked fetcher responded: status=%d, body=%s\n",
		resp.StatusCode, string(resp.Body))

	// Clean exit
	os.Exit(0)
}
