// Phone mode (?phone=1): the phone app's Explore tab runs this frontend
// in its WebView, served by the Mac over the phone's connection
// (docs/phone.md, part 3). Explore and Ask only, read-only: no desktop
// chrome, no edits, no audio (the Mac's media isn't served).
export const phoneMode = new URLSearchParams(window.location.search).get('phone') === '1';
