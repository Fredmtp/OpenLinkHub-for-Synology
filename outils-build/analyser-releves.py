#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Analyse les relevés de températures (temperatures-AAAA-MM.csv, une ligne par minute) et propose les
seuils d'alerte d'après la méthode de docs/seuils-mode-reel.md, section 6.

  python analyser-releves.py releves/*.csv --depuis "2026-10-30 08:00"
  python analyser-releves.py --autotest

Les lignes en maintenance ou en ventilation non normale sont écartées (elles ne représentent pas le
régime normal). --depuis écarte aussi la période de stabilisation (conseillé: 24 h après la fermeture
de la fenêtre). Les seuils d'ARRÊT des disques ne changent pas: 45 °C maintenus 60 s (limite voulue).
"""
import argparse, csv, math, sys, tempfile, os
from datetime import datetime, timedelta

DISQUE_ALERTE, DISQUE_CRIT, DISQUE_ARRET = 40, 43, 45   # décidés le 28/10/2026
SONDE_ARRET, CPU_ARRET, CARTE_ARRET = 45, 85, 65          # seuils d'arrêt actuels


def lire(chemins):
    lignes, entete = [], None
    for c in chemins:
        with open(c, newline="", encoding="utf-8") as f:
            for row in csv.reader(f):
                if not row:
                    continue
                if row[0] == "horodatage":   # en-tête (réécrite à chaque démarrage ou changement de colonnes)
                    entete = row
                    continue
                if entete is None or len(row) != len(entete):
                    continue
                d = dict(zip(entete, row))
                try:
                    d["_t"] = datetime.strptime(d["horodatage"], "%Y-%m-%d %H:%M:%S")
                except ValueError:
                    continue
                lignes.append(d)
    lignes.sort(key=lambda d: d["_t"])
    return lignes


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def centile(vals, p):
    s = sorted(vals)
    k = (len(s) - 1) * p / 100
    lo, hi = int(math.floor(k)), int(math.ceil(k))
    return s[lo] + (s[hi] - s[lo]) * (k - lo)


def etat_disque(mx):
    if mx >= DISQUE_ARRET:
        return "DÉPASSE L'ARRÊT (45 °C)"
    if mx >= DISQUE_CRIT:
        return "dépasse le critique (43 °C)"
    if mx >= DISQUE_ALERTE:
        return "dépasse l'alerte (40 °C)"
    if mx >= DISQUE_ALERTE - 2:
        return "moins de 2 °C sous l'alerte"
    return "OK"


def analyser(lignes, depuis=None, jusqua=None, ambiante=None, cible=24.0):
    out = []
    p = out.append
    if depuis:
        lignes = [d for d in lignes if d["_t"] >= depuis]
    if jusqua:
        lignes = [d for d in lignes if d["_t"] <= jusqua]
    if not lignes:
        return "Aucune ligne dans la période demandée."
    total = len(lignes)
    ecartees = [d for d in lignes if d.get("maintenance") == "1" or d.get("ventilation") not in ("ok", None, "")]
    gardees = [d for d in lignes if d not in ecartees]
    debut, fin = lignes[0]["_t"], lignes[-1]["_t"]
    duree = fin - debut
    attendu = int(duree.total_seconds() / 60) + 1
    p(f"Période: {debut:%d/%m/%Y %H:%M} → {fin:%d/%m/%Y %H:%M} ({duree.total_seconds()/3600:.1f} h)")
    p(f"Lignes: {total} (attendues ≈ {attendu}, manquantes ≈ {max(0, attendu - total)}); écartées (maintenance ou ventilation non normale): {len(ecartees)}; analysées: {len(gardees)}")
    if len(gardees) < 60:
        p("ATTENTION: moins d'une heure de données analysables, les chiffres ne sont pas significatifs.")
    if not gardees:
        return "\n".join(out)
    cols = [c for c in gardees[0] if not c.startswith("_") and c not in ("horodatage", "ventilation", "maintenance")]
    stats = {}
    for c in cols:
        v = [(num(d.get(c)), d["_t"]) for d in gardees if num(d.get(c)) is not None]
        if not v:
            continue
        vals = [x for x, _ in v]
        mx = max(v, key=lambda a: a[0])
        stats[c] = dict(n=len(vals), min=min(vals), moy=sum(vals) / len(vals), p95=centile(vals, 95), p99=centile(vals, 99), max=mx[0], quand=mx[1])

    p("\n== Valeurs observées (régime normal) ==")
    p(f"{'colonne':28} {'min':>7} {'moy':>7} {'p95':>7} {'p99':>7} {'max':>7}  quand (max)")
    for c, s in stats.items():
        p(f"{c:28} {s['min']:7.1f} {s['moy']:7.1f} {s['p95']:7.1f} {s['p99']:7.1f} {s['max']:7.1f}  {s['quand']:%d/%m %H:%M}")

    p("\n== Par jour: min / max ==")
    jours = sorted({d["_t"].date() for d in gardees})
    clef = [c for c in ("disque_max", "sonde_nas", "sonde_qnap", "processeur", "carte_mere_max") if c in stats]
    p(f"{'jour':12}" + "".join(f"{c:>20}" for c in clef))
    for j in jours:
        ligne = f"{j:%d/%m/%Y}  "
        for c in clef:
            v = [num(d[c]) for d in gardees if d["_t"].date() == j and num(d.get(c)) is not None]
            ligne += f"{(f'{min(v):.0f} / {max(v):.0f}' if v else '-'):>20}"
        p(ligne)

    p("\n== Disques: marge avant les seuils décidés (alerte 40, critique 43, arrêt 45) ==")
    for c, s in stats.items():
        if c.startswith("disque_") and c != "disque_max":
            etat = "OK"
            if s["max"] >= DISQUE_ARRET:
                etat = "DÉPASSE L'ARRÊT"
            elif s["max"] >= DISQUE_CRIT:
                etat = "dépasse le critique"
            elif s["max"] >= DISQUE_ALERTE:
                etat = "dépasse l'alerte (faux positifs à prévoir)"
            elif s["max"] >= DISQUE_ALERTE - 2:
                etat = "marge de moins de 2 °C sous l'alerte"
            p(f"{c:22} max {s['max']:4.0f} °C   marge alerte {DISQUE_ALERTE - s['max']:+5.1f}   marge arrêt {DISQUE_ARRET - s['max']:+5.1f}   {etat}")
    dm = stats.get("disque_max")
    if dm:
        if dm["max"] >= DISQUE_ALERTE - 2:
            p("→ Le disque le plus chaud est trop près de 40 °C en régime normal: une alerte fixe à 40 °C est fragile. Pistes: alerte par disque, ou améliorer la ventilation (profil des ventilateurs en fonction de la température), avant de remonter un seuil.")
        else:
            p("→ Marge confortable sous 40 °C: les seuils 40 / 43 / 45 sont tenables en régime normal.")

    if ambiante is not None:
        delta = cible - ambiante
        p(f"\n== Extrapolation à une pièce à {cible:.0f} °C (mesure faite à environ {ambiante:.0f} °C: écart {delta:+.1f} °C) ==")
        p("Hypothèse: l'écart entre une température et l'air de la pièce reste le même; le maximum observé monte donc de l'écart.")
        pire = None
        for c, s in stats.items():
            if c.startswith("disque_") and c != "disque_max":
                e = s["max"] + delta
                p(f"{c:22} max observé {s['max']:4.0f} → extrapolé {e:5.1f} °C   {etat_disque(e)}")
                pire = e if pire is None else max(pire, e)
        for c, nom in (("sonde_nas", "sonde NAS"), ("sonde_qnap", "sonde QNAP"), ("processeur", "processeur"), ("carte_mere_max", "carte mère")):
            if c in stats:
                p(f"{nom:22} max observé {stats[c]['max']:4.0f} → extrapolé {stats[c]['max'] + delta:5.1f} °C")
        if pire is not None:
            if pire >= DISQUE_ALERTE:
                p(f"→ ATTENTION: à {cible:.0f} °C de pièce, le disque le plus chaud atteindrait environ {pire:.0f} °C en régime NORMAL, au niveau de l'alerte ou au-dessus. Il faut agir sur le refroidissement (ventilateurs plus rapides en fonction de la température, circulation d'air de la baie du Disque 2) avant l'été, plutôt que remonter les seuils.")
            else:
                p(f"→ À {cible:.0f} °C de pièce, le disque le plus chaud resterait vers {pire:.0f} °C: sous l'alerte de 40 °C.")
    else:
        p("\n(Pas d'extrapolation: indiquez la température de la pièce pendant la mesure avec --ambiante pour estimer l'été, voir --cible.)")

    p("\n== Seuils d'alerte proposés (maximum observé + marge, sous les seuils d'arrêt) ==")
    def prop(c, marge, plafond, nom):
        if c not in stats:
            return
        mx = stats[c]["max"]
        a = math.ceil(mx + marge)
        a = min(a, plafond - 5)
        p(f"{nom:22} max observé {mx:5.1f} → alerte proposée {a:3d} °C (arrêt actuel {plafond} °C)")
    prop("sonde_nas", 5, SONDE_ARRET, "sonde NAS (canal 13)")
    prop("sonde_qnap", 5, SONDE_ARRET, "sonde QNAP (canal 14)")
    prop("processeur", 15, CPU_ARRET, "processeur")
    prop("carte_mere_max", 15, CARTE_ARRET, "carte mère")
    p("(Disques: seuils décidés 40 / 43 / 45 °C, non modifiés par cette analyse.)")

    p("\n== Ventilateurs (tr/min) ==")
    for c, s in stats.items():
        if c.startswith("ventilo_"):
            p(f"{c:28} min {s['min']:6.0f}  moy {s['moy']:6.0f}  max {s['max']:6.0f}")
    return "\n".join(out)


def autotest():
    """Génère trois jours synthétiques et vérifie les résultats principaux."""
    d = tempfile.mkdtemp()
    f = os.path.join(d, "temperatures-2026-10.csv")
    en = "horodatage,disque_sata1,disque_sata2,disque_max,processeur,carte_mere_max,ventilo_nas_1_tr_min,sonde_nas,sonde_qnap,ventilo_ch1_tr_min,ventilation,maintenance"
    t = datetime(2026, 10, 30, 0, 0, 0)
    with open(f, "w", encoding="utf-8") as fh:
        fh.write(en + "\n")
        for i in range(3 * 24 * 60):
            h = (t + timedelta(minutes=i))
            cycle = math.sin((h.hour * 60 + h.minute) / 1440 * 2 * math.pi)
            d1, d2 = 33 + 2 * cycle, 29 + 2 * cycle
            mt = "1" if 600 <= i < 630 else "0"           # 30 min de maintenance
            vent = "perdue" if 1000 <= i < 1010 else "ok"  # 10 min de ventilation perdue
            fh.write(f"{h:%Y-%m-%d %H:%M:%S},{d1:.0f},{d2:.0f},{max(d1,d2):.0f},{40+3*cycle:.1f},{32+1*cycle:.1f},1300,{27+1.5*cycle:.1f},{24+1.5*cycle:.1f},{750+40*cycle:.0f},{vent},{mt}\n")
            if i == 2000:  # en-tête réécrite (redémarrage)
                fh.write(en + "\n")
    lignes = lire([f])
    assert len(lignes) == 3 * 24 * 60, len(lignes)
    rapport = analyser(lignes, depuis=datetime(2026, 10, 30, 12, 0))
    assert "écartées (maintenance ou ventilation non normale): 10" in rapport or "écartées" in rapport
    assert "disque_sata1" in rapport and "disque_max" in rapport
    assert "marge confortable" in rapport.lower() or "Marge confortable" in rapport
    assert "sonde NAS (canal 13)" in rapport
    # un disque trop chaud doit être signalé
    chaud = [dict(x) for x in lignes]
    for x in chaud:
        x["disque_sata1"] = "41"
        x["disque_max"] = "41"
    r2 = analyser(chaud)
    assert "faux positifs" in r2 and "fragile" in r2
    r3 = analyser(lignes, depuis=datetime(2030, 1, 1))
    assert "Aucune ligne" in r3
    # extrapolation: pièce à 20 °C pendant la mesure, cible 24 °C (+4 °C)
    r4 = analyser(lignes, ambiante=20, cible=24)
    assert "Extrapolation à une pièce à 24 °C" in r4 and "extrapolé" in r4
    assert "sous l'alerte de 40" in r4          # 35 + 4 = 39: sous l'alerte
    r5 = analyser(chaud, ambiante=20, cible=24)  # 41 + 4 = 45 °C
    assert "DÉPASSE L'ARRÊT" in r5 and "ATTENTION" in r5
    assert "Pas d'extrapolation" in analyser(lignes)
    print("Autotest réussi.")


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("fichiers", nargs="*")
    ap.add_argument("--depuis", help='début de la période analysée, ex. "2026-10-30 08:00"')
    ap.add_argument("--jusqua", help="fin de la période analysée")
    ap.add_argument("--ambiante", type=float, help="température moyenne de la PIÈCE pendant la mesure (°C), pour extrapoler à l'été")
    ap.add_argument("--cible", type=float, default=24.0, help="température de la pièce visée pour l'extrapolation (défaut 24 °C: climatisation d'été)")
    ap.add_argument("--autotest", action="store_true")
    a = ap.parse_args()
    if a.autotest:
        autotest()
        sys.exit(0)
    if not a.fichiers:
        ap.error("indiquez au moins un fichier CSV")
    fmt = "%Y-%m-%d %H:%M"
    sys.stdout.reconfigure(encoding="utf-8")
    print(analyser(lire(a.fichiers),
                   datetime.strptime(a.depuis, fmt) if a.depuis else None,
                   datetime.strptime(a.jusqua, fmt) if a.jusqua else None,
                   a.ambiante, a.cible))
