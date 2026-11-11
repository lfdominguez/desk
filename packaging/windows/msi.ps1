# msi.ps1: desk's MSI, on Windows, from the folder packaging/windows/build.sh made
# (dist/windows/msi: desk.exe, the icon, the license, the wizard's pictures, desk.wxs, version).
# Needs WiX v5 and its UI and Util extensions:
#
#   dotnet tool install --global wix --version 5.0.2
#   wix extension add -g WixToolset.UI.wixext/5.0.2
#   wix extension add -g WixToolset.Util.wixext/5.0.2
#   pwsh dist/windows/msi/msi.ps1 desk-2026.11.03-windows-amd64.msi
param([Parameter(Mandatory)][string]$Out)
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$version = (Get-Content "$here/version").Trim()
wix build -arch x64 -ext WixToolset.UI.wixext -ext WixToolset.Util.wixext `
	-d "Version=$version" -d "Exe=$here/desk.exe" -d "Icon=$here/desk.ico" `
	-d "License=$here/LICENSE.txt" -d "LicenseRtf=$here/license.rtf" `
	-d "Banner=$here/banner.bmp" -d "Dialog=$here/dialog.bmp" `
	-o $Out "$here/desk.wxs"
if ($LASTEXITCODE) { exit $LASTEXITCODE }
Write-Output "$Out (ProductVersion $version)"
