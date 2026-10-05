package media

import (
	"path/filepath"
	"slices"
)

func multiEncodeArguments(source, output string, renditions []RecordingRendition, encoder string) ([]string, error) {
	if len(renditions) < 1 || len(renditions) > 3 {
		return nil, ErrInvalidInput
	}
	var result []string
	seen := map[string]bool{}
	for _, r := range renditions {
		if seen[r.Name] {
			return nil, ErrInvalidInput
		}
		seen[r.Name] = true
		args, err := encodeArguments(source, filepath.Join(output, r.Name), r, encoder)
		if err != nil {
			return nil, err
		}
		boundary := slices.Index(args, "-map")
		if boundary < 0 {
			return nil, ErrInvalidInput
		}
		if len(result) == 0 {
			result = append(result, args[:boundary]...)
		}
		result = append(result, args[boundary:]...)
	}
	return result, nil
}
