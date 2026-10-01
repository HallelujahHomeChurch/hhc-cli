package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

func recordingProgress(diagnostics io.Writer, jsonMode bool, operationID string) func(media.EncodingProgress) {
	return func(p media.EncodingProgress) {
		if jsonMode {
			_ = json.NewEncoder(diagnostics).Encode(struct {
				Type        string `json:"type"`
				OperationID string `json:"operationId"`
				media.EncodingProgress
			}{"encoding_progress", operationID, p})
		} else {
			fmt.Fprintf(diagnostics, "轉檔 %s %.0f%% · %s · %.2fx\n", p.Rendition, p.Fraction*100, p.Encoder, p.Speed)
		}
	}
}
