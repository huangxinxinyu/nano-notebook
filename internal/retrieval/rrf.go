package retrieval

import (
	"errors"
	"sort"
	"strings"
)

func FuseRRF(channels map[string][]Candidate, rankConstant int) ([]Candidate, error) {
	if len(channels) == 0 || rankConstant <= 0 {
		return nil, errors.New("invalid RRF input")
	}
	channelNames := make([]string, 0, len(channels))
	for name := range channels {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("RRF channel name is required")
		}
		channelNames = append(channelNames, name)
	}
	sort.Strings(channelNames)
	scores := make(map[string]float64)
	for _, name := range channelNames {
		seen := make(map[string]struct{})
		for rank, candidate := range channels[name] {
			if strings.TrimSpace(candidate.ID) == "" {
				return nil, errors.New("RRF candidate identity is required")
			}
			if _, exists := seen[candidate.ID]; exists {
				return nil, errors.New("duplicate RRF candidate identity in one channel")
			}
			seen[candidate.ID] = struct{}{}
			scores[candidate.ID] += 1 / float64(rankConstant+rank+1)
		}
	}
	result := make([]Candidate, 0, len(scores))
	for id, score := range scores {
		result = append(result, Candidate{ID: id, Score: score})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return result[i].ID < result[j].ID
		}
		return result[i].Score > result[j].Score
	})
	return result, nil
}
