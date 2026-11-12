# Changelog

**English** · [Français](CHANGELOG.fr.md)

Dates are dd/mm/2026. Version numbers were assigned after the fact to the milestones below (per-version tracking starts with 1.0.0). Major = redesign or change of behaviour, minor = new feature or protection, patch = 1.0.x.

## 1.0.0 — 12/11: theme, charts, fans' plug

- **Light and dark themes** on every page (light by default), toggle remembered per browser, local fonts, dark sidebar, NAS tiles with a dot or a labelled badge.
- Application title “OPENLINKHUB for SYNOLOGY WebUI”, version displayed, changelog.
- **“Températures NAS” tab**: charts over a chosen period, thresholds, alerts, state bands (`/api/releves` API).
- **Smart plug for the fans**: switched back on if it turns off, repair cycle if the Hub freezes, NAS shutdown if nothing helps; frozen-Hub detection (identical speeds for 10 minutes).
- Monitoring: 262 tests.
- *Lessons*: a Hub without power leaves OpenLinkHub displaying the last values (ventilation “looks” normal); a shutdown rule must never apply after a plain repair that could not restart OpenLinkHub; test for real any protection that cuts hardware.

## 0.8.0 — 08/11: automatic restart

- The supervisor restarts **OpenLinkHub alone** (USB reset of the Hub) when its API stops answering for 3 minutes. Origin: a crash at start-up left 43 minutes without probe readings.
- Quiet window of the remote check depends on the day of the week (different reboot times).

## 0.7.0 — 02/11: frozen probe, RAID scrubbing, USB enclosure

- “Frozen probe” alert (identical value for 4 hours); **data scrubbing** tracking; **USB enclosure** monitoring (disappearance, kernel errors); UPS check; scheduled night shutdown.

## 0.6.0 — 29/10 to 01/11: reliable notifications and disk names

- Notifications: only failed channels are retried (end of triple sends); DSM notification dropped (free text is refused).
- Disks named after DSM bays rather than `/dev/sataN`.

## 0.5.0 — 28/10 (evening): real mode

- Real shutdown test passed, then the monitoring switched to real mode (NAS shut down if ventilation is lost for 15 minutes or a disk stays at 45 °C for 1 minute).

## 0.4.0 — 28/10: full monitoring

- 4-state ventilation, disk thresholds 40/43/45 °C, shutdown-loop protection, audible and visual alerts, protected command page, maintenance mode, supervision (heartbeat, supervisor, check from a second NAS), ntfy and email notifications.

## 0.3.0 — 27/10: reading the disks

- SMART counters by bay, known situations, first reading-analysis tools.

## 0.2.0 — 26/10: monitoring in Go

- First monitoring (dry-run) and git repository.

## 0.1.0 — 24 and 25/10: OpenLinkHub on the NAS

- OpenLinkHub built for DSM (static libusb, no PipeWire, no udev), started at boot, USB reset of the Hub.
