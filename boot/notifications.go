package boot

import "github.com/go-aie/teamx/internal/app/platforms"

type notificationSummary struct {
	Provider   string `json:"provider"`
	Channel    string `json:"channel"`
	Schema     int    `json:"schema"`
	InputCodec string `json:"input_codec,omitempty"`
}

func notificationSummaries(catalog *platforms.Catalog) []notificationSummary {
	result := []notificationSummary{}
	for _, definition := range catalog.Definitions() {
		for _, source := range definition.Sources {
			v := notificationSummary{Provider: source.ID.Provider, Channel: source.ID.Channel, Schema: source.New().Schema()}
			if input := definition.Notifications.Input; input != nil {
				v.InputCodec = input.Codec.Descriptor().Codec
			}
			result = append(result, v)
		}
	}
	return result
}
