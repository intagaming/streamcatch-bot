package broadcaster

import "strings"

func isMediaServersFault(stderr string) bool {
	return strings.Contains(stderr, "Connection refused") || strings.Contains(stderr, "Broken pipe")
}
