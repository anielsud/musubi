package meeting

// Platform names the app a call runs in. The values are written into saved
// sessions and logs, so they stay fixed.
type Platform string

// The platforms the recorder can tell apart. PlatformUnknown covers an app
// with a call in progress that matches none of the others.
const (
	PlatformTeams   Platform = "Teams"
	PlatformMeet    Platform = "Meet"
	PlatformZoom    Platform = "Zoom"
	PlatformWebex   Platform = "Webex"
	PlatformSlack   Platform = "Slack"
	PlatformUnknown Platform = "Unknown"
)
