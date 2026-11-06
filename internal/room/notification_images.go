package room

import (
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
)

type notificationImage struct {
	Path, Identity string
	Supported      bool
	Positions      []int
}
type notificationImageReferences struct {
	Images              []notificationImage
	References, Omitted int
}

func findNotificationImages(saved string, source platform.SourceRef) notificationImageReferences {
	var result notificationImageReferences
	switch source.Provider {
	case "gitlab":
		found := gitlab.FindNotificationImages(saved, source, gitlab.MaxImageReferences)
		result.References, result.Omitted = found.References, found.Omitted
		for _, image := range found.Images {
			identity, _ := json.Marshal(image.Upload)
			result.Images = append(result.Images, notificationImage{image.Path, string(identity), image.Supported, image.Positions})
		}
	case "lark":
		found := lark.FindNotificationImages(saved, source, lark.MaxImageReferences)
		result.References, result.Omitted = found.References, found.Omitted
		for _, image := range found.Images {
			result.Images = append(result.Images, notificationImage{lark.ImagePrefix + image.Key, source.Reference.Target.ID.Value + "/" + image.Key, true, image.Positions})
		}
	}
	return result
}
