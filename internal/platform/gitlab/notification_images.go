package gitlab

import (
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

const MaxImageReferences = 100

// NotificationImage is transient discovery evidence, never a durable attachment
// list. Positions are one-based image occurrences in the saved Markdown.
// Unsupported destinations remain visible as missing material, without fetching.
type NotificationImage struct {
	Path      string
	Upload    Upload
	Supported bool
	Positions []int
}

type NotificationImages struct {
	Images     []NotificationImage
	References int
	Omitted    int
}

// FindNotificationImages parses only saved Markdown. CommonMark handles inline,
// full/collapsed/shortcut references and excludes code and ordinary links. A
// trailing GitLab {width=... height=...} leaves the preceding image unchanged.
// Incomplete references are never repaired using a URL elsewhere in the text.
func FindNotificationImages(saved string, source platform.SourceRef, limit int) NotificationImages {
	limit = max(0, min(limit, MaxImageReferences))
	input := []byte(NotificationMarkdown(saved))
	doc := goldmark.DefaultParser().Parse(text.NewReader(input))
	result := NotificationImages{}
	byUpload := map[Upload]int{}
	byUnsupported := map[string]int{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		img, ok := n.(*ast.Image)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		result.References++
		if result.References > limit {
			result.Omitted++
			return ast.WalkContinue, nil
		}
		path := string(img.Destination)
		upload, supported := FindUpload(saved, source, path)
		// A cut reference definition can still be valid Markdown. Never fetch a
		// filename ending in the formatter's ellipsis, including in old records;
		// this does not infer or rewrite the old body's truncation status.
		if strings.HasSuffix(upload.Filename, "…") {
			supported = false
			upload = Upload{}
		}
		index, found := byUnsupported[path]
		if supported {
			index, found = byUpload[upload]
		}
		if found {
			result.Images[index].Positions = append(result.Images[index].Positions, result.References)
		} else {
			index = len(result.Images)
			result.Images = append(result.Images, NotificationImage{Path: path, Upload: upload, Supported: supported, Positions: []int{result.References}})
			if supported {
				byUpload[upload] = index
			} else {
				byUnsupported[path] = index
			}
		}
		return ast.WalkContinue, nil
	})
	return result
}
