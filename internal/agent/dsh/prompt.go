package dsh

import (
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/go-aie/teamx/agent"
)

type ContentBlock = agent.ContentBlock

func (s *Session) validatePrompt(prompt []ContentBlock) error {
	if len(prompt) == 0 {
		return errors.New("empty DSH prompt")
	}
	textBytes, imageBytes := 0, 0
	for _, block := range prompt {
		switch block.Type {
		case "text":
			if block.MIMEType != "" || len(block.Data) != 0 || !utf8.ValidString(block.Text) || len(block.Text) > s.opts.Limits.InputBytes-textBytes {
				return errors.New("invalid or oversized DSH text input")
			}
			textBytes += len(block.Text)
		case "image":
			if !s.SupportsImages() {
				return errors.New("DSH image input requires model configuration and ACP promptCapabilities.image")
			}
			if block.Text != "" || len(block.Data) == 0 || len(block.Data) > s.opts.Limits.ImageBytes-imageBytes {
				return errors.New("invalid or oversized DSH image input")
			}
			switch block.MIMEType {
			case "image/png", "image/jpeg", "image/gif", "image/webp":
			default:
				return errors.New("unsupported DSH image MIME type")
			}
			if http.DetectContentType(block.Data) != block.MIMEType {
				return errors.New("DSH image data does not match MIME type")
			}
			imageBytes += len(block.Data)
		default:
			return errors.New("unsupported DSH prompt block type")
		}
	}
	// connection.write checks the complete JSON-RPC frame after base64 expansion,
	// including escaped text, session ID and request ID, before any transport write.
	return nil
}
