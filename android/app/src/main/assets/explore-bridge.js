// Explore's bridge (docs/phone.md, part 3): stands in for what Wails gives
// the desktop's page. window.go.gui.App.<Method>(...args) sends the call to
// the app, which passes it to the Mac, and returns a Promise of the result;
// window.runtime.EventsOn and friends receive the Mac's events. The app
// hands the port for this only to the https://musubi.local origin, after
// the page has loaded; calls made before then wait for it.
(function () {
  'use strict';
  if (window.location.origin !== 'https://musubi.local') return;
  var port = null;
  var queue = [];
  var next = 0;
  var waiting = new Map();
  var listeners = new Map();

  function post(m) {
    var s = JSON.stringify(m);
    if (port) port.postMessage(s); else queue.push(s);
  }

  function off(name, l) {
    var ls = listeners.get(name);
    if (!ls) return;
    var i = ls.indexOf(l);
    if (i >= 0) ls.splice(i, 1);
    if (ls.length === 0) listeners.delete(name);
  }

  function on(name, fn, times) {
    var l = { fn: fn, left: times };
    if (!listeners.has(name)) listeners.set(name, []);
    listeners.get(name).push(l);
    return function () { off(name, l); };
  }

  function received(ev) {
    var m;
    try { m = JSON.parse(ev.data); } catch (e) { return; }
    if (!m || typeof m !== 'object') return;
    if (m.t === 'r') {
      var w = waiting.get(m.id);
      if (!w) return;
      waiting.delete(m.id);
      if (m.ok) w.resolve(m.result); else w.reject(m.message || m.error || 'failed');
    } else if (m.t === 'e' && typeof m.name === 'string') {
      var ls = listeners.get(m.name);
      if (!ls) return;
      ls.slice().forEach(function (l) {
        if (l.left > 0 && --l.left === 0) off(m.name, l);
        try { l.fn(m.data); } catch (e) { setTimeout(function () { throw e; }); }
      });
    }
  }

  window.addEventListener('message', function (e) {
    if (port || e.data !== 'musubi-bridge' || !e.ports || e.ports.length !== 1) return;
    port = e.ports[0];
    port.onmessage = received;
    queue.splice(0).forEach(function (s) { port.postMessage(s); });
  });

  function call(method, args) {
    return new Promise(function (resolve, reject) {
      var id = ++next;
      waiting.set(id, { resolve: resolve, reject: reject });
      post({ t: 'call', id: id, method: method, args: args });
    });
  }

  var App = new Proxy({}, {
    get: function (_, name) {
      if (typeof name !== 'string' || name === 'then') return undefined;
      return function () { return call(name, Array.prototype.slice.call(arguments)); };
    },
  });

  Object.defineProperty(window, 'go', { value: Object.freeze({ gui: Object.freeze({ App: App }) }), writable: false, configurable: false });
  Object.defineProperty(window, 'runtime', {
    value: Object.freeze({
      EventsOn: function (name, fn) { return on(name, fn, -1); },
      EventsOnMultiple: function (name, fn, max) { return on(name, fn, max > 0 ? max : -1); },
      EventsOnce: function (name, fn) { return on(name, fn, 1); },
      EventsOff: function () { for (var i = 0; i < arguments.length; i++) listeners.delete(arguments[i]); },
      EventsOffAll: function () { listeners.clear(); },
      // The phone's screens never send events to the Mac.
      EventsEmit: function () {},
    }),
    writable: false,
    configurable: false,
  });
})();
