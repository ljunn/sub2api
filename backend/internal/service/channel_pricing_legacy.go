package service

import "encoding/json"

// Preview and production share group JSON. Read legacy entries written by the
// production binary, and mirror max on new writes until both versions converge.
func (p *ChannelModelPricing) UnmarshalJSON(data []byte) error {
	type plain ChannelModelPricing
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, generic := fields["reasoning_effort_multipliers"]; !generic {
		var legacy *float64
		if raw, exists := fields["max_reasoning_effort_multiplier"]; exists {
			if err := json.Unmarshal(raw, &legacy); err != nil {
				return err
			}
		}
		if legacy != nil {
			decoded.ReasoningEffortMultipliers = map[string]float64{"max": *legacy}
		}
	}
	*p = ChannelModelPricing(decoded)
	return nil
}

func (p ChannelModelPricing) MarshalJSON() ([]byte, error) {
	type plain ChannelModelPricing
	var legacy *float64
	if max, exists := p.ReasoningEffortMultipliers["max"]; exists {
		legacy = &max
	}
	return json.Marshal(struct {
		plain
		Legacy *float64 `json:"max_reasoning_effort_multiplier,omitempty"`
	}{plain: plain(p), Legacy: legacy})
}
