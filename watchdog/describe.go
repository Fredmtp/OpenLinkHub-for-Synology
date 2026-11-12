package main

import (
	"fmt"
	"strings"
	"unicode"
)

// Textes standard des alertes. Ils servent seuls quand l'IA est absente ou en panne (l'alerte part
// alors « non analysée »), et de base de faits à l'IA quand elle est disponible.

// baseGravity est la gravité déterministe d'une anomalie. L'IA peut seulement la relever pour une
// alerte critique ou haute; elle peut la modifier librement pour une alerte moyenne ou basse.
func baseGravity(f finding) string {
	if f.Level == levelCrit {
		return "critique"
	}
	k := f.Key
	switch {
	case k == "power-isolated":
		return "basse"
	case k == "volume", k == "nas-stale", k == "api",
		strings.HasPrefix(k, "disk-realloc-"), strings.HasPrefix(k, "disk-errors-"),
		strings.HasSuffix(k, "-missing"), strings.HasPrefix(k, "probe-"), strings.HasPrefix(k, "raid-"), strings.HasPrefix(k, "qnap-"),
		k == "hue-unreachable":
		return "moyenne"
	}
	return "haute"
}

func capitalize(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+len(string(r)):]
	}
	return s
}

// diskNum extrait le numéro de baie d'une clé « …-sata2 » ou d'un nom « sata2 ».
func diskNum(s string) string {
	i := strings.LastIndex(s, "sata")
	if i < 0 {
		return "?"
	}
	return s[i+len("sata"):]
}

func findDisk(in assessInput, name string) (diskInfo, bool) {
	for _, d := range in.NAS.Disks {
		if d.Name == name {
			return d, true
		}
	}
	return diskInfo{}, false
}

func diskMeasures(d diskInfo) []measure {
	var ms []measure
	if d.TempOK {
		lvl := ""
		if d.Temp >= defaultLimits().DiskWarn {
			lvl = "warn"
		}
		ms = append(ms, measure{"Température", fmt.Sprintf("%.0f °C", d.Temp), lvl})
	}
	lvl := func(n int, bad string) string {
		if n > 0 {
			return bad
		}
		return ""
	}
	ms = append(ms,
		measure{"Secteurs retirés", frInt(d.Reallocated), lvl(d.Reallocated, "warn")},
		measure{"Secteurs en attente", frInt(d.Pending), lvl(d.Pending, "crit")},
		measure{"Erreurs de lecture cumulées", frInt(d.Reported), lvl(d.Reported, "warn")})
	return ms
}

// describeFinding transforme une anomalie en carte avec ses textes standard et ses mesures.
func describeFinding(f finding, in assessInput) card {
	c := card{Key: f.Key, Gravity: baseGravity(f), Summary: capitalize(f.Text) + ".", Equip: "DS1825+"}
	k := f.Key
	crit := f.Level == levelCrit

	diskCard := func(prefix string) {
		name := strings.TrimPrefix(k, prefix)
		num := diskNum(name)
		c.Where = "Matériel : disque " + num
		c.Equip = fmt.Sprintf("DS1825+ · baie %s (%s)", num, name)
		if d, ok := findDisk(in, name); ok {
			c.Measures = diskMeasures(d)
			meta := d.Model
			if d.PowerOn > 0 {
				if meta != "" {
					meta += " · "
				}
				meta += frInt(d.PowerOn) + " h de fonctionnement"
			}
			c.Meta = meta
		}
	}
	const replaceAdvice = "Vérifiez que la sauvegarde est à jour, ne retirez aucun disque avant l'arrivée du remplaçant, " +
		"puis remplacez ce disque selon la procédure (un seul disque à la fois)."

	switch {
	case strings.HasPrefix(k, "disk-temp-"):
		diskCard("disk-temp-")
		c.Title = "Disque " + diskNum(k) + " : température élevée"
		c.Action = "Vérifiez la ventilation du meuble et la température des autres disques."
		if crit {
			c.Action = "Vérifiez immédiatement la ventilation du meuble (ventilateurs, porte, obstruction). Le NAS s'éteindra de lui-même si la température se maintient trop haut."
		}
		c.Why = "Au-delà de 50 °C la durée de vie d'un disque baisse nettement ; l'arrêt automatique se déclenche à 55 °C maintenus."
	case strings.HasPrefix(k, "disk-health-"):
		diskCard("disk-health-")
		c.Title = "Disque " + diskNum(k) + " : le test de santé du disque a échoué"
		c.Action = replaceAdvice
		c.Why = "Le disque lui-même déclare un défaut: ce n'est pas une alerte de seuil."
	case strings.HasPrefix(k, "disk-bad-"):
		diskCard("disk-bad-")
		c.Title = "Disque " + diskNum(k) + " : secteurs illisibles"
		c.Action = replaceAdvice
		c.Why = "Des secteurs en attente ou non corrigeables signalent une surface abîmée qui peut s'étendre."
	case strings.HasPrefix(k, "disk-growth-"):
		diskCard("disk-growth-")
		c.Title = "Disque " + diskNum(k) + " : les défauts augmentent"
		c.Action = replaceAdvice
		c.Why = "Un compteur de défauts qui monte montre que le disque continue de se dégrader."
	case strings.HasPrefix(k, "disk-errors-"):
		diskCard("disk-errors-")
		c.Title = "Disque " + diskNum(k) + " : erreurs de lecture cumulées"
		c.Action = "Surveillez ce disque : un remplacement est à prévoir s'il s'aggrave."
		c.Why = "Ces erreurs sont cumulées depuis la mise en service, pas forcément récentes."
	case strings.HasPrefix(k, "disk-realloc-"):
		diskCard("disk-realloc-")
		c.Title = "Disque " + diskNum(k) + " : secteurs retirés"
		c.Action = "Surveillez ce disque : un remplacement est à prévoir si le nombre augmente."
		c.Why = "Le disque a déjà remplacé des secteurs défectueux par des secteurs de réserve."
	case k == "cpu-temp":
		c.Where, c.Title = "Matériel : processeur", "Processeur du NAS : température élevée"
		c.Action = "Vérifiez la ventilation du meuble et la charge du NAS."
		if in.NAS.CPUOK {
			c.Measures = []measure{{"Température du processeur", fmt.Sprintf("%.0f °C", in.NAS.CPU), "warn"}}
		}
		c.Why = "L'arrêt automatique se déclenche à 85 °C maintenus."
	case k == "board-temp":
		c.Where, c.Title = "Matériel : carte mère", "Carte mère du NAS : température élevée"
		c.Action = "Vérifiez la ventilation du meuble et les ventilateurs internes du NAS."
		c.Why = "L'arrêt automatique se déclenche à 65 °C maintenus."
	case k == "nasfan-all":
		c.Where, c.Title = "Matériel : ventilateurs du NAS", "Tous les ventilateurs internes du NAS sont arrêtés"
		c.Action = "Éteignez le NAS proprement et faites contrôler ses ventilateurs avant de le relancer."
		c.Why = "Sans ventilation interne, les disques et le processeur chauffent vite."
	case strings.HasPrefix(k, "nasfan-"):
		c.Where, c.Title = "Matériel : ventilateurs du NAS", "Un ventilateur interne du NAS est arrêté"
		c.Action = "Faites contrôler ce ventilateur. Les autres continuent de refroidir le NAS."
	case k == "corsair-all":
		c.Where, c.Title = "Meuble : ventilation", "Tous les ventilateurs du meuble sont arrêtés"
		c.Action = "Vérifiez l'alimentation et le câble iCUE LINK du Hub. Le NAS s'éteindra de lui-même si la situation dure plus de 15 minutes."
		c.Why = "Sans ventilation, le NAS, la baie QNAP et le Zyxel chauffent dans le meuble fermé."
		c.Equip = "Meuble · iCUE LINK System Hub"
	case k == "corsair-extraction":
		c.Where, c.Title = "Meuble : ventilation", "La ventilation d'extraction est dégradée ou perdue"
		c.Action = "Vérifiez l'alimentation et le câble iCUE LINK de la chaîne. Si tous les extracteurs sont arrêtés, le NAS s'éteindra de lui-même au bout de 15 minutes sans rétablissement; si deux seulement le sont, il ne s'éteindra que si un disque atteint 45 °C. Pour intervenir sans arrêt du NAS, mettez la veille en route avec maintenance.sh, et coupez le son avec silence.sh."
		c.Why = "L'air chaud n'est plus évacué du meuble fermé: le NAS, la baie QNAP et le Zyxel chauffent."
		c.Equip = "Meuble · iCUE LINK System Hub"
	case strings.HasPrefix(k, "corsair-fan-"):
		c.Where, c.Title = "Meuble : ventilateur canal "+strings.TrimPrefix(k, "corsair-fan-"), "Un ventilateur du meuble est arrêté"
		c.Action = "Vérifiez le câble iCUE LINK de ce ventilateur et sa connexion à la chaîne. S'il ne repart pas, remplacez-le sans attendre."
		c.Why = "Un seul ventilateur arrêté ne met pas le NAS en danger, mais la marge de refroidissement est réduite."
		c.Equip = "Meuble · iCUE LINK System Hub"
	case k == "corsair-missing":
		c.Where, c.Title = "Meuble : ventilation", "Il manque un ventilateur du meuble"
		c.Action = "Vérifiez la chaîne iCUE LINK: un ventilateur n'est plus détecté."
		c.Equip = "Meuble · iCUE LINK System Hub"
	case k == "hub-stale":
		c.Where, c.Title, c.Equip = "Meuble : ventilation", "Les ventilateurs du meuble ne sont plus lus", "Meuble · iCUE LINK System Hub"
		c.Action = "Vérifiez que les ventilateurs tournent réellement et que la prise Hue est allumée. La surveillance coupe puis rallume la prise toute seule si elle est configurée (deux fois au plus par jour)."
		c.Why = "Un Hub sans courant ou bloqué laisse OpenLinkHub afficher les dernières valeurs: la nuit du 12/11/2026, 1 103 tr/min et 30,6 °C sont restés figés de 0 h 15 à 1 h 10 alors que les ventilateurs étaient arrêtés."
	case k == "hue-off" || k == "hue-unreachable":
		c.Where, c.Equip = "Meuble : alimentation des ventilateurs", "Prise Philips Hue « Aération NAS » · Bridge Hue"
		if k == "hue-off" {
			c.Title = "La prise des ventilateurs du meuble est éteinte"
			c.Action = "La surveillance la rallume toute seule; vérifiez que les ventilateurs tournent. Si le NAS devait être éteint, vérifiez le programme de DSM (minuit) et la routine Hue de 0 h 15."
			c.Why = "Sans courant, le meuble ne se ventile plus: les disques montent à 43 °C en quarante minutes (nuits des 11 et 12/11/2026)."
		} else {
			c.Title = "Le Bridge Hue ne répond plus"
			c.Action = "Vérifiez l'alimentation et le réseau du Bridge, et le relais Zigbee de la prise. En attendant, la prise n'est plus surveillée ni commandée par la surveillance."
			c.Why = "Sans le Bridge, la surveillance ne peut ni lire l'état de la prise ni la rallumer; l'arrêt par ventilation perdue (15 min) et par température restent actifs."
		}
	case k == "qnap-missing" || k == "qnap-io":
		c.Where, c.Equip = "Meuble : baie QNAP (archives et sauvegarde)", "Baie QNAP TR-004 · USB"
		if k == "qnap-missing" {
			c.Title = "La baie QNAP n'est plus vue par le NAS"
			c.Action = "Vérifiez le câble USB et l'alimentation de la baie et ses voyants; si besoin, rebranchez-la au PC et lisez son état avec QNAP External RAID Manager."
		} else {
			c.Title = "Erreurs USB sur la baie QNAP"
			c.Action = "Contrôlez le câble USB, puis rebranchez la baie au PC et vérifiez ses disques avec QNAP External RAID Manager."
		}
		c.Why = "Le NAS ne voit de cette baie qu'un disque virtuel: sans SMART ni état du RAID, ces signes sont les seuls disponibles."
	case strings.HasPrefix(k, "probe-"):
		c.Where, c.Title = "Meuble : sonde", "Température élevée dans le meuble"
		if strings.HasSuffix(k, "-missing") {
			c.Title = "Une sonde du meuble ne répond plus"
			c.Action = "Vérifiez le branchement de la sonde sur le Commander DUO."
		} else if strings.HasSuffix(k, "-frozen") {
			c.Title = "Une sonde du meuble est figée"
			c.Action = "Éteignez le NAS proprement, coupez l'alimentation du meuble (chaîne iCUE) 30 secondes, puis rallumez: c'est ce qui a réparé le Commander DUO le 02/11/2026."
			c.Why = "Les ventilateurs suivent la courbe de la sonde: figée, ils ne réagissent plus à la chaleur. Les disques, le processeur et la carte mère restent surveillés."
		} else {
			c.Action = "Vérifiez les ventilateurs du meuble et la circulation d'air (porte, obstruction)."
			c.Why = "L'arrêt automatique se déclenche à 45 °C maintenus dans le NAS ou la baie QNAP."
		}
		c.Equip = "Meuble · Commander DUO"
	case k == "ups":
		c.Where, c.Title = "Alimentation : onduleur", "Coupure secteur : le NAS est sur batterie"
		c.Action = "Si la coupure dure, préparez l'arrêt propre du NAS (DSM le fait normalement quand la batterie est faible)."
		if crit {
			c.Title = "Onduleur : batterie faible"
			c.Action = "Éteignez le NAS proprement maintenant si DSM ne l'a pas déjà fait."
		}
		c.Equip = "Onduleur Eaton 5P"
		if in.NAS.UPS.Present {
			u := in.NAS.UPS
			c.Measures = []measure{{"État", u.Status, "warn"}, {"Charge", fmt.Sprintf("%.0f %%", u.Charge), ""},
				{"Autonomie", fmt.Sprintf("%.0f min", u.RuntimeS/60), ""}}
		}
	case strings.HasPrefix(k, "raid-") && strings.HasSuffix(k, "-severe"):
		c.Where, c.Title = "Stockage : RAID", "RAID sans aucune redondance"
		c.Action = "Ne touchez plus au NAS : un disque de plus en panne et des données sont perdues. Vérifiez la sauvegarde et remplacez le disque manquant sans attendre."
		c.Why = "La marge de sécurité du RAID est épuisée."
	case strings.HasPrefix(k, "raid-"):
		c.Where, c.Title = "Stockage : RAID", "Le RAID est dégradé ou se reconstruit"
		c.Action = "Si c'est volontaire (remplacement de disque), laissez la reconstruction aller au bout sans éteindre le NAS. Sinon, vérifiez le disque manquant."
		for _, r := range in.NAS.RAID {
			if "raid-"+r.Name == k {
				c.Measures = []measure{{"Disques en ligne", fmt.Sprintf("%d sur %d", r.Up, r.Total), "warn"}}
				if r.Rebuilding && r.Progress > 0 {
					c.Measures = append(c.Measures, measure{"Reconstruction", fmt.Sprintf("%.0f %%", r.Progress), ""})
				}
			}
		}
	case k == "volume":
		c.Where, c.Title = "Stockage : volume 1", "Le volume 1 se remplit"
		c.Action = "Libérez de l'espace ou prévoyez plus de capacité."
	case k == "api":
		c.Where, c.Title = "Meuble : OpenLinkHub", "OpenLinkHub ne répond plus"
		c.Action = "Relancez OpenLinkHub avec start.sh. Les ventilateurs gardent leurs réglages en attendant."
		c.Equip = "Meuble · iCUE LINK System Hub"
	case k == "nas-stale":
		c.Where, c.Title = "Surveillance", "Les mesures du NAS ne se mettent plus à jour"
		c.Action = "Relancez la surveillance avec start.sh."
	case k == "power-isolated":
		c.Where, c.Title = "Matériel : disque", "Un disque a parqué ses têtes de lui-même"
		c.Action = "Rien de particulier à faire. Si cela se répète sur le même disque, vérifiez le contact de sa baie et les vibrations autour du meuble."
		c.Why = "Un seul disque est touché: un choc ou une vibration plutôt qu'une coupure."
	case k == "power-general":
		c.Where, c.Title = "Alimentation : coupure", "Coupure de courant brutale"
		c.Action = "Vérifiez l'onduleur (autonomie, journal), le journal de DSM et la ligne électrique."
		c.Why = "La plupart des disques ont parqué leurs têtes en même temps."
		c.Equip = "Onduleur Eaton 5P"
	default:
		c.Where, c.Title = "Surveillance", capitalize(f.Text)
		c.Action = "Consultez la page d'état du NAS."
	}
	if c.Where == "" {
		c.Where = "Surveillance"
	}
	return c
}
