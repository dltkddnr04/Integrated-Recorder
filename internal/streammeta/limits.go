// Package streammeta defines the shared bounds for adapter-supplied source
// metadata and its canonical timeline projection.
package streammeta

import (
	"fmt"
	"time"
	"unicode/utf8"
)

const (
	MaxTitleBytes        = 4 << 10
	MaxDescriptionBytes  = 64 << 10
	MaxTimelineRevisions = 4096
	MaxTimelineBytes     = 4 << 20
)

func ValidateText(value *string, maximum int) error {
	if value == nil {
		return nil
	}
	if !utf8.ValidString(*value) || len(*value) > maximum {
		return fmt.Errorf("source metadata text exceeds its valid size")
	}
	for _, r := range *value {
		if r == 0 {
			return fmt.Errorf("source metadata text contains an invalid character")
		}
	}
	return nil
}

func ValidateTimestamp(value *time.Time) error {
	if value != nil && (value.IsZero() || value.Year() < 1 || value.Year() > 9999) {
		return fmt.Errorf("source metadata timestamp is invalid")
	}
	return nil
}
