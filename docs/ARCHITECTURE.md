# Architecture

**English** · [Français](ARCHITECTURE.fr.md)

Three programs run on the NAS, as root, in `/volume1/openlinkhub/`:

| Program | Role | Port |
|---|---|---|
| **OpenLinkHub** (built for DSM) | Drives the iCUE LINK Hub over USB: fan speeds, Commander DUO probes, lighting. The original web interface, with the gold-and-cream theme on top | 27003 |
| **`olh-watchdog`** (`watchdog/`, Go) | Safety loop (every few seconds), slow sensor collection, status page, readings API, notifications, Hue plug control | 27004 |
| **`superviseur.sh`** | Run every minute by DSM's Task Scheduler: restarts the monitoring if it is dead or frozen, restarts OpenLinkHub if its API stops answering | — |

The theme and the dashboard tiles are plain files (`dist/static/`, `dist/web/`) read by OpenLinkHub: **no change to OpenLinkHub's Go code** beyond a few build patches (`openlinkhub-patches/`).

## What the monitoring reads

- **From DSM**: disk temperatures and SMART counters (by bay number, not by device name, which changes at every boot), RAID (state, rebuild, data scrubbing), volume, motherboard and CPU, internal fans, UPS, QNAP USB enclosure.
- **From OpenLinkHub**: speeds of the cabinet fans, temperatures of the two probes.
- **From the Hue bridge** (optional): state of the plug that powers the fans.

## Safety principles

1. **Several independent layers.** Disk and motherboard temperatures depend neither on OpenLinkHub nor on the Hub: if the whole cabinet fails, this safety net remains.
2. **Never trust a value that stopped moving.** A Hub without power leaves OpenLinkHub displaying the last values; the monitoring detects “all speeds identical for 10 minutes”.
3. **No shutdown loop.** After an automatic shutdown, ventilation-based shutdowns are suspended for 6 hours (temperatures stay monitored).
4. **Dry-run mode** (`-dry`) and **maintenance mode** (a file with an expiry, 4 h at most) to work safely.
5. **No action on the hardware** during maintenance or the start-up grace period.

## Data

Readings (one CSV file per month, one line per minute) are in `releves/`. The `/api/releves` API groups them by intervals (average and maximum) for the charts; it is read-only.
