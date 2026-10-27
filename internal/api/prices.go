package api

import "strings"

// Price is USD per million tokens.
type Price struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CacheWrite  float64 `json:"cache_write"`
	CacheWrite1 float64 `json:"cache_write_1h"`
	CacheRead   float64 `json:"cache_read"`
}

const WebSearchPrice = 0.01 // USD per search

// DefaultPrices is keyed by model id prefix; longest prefix wins.
var DefaultPrices = map[string]Price{
	"claude-fable-5-1":  {10, 50, 12.5, 20, 0.25},
	"claude-mythos-5-1": {10, 50, 12.5, 20, 0.25},
	"claude-fable-5":    {10, 50, 12.5, 20, 1},
	"claude-mythos-5":   {10, 50, 12.5, 20, 1},
	"claude-opus-5-5":   {4, 20, 5, 8, 0.2},
	"claude-opus-5":     {5, 25, 6.25, 10, 0.5},
	"claude-opus-4":     {5, 25, 6.25, 10, 0.5},
	"claude-opus-4-1":   {15, 75, 18.75, 30, 1.5},
	"claude-opus-4-0":   {15, 75, 18.75, 30, 1.5},
	"claude-sonnet-5-5": {2, 10, 2.5, 4, 0.1},
	"claude-sonnet-5":   {2, 10, 2.5, 4, 0.2},
	"claude-sonnet-4":   {3, 15, 3.75, 6, 0.3},
	"claude-haiku-5-5":  {0.1, 0.5, 0.125, 0.2, 0.01},
	"claude-haiku-4-5":  {1, 5, 1.25, 2, 0.1},
	"claude-haiku-3-5":  {0.8, 4, 1, 1.6, 0.08},
	"claude-3-5-haiku":  {0.8, 4, 1, 1.6, 0.08},
}

// PriceFor finds the price for a model id; extra overrides the table.
func PriceFor(model string, extra map[string]Price) (Price, bool) {
	best, blen, ok := Price{}, -1, false
	look := func(m map[string]Price) {
		for k, p := range m {
			if strings.HasPrefix(model, k) && len(k) > blen {
				best, blen, ok = p, len(k), true
			}
		}
	}
	look(DefaultPrices)
	look(extra)
	return best, ok
}

// Cost prices one call's usage in USD.
func Cost(model string, u Usage, extra map[string]Price) float64 {
	p, ok := PriceFor(model, extra)
	if !ok {
		return 0
	}
	mult := 1.0
	if strings.HasPrefix(model, "claude-haiku-5-5") && u.TotalInput() > 100_000 {
		mult = 5
	}
	w5 := u.CacheWrite - u.CacheWrite1h
	if w5 < 0 {
		w5 = 0
	}
	c := float64(u.Input)*p.Input + float64(u.Output)*p.Output +
		float64(w5)*p.CacheWrite + float64(u.CacheWrite1h)*p.CacheWrite1 +
		float64(u.CacheRead)*p.CacheRead
	return c*mult/1e6 + float64(u.WebSearches)*WebSearchPrice
}
