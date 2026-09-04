package plugin

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

const schedulerStateCapacity = 4096
const maxSchedulerWeight = 1_000_000

type smoothWeightedState struct {
	current map[string]int64
}

func (a *App) clearSchedulerState() {
	a.schedulerMu.Lock()
	a.schedulerRR = make(map[string]*smoothWeightedState)
	a.schedulerMu.Unlock()
}

func (a *App) pickSmoothWeighted(req SchedulerPickRequest, group string, priority int, candidates []SchedulerAuthCandidate) SchedulerAuthCandidate {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })

	total := int64(0)
	active := make(map[string]struct{}, len(candidates))
	for _, cand := range candidates {
		total += int64(schedulerCandidateWeight(cand))
		active[cand.ID] = struct{}{}
	}

	poolKey := schedulerPoolKey(req, group, priority)
	a.schedulerMu.Lock()
	defer a.schedulerMu.Unlock()
	if a.schedulerRR == nil {
		a.schedulerRR = make(map[string]*smoothWeightedState)
	}
	if _, ok := a.schedulerRR[poolKey]; !ok && len(a.schedulerRR) >= schedulerStateCapacity {
		a.schedulerRR = make(map[string]*smoothWeightedState)
	}
	state := a.schedulerRR[poolKey]
	if state == nil {
		state = &smoothWeightedState{current: make(map[string]int64, len(candidates))}
		a.schedulerRR[poolKey] = state
	}
	for id := range state.current {
		if _, ok := active[id]; !ok {
			delete(state.current, id)
		}
	}

	bestIndex := 0
	bestCurrent := int64(0)
	for index, cand := range candidates {
		current := state.current[cand.ID]
		if current > total {
			current = total
		} else if current < -total {
			current = -total
		}
		current += int64(schedulerCandidateWeight(cand))
		state.current[cand.ID] = current
		if index == 0 || current > bestCurrent {
			bestIndex = index
			bestCurrent = current
		}
	}
	state.current[candidates[bestIndex].ID] -= total
	return candidates[bestIndex]
}

func schedulerPoolKey(req SchedulerPickRequest, group string, priority int) string {
	providers := append([]string(nil), req.Providers...)
	for index := range providers {
		providers[index] = strings.ToLower(strings.TrimSpace(providers[index]))
	}
	sort.Strings(providers)
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = strings.Join(providers, ",")
	}
	return provider + "\x00" + strings.ToLower(strings.TrimSpace(req.Model)) + "\x00" +
		strings.ToLower(strings.TrimSpace(group)) + "\x00" + strconv.Itoa(priority)
}

func schedulerCandidateWeight(cand SchedulerAuthCandidate) int {
	if weight, ok := parseSchedulerWeight(cand.Weight); ok {
		return weight
	}
	for key, value := range cand.Attributes {
		if strings.EqualFold(strings.TrimSpace(key), "weight") {
			if weight, ok := parseSchedulerWeight(value); ok {
				return weight
			}
		}
	}
	for key, value := range cand.Metadata {
		if strings.EqualFold(strings.TrimSpace(key), "weight") {
			if weight, ok := parseSchedulerWeight(value); ok {
				return weight
			}
		}
	}
	return 1
}

func parseSchedulerWeight(value any) (int, bool) {
	var weight int64
	switch typed := value.(type) {
	case nil:
		return 0, false
	case int:
		weight = int64(typed)
	case int8:
		weight = int64(typed)
	case int16:
		weight = int64(typed)
	case int32:
		weight = int64(typed)
	case int64:
		weight = typed
	case uint:
		if uint64(typed) > uint64(maxSchedulerWeight) {
			return maxSchedulerWeight, true
		}
		weight = int64(typed)
	case uint8:
		weight = int64(typed)
	case uint16:
		weight = int64(typed)
	case uint32:
		weight = int64(typed)
	case uint64:
		if typed > uint64(maxSchedulerWeight) {
			return maxSchedulerWeight, true
		}
		weight = int64(typed)
	case float32:
		weight = int64(typed)
	case float64:
		weight = int64(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		weight = parsed
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		weight = parsed
	default:
		return 0, false
	}
	if weight > maxSchedulerWeight {
		return maxSchedulerWeight, true
	}
	return int(weight), true
}
