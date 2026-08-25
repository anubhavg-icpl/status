/* Status page push service worker.
 *
 * Scope is the site root, so it controls every page. It does one job: turn a
 * push message into a notification, and focus the status page when tapped.
 */

self.addEventListener('install', (event) => {
  // Take over immediately instead of waiting for every tab to close.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

const ICONS = {
  down: '/static/favicon.svg',
  degraded: '/static/favicon.svg',
  operational: '/static/favicon.svg',
};

self.addEventListener('push', (event) => {
  let payload = {};
  if (event.data) {
    try {
      payload = event.data.json();
    } catch (e) {
      // A push service can deliver a bare string; still worth showing.
      payload = { title: 'Status update', body: event.data.text() };
    }
  }

  const title = payload.title || 'Status update';
  const critical = payload.severity === 'critical';

  const options = {
    body: payload.body || '',
    icon: ICONS[payload.status] || '/static/favicon.svg',
    badge: '/static/favicon.svg',
    // Same tag replaces the previous alert for that service instead of
    // stacking a wall of notifications during a long outage.
    tag: payload.tag || payload.event || 'status',
    renotify: true,
    requireInteraction: critical,
    timestamp: payload.timestamp ? Date.parse(payload.timestamp) : Date.now(),
    data: { url: payload.url || '/', event: payload.event || '' },
  };

  // Critical alerts vibrate; routine ones stay quiet.
  if (critical) {
    options.vibrate = [200, 100, 200, 100, 200];
  }

  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = (event.notification.data && event.notification.data.url) || '/';

  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
      // Reuse an already-open status tab rather than spawning another one.
      for (const client of clientList) {
        if ('focus' in client) {
          if (client.url === target) return client.focus();
        }
      }
      if (self.clients.openWindow) return self.clients.openWindow(target);
      return undefined;
    })
  );
});

self.addEventListener('pushsubscriptionchange', (event) => {
  // The browser rotated our endpoint. Re-register with the new one so alerts
  // keep arriving without the user re-enabling notifications by hand.
  event.waitUntil(
    self.registration.pushManager
      .subscribe(event.oldSubscription ? event.oldSubscription.options : { userVisibleOnly: true })
      .then((sub) =>
        fetch('/api/push/subscribe', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(sub),
        })
      )
      .catch(() => {})
  );
});
