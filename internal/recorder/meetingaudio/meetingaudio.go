package meetingaudio

// NoSource turns system-audio capture off for a recording, so only the
// microphone is heard. config's meeting.monitor_device may hold it. It is
// kept apart from "", the platform's default source, so choosing no
// system audio is never mistaken for choosing the default.
const NoSource = "none"

// AutoSource is the selection that captures the meeting app (Teams, Zoom,
// Webex...) when one is making sound, and the whole system's audio
// otherwise, moving to the app once it starts: the macOS default, so
// notification sounds and music stay out of meeting transcripts.
const AutoSource = "auto"
