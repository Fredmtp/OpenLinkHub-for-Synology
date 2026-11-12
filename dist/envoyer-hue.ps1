# Envoie l'adresse du Bridge Hue et la clé d'accès (créées par hue-lister.ps1, gardées dans %USERPROFILE%\.hue-nas.env)
# dans secrets.env du NAS, sans jamais les afficher. À lancer dans PowerShell 7, une seule fois (et si la clé change).
#   & '.\dist\envoyer-hue.ps1'
param(
    [string]$Nas = 'admin@192.168.1.10',
    [int]$Port = 22
)
$ErrorActionPreference = 'Stop'
$fichier = Join-Path $env:USERPROFILE '.hue-nas.env'
if (-not (Test-Path -LiteralPath $fichier)) { throw "Fichier $fichier introuvable: lancez d'abord hue-lister.ps1" }
$lignes = @(Get-Content -LiteralPath $fichier | Where-Object { $_ -match '^(HUE_BRIDGE|HUE_KEY)=\S+' })
if ($lignes.Count -ne 2) { throw "Le fichier doit contenir une ligne HUE_BRIDGE= et une ligne HUE_KEY= (relancez hue-lister.ps1)" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) 'hue.env'
[IO.File]::WriteAllText($tmp, (($lignes -join "`n") + "`n"), [Text.Encoding]::ASCII)
try {
    $home_nas = '/var/services/homes/admin'
    scp -O -P $Port $tmp "${Nas}:$home_nas/hue.env"
    if ($LASTEXITCODE -ne 0) { throw 'Envoi de hue.env impossible' }
    scp -O -P $Port (Join-Path $PSScriptRoot 'hue-secrets-nas.sh') "${Nas}:$home_nas/hue-secrets-nas.sh"
    if ($LASTEXITCODE -ne 0) { throw 'Envoi de hue-secrets-nas.sh impossible' }
    ssh -t -p $Port $Nas "sudo sh $home_nas/hue-secrets-nas.sh; rm -f $home_nas/hue-secrets-nas.sh"
} finally {
    Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
}
