package audio

import "strings"

// SlugForURI extracts the Spotify ID from either URI format and returns it
// as a filesystem-safe directory name for per-station interstitial clips.
//
//	spotify:playlist:ABC123                          -> "ABC123"
//	https://open.spotify.com/playlist/ABC123?si=xyz -> "ABC123"
func SlugForURI(uri string) string {
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		if i := strings.IndexByte(uri, '?'); i >= 0 {
			uri = uri[:i]
		}
		uri = strings.TrimRight(uri, "/")
		if i := strings.LastIndexByte(uri, '/'); i >= 0 {
			return uri[i+1:]
		}
		return ""
	}
	parts := strings.Split(uri, ":")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
