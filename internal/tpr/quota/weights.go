package quota

import (
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/router/engine"
)

// planSize is a plan's quota relative to the provider's base paid plan. These are
// best estimates from how plans are marketed, and providers can change them. They
// only size accounts against each other in a pool's totals and forecasts: routing
// never reads them, and a pool of one plan is unaffected. internal/credentialweight
// is a traffic share for weighted round robin, not a quota size, so it is not used.
func planSize(provider, plan string) (size float64, label string) {
	plan = strings.ToLower(strings.TrimSpace(plan))
	if provider == engine.ProviderClaude {
		switch plan {
		case "free", "plan_free":
			return 0.2, "Free"
		case "pro", "plan_pro":
			return 1, "Pro"
		case "team", "plan_team":
			return 1, "Team"
		case "max", "plan_max", "max5", "plan_max5":
			return 5, "Max 5x"
		case "max20", "plan_max20":
			return 20, "Max 20x"
		}
		return 0, ""
	}
	switch plan {
	case "free", "guest":
		return 0.2, "Free"
	case "plus", "go":
		return 1, "Plus"
	case "team", "business", "enterprise", "edu":
		return 1, "Team"
	case "prolite", "pro-lite", "pro_lite", "self_serve_business_prolite":
		return 5, "Pro Lite"
	case "pro":
		return 20, "Pro"
	}
	return 0, ""
}

// planWeights turns each plan into its weight in the pool, relative to the pool's
// smallest known plan, so that a pool of one plan is 100% per account. An unknown
// plan counts as that smallest one. The note names every other weight in use.
func planWeights(provider string, plans []string) (weights map[string]float64, note string) {
	weights = make(map[string]float64, len(plans))
	smallest := 0.0
	for _, plan := range plans {
		if size, _ := planSize(provider, plan); size > 0 && (smallest == 0 || size < smallest) {
			smallest = size
		}
	}
	notes := map[string]bool{}
	for _, plan := range plans {
		size, label := planSize(provider, plan)
		if size <= 0 || smallest == 0 {
			weights[plan] = 1
			continue
		}
		weight := size / smallest
		weights[plan] = weight
		if weight != 1 {
			notes[fmt.Sprintf("%s = %gx", label, weight)] = true
		}
	}
	parts := make([]string, 0, len(notes))
	for text := range notes {
		parts = append(parts, text)
	}
	sort.Strings(parts)
	return weights, strings.Join(parts, ", ")
}
