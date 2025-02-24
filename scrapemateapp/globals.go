package scrapemateapp

import (
	"sync"

	"github.com/go-playground/validator/v10"

	"github.com/EmreKaplaner/scrapemate"
)

const (
	DefaultConcurrency = 1
	DefaultProvider    = "memory"
)

var (
	validate *validator.Validate
	once     sync.Once
)

type ScrapeMateApp struct {
	s *scrapemate.ScrapeMate
}
