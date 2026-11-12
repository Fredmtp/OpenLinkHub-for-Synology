# Bridge Philips Hue (v2): enregistre l'application une seule fois, puis LISTE les appareils avec leur nom exact,
# leur type, leur état (allumé / éteint) et l'état de la liaison Zigbee. Lecture seule: aucune commande n'est envoyée
# aux appareils. Sert à vérifier le nom exact de la prise « Aération NAS » avant de configurer la surveillance.
#
#   & '.\dist\hue-lister.ps1'            (demande l'adresse IP du Bridge)
#   & '...\hue-lister.ps1' -Bridge 192.168.8.xx
#
# La clé d'accès au Bridge est gardée dans %USERPROFILE%\.hue-nas.env (hors OneDrive, lisible par votre compte
# seulement). Elle n'est JAMAIS affichée: ne la copiez pas dans une conversation.
# Nécessite PowerShell 7 (option -SkipCertificateCheck: le Bridge a un certificat auto-signé).
param([string]$Bridge)
$ErrorActionPreference = 'Stop'

$fichier = Join-Path $env:USERPROFILE '.hue-nas.env'
$vals = @{}
if (Test-Path -LiteralPath $fichier) {
    foreach ($l in Get-Content -LiteralPath $fichier) {
        if ($l -match '^\s*(HUE_[A-Z]+)\s*=\s*(.*?)\s*$') { $vals[$Matches[1]] = $Matches[2] }
    }
}
if (-not $Bridge) { $Bridge = $vals['HUE_BRIDGE'] }
if (-not $Bridge) { $Bridge = Read-Host "Adresse IP du Bridge Hue (appli Hue: Réglages, Bridges, l'icône i)" }
if ($vals['HUE_BRIDGE'] -and $vals['HUE_BRIDGE'] -ne $Bridge) { $vals.Remove('HUE_KEY') }
$vals['HUE_BRIDGE'] = $Bridge

if (-not $vals['HUE_KEY']) {
    Read-Host 'Appuyez sur le gros bouton rond du Bridge, puis sur Entrée (vous avez 30 secondes)' | Out-Null
    $rep = Invoke-RestMethod -SkipCertificateCheck -Method Post -Uri "https://$Bridge/api" -ContentType 'application/json' `
        -Body '{"devicetype":"nas-watchdog#claude","generateclientkey":true}'
    if ($rep[0].error) { throw "Refusé par le Bridge: $($rep[0].error.description)" }
    $vals['HUE_KEY'] = $rep[0].success.username
    Set-Content -LiteralPath $fichier -Value @("HUE_BRIDGE=$Bridge", "HUE_KEY=$($vals['HUE_KEY'])") -Encoding ASCII
    icacls $fichier /inheritance:r /grant:r "${env:USERNAME}:(R,W)" | Out-Null
    Write-Host "Application enregistrée (clé gardée dans $fichier, non affichée)."
}

$h = @{ 'hue-application-key' = $vals['HUE_KEY'] }
$base = "https://$Bridge/clip/v2/resource"
function Get-Hue($type) { (Invoke-RestMethod -SkipCertificateCheck -Headers $h -Uri "$base/$type").data }

$lights = @{}; Get-Hue 'light' | ForEach-Object { $lights[$_.id] = $_ }
$conn = @{}; Get-Hue 'zigbee_connectivity' | ForEach-Object { $conn[$_.id] = $_ }

$lignes = foreach ($d in Get-Hue 'device') {
    $l = $d.services | Where-Object { $_.rtype -eq 'light' } | Select-Object -First 1
    $z = $d.services | Where-Object { $_.rtype -eq 'zigbee_connectivity' } | Select-Object -First 1
    $etat = if ($l -and $lights[$l.rid]) { if ($lights[$l.rid].on.on) { 'ALLUMÉ' } else { 'éteint' } } else { '-' }
    $liaison = if ($z -and $conn[$z.rid]) { $conn[$z.rid].status } else { '-' }
    [pscustomobject]@{
        Nom       = $d.metadata.name
        Type      = $d.product_data.product_name
        Archetype = $d.metadata.archetype
        Etat      = $etat
        Liaison   = $liaison
    }
}
$lignes | Sort-Object Nom | Format-Table -AutoSize
Write-Host "Repérez la ligne de la prise de la ventilation (nom exact, majuscules et accents compris) et dites-moi son nom, son type et son état."
