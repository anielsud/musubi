package item

import "testing"

// A phone recording's named event reaches the item, for linking; other
// recordings translate as before (no key at all).
func TestTomoeEventID(t *testing.T) {
	its, err := fromTomoe([]byte(`{"title":"Phone Meeting 2026-11-17 10:02","platform":"phone","created_at":"2026-11-17T10:02:00+01:00","duration":60,"event_id":"calendar:1a2b","segments":[]}`))
	if err != nil || its[0].Native["event_id"] != "calendar:1a2b" || its[0].Native["platform"] != "phone" {
		t.Fatalf("%v %v", its, err)
	}
	its, _ = fromTomoe([]byte(`{"title":"Teams Meeting","created_at":"2026-11-17T10:02:00Z","segments":[]}`))
	if _, ok := its[0].Native["event_id"]; ok {
		t.Error("event_id on a recorded meeting")
	}
}
