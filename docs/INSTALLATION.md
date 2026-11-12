# Installing on a Synology (DSM 7)

**English** · [Français](INSTALLATION.fr.md)

> [!NOTE]
> The application, its scripts' messages and its source-code comments are in **French** (see the language notice in the [README](../README.md)).

> [!WARNING]
> These scripts run as **root** on your NAS and can **shut it down**. They contain **example values** (address `192.168.1.10`, account `admin`, SSH port `22`, folder `/volume1/openlinkhub`): adapt them to your setup before running anything. Always start in **dry-run mode** (`MODE_SURVEILLANCE="-dry"` in `start.sh`) and keep an up-to-date backup.

This guide assumes you can SSH into the NAS (Control Panel, Terminal & SNMP) and that the iCUE LINK Hub is plugged into the NAS by USB.

## 1. Build (on a Linux PC or WSL)

1. **OpenLinkHub for DSM.** Clone the upstream project (<https://github.com/jurkovic-nikola/OpenLinkHub>), check out the commit named in `outils-build/LISEZMOI.md`, apply `openlinkhub-patches/modifications-suivies.patch`, copy `openlinkhub-patches/nouveaux/src/audio/audio_stub.go`, then run `outils-build/build3.sh` (static binary, tags `libusb noaudio osusergo netgo`, option `-DNO_ICONV`). `outils-build/build2.sh` prepares the static USB library.
2. **USB reset of the Hub.** `outils-build/build-usbreset.sh` builds `usbreset` (it resets the Hub, and only the Hub, before every OpenLinkHub start).
3. **Monitoring.** `outils-build/build-watchdog.sh` (formatting, `go vet`, tests, static build `GOOS=linux GOARCH=amd64`).

## 2. Copy to the NAS

Create `/volume1/openlinkhub/` and copy into it: `OpenLinkHub`, `olh-watchdog`, `usbreset`, `start.sh`, `superviseur.sh`, `maintenance.sh`, `silence.sh`, `connus.txt`, plus OpenLinkHub's own `static/` and `web/` folders (this repository's theme files go on top, step 5). Make the binaries executable.

## 3. Secrets

Copy `dist/secrets.env.exemple` to `/volume1/openlinkhub/secrets.env` (mode 600, owner root) and fill in what you use: an ntfy topic for phone notifications, email (Microsoft Graph), `CONTROL_PASSWORD` for the command page. All of it is optional; without notifications, alerts only appear on the status page and in the log.

## 4. DSM Task Scheduler tasks (root account)

| Task | Trigger | Command |
|---|---|---|
| Ventilation | Boot-up | `sh /volume1/openlinkhub/start.sh` |
| Supervisor | Every minute | `sh /volume1/openlinkhub/superviseur.sh` |

`start.sh` waits for the Hub, resets it, starts OpenLinkHub, then the monitoring. The supervisor restarts either one if they die (at most 3 restarts per hour for OpenLinkHub, 3 per 30 minutes for the monitoring) and notifies you.

Before switching to real mode (the monitoring then **shuts the NAS down**): run dry mode for several days, check the thresholds against your own readings (`outils-build/analyser-releves.py`), and do a controlled shutdown test.

## 5. Theme, tiles and charts

On the PC: `bash outils-build/empaqueter-theme-goldcream.sh` builds `dist/theme-goldcream.tar.gz`. Copy the archive and `dist/installer-theme-goldcream.sh` to the NAS, then `sudo sh installer-theme-goldcream.sh`: it backs up the replaced files in `/volume1/openlinkhub/theme-goldcream-precedent/`, installs the theme and restarts OpenLinkHub only. `retour-theme-goldcream.sh` restores the original look. Then press Ctrl+Shift+R in the browser.

## 6. Fans' Hue smart plug (optional)

1. On the PC (PowerShell 7): `dist/hue-lister.ps1` creates the access key (press the bridge button) and lists your devices with their **exact name**; the key stays in your profile and is never displayed.
2. `dist/envoyer-hue.ps1` sends it to the NAS's `secrets.env` (adapt the address and SSH account at the top of the script).
3. In `start.sh`, the option `-hue-plug "Plug name"` enables plug monitoring (disabled if the name is empty or the key missing).
4. **Test it**: switch the plug off from the Hue app; the monitoring should switch it back on in under a minute and notify you. Do not trigger the “repair cycle” by hand.

## 7. Check from a second NAS (optional)

`dist/controle-ds1825.sh` (rename it as you like) queries the status page every 5 minutes from another NAS or machine and alerts when it stops answering. The scheduled-night-shutdown window is set in its `ntfy.env`.

## Checking

- `http://<NAS address>:27003/`: the interface, with the “Températures NAS” tab.
- `http://<NAS address>:27004/`: the monitoring's status page (read-only).
- `watchdog.log` and `superviseur.log` in `/volume1/openlinkhub/`.
