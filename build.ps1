# Build xsd2code-go.exe locally.
#
# Released builds are versioned v0.1.<run number> by the GitHub Actions
# pipeline. A local build has no run number, so it stamps 0.1.0000-dev: the
# version string alone tells you a binary did not come from CI.
$ErrorActionPreference = "Stop"
$version = "0.1.0000-dev"

# The commit is a nicety, not a requirement: building from an unpacked archive
# has to work. Outside a repository git writes to stderr and exits non-zero,
# which Windows PowerShell turns into a terminating error while the preference
# above is in force, so it is relaxed for this one call.
$sha = $null
try {
    $ErrorActionPreference = "Continue"
    $sha = git rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -ne 0) { $sha = $null }
} catch {
    $sha = $null
} finally {
    $ErrorActionPreference = "Stop"
}
if ($sha) { $version = "$version+$sha" }
go build -trimpath -ldflags "-s -w -X main.version=$version" -o xsd2code-go.exe ./cmd/xsd2code-go
Write-Host "built xsd2code-go.exe $version"
