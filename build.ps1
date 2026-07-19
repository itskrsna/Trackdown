<#
.SYNOPSIS
    Windows-native build helper, mirroring the Makefile's targets for
    developers who prefer PowerShell over make.

.EXAMPLE
    ./build.ps1              # build trackdown.exe
    ./build.ps1 -Vet         # go vet ./...
    ./build.ps1 -Test        # go test ./...
    ./build.ps1 -Race        # go test -race ./... (needs CGO)
    ./build.ps1 -CrossCheck  # cross-compile sanity check for every target platform
    ./build.ps1 -Snapshot    # goreleaser build --snapshot --clean (local only)
    ./build.ps1 -Clean       # remove built binaries and dist/
#>
param(
    [switch]$Vet,
    [switch]$Test,
    [switch]$Race,
    [switch]$CrossCheck,
    [switch]$Snapshot,
    [switch]$Clean
)

$ErrorActionPreference = "Stop"

if ($Clean) {
    Remove-Item -Force -ErrorAction SilentlyContinue trackdown.exe
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue dist
    Write-Host "Cleaned."
    exit 0
}

if ($Vet) {
    go vet ./...
    exit $LASTEXITCODE
}

if ($Test) {
    go test ./...
    exit $LASTEXITCODE
}

if ($Race) {
    # Go's race detector needs CGO even for this pure-Go project -- 
    # where the MinGW gcc toolchain this needs on
    # Windows comes from.
    $env:CGO_ENABLED = "1"
    go test -race ./...
    exit $LASTEXITCODE
}

if ($CrossCheck) {
    $targets = @(
        @{ os = "darwin"; arch = "arm64" },
        @{ os = "darwin"; arch = "amd64" },
        @{ os = "linux"; arch = "amd64" },
        @{ os = "windows"; arch = "amd64" }
    )
    foreach ($t in $targets) {
        Write-Host "Cross-compiling for $($t.os)/$($t.arch)..."
        $env:GOOS = $t.os
        $env:GOARCH = $t.arch
        # No -o: building "./..." with multiple packages and no output path
        # just verifies compilation without writing binaries -- exactly
        # what a cross-compile sanity check needs.
        go build ./...
        if ($LASTEXITCODE -ne 0) {
            Write-Error "Cross-compile failed for $($t.os)/$($t.arch)"
            exit $LASTEXITCODE
        }
    }
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
    Write-Host "All cross-compile targets OK."
    exit 0
}

if ($Snapshot) {
    # Builds real cross-platform binaries into dist/ via goreleaser, entirely
    # locally -- --snapshot never touches a remote, never tags, never publishes.
    goreleaser build --snapshot --clean
    exit $LASTEXITCODE
}

go build -o trackdown.exe ./cmd/trackdown
Write-Host "Built trackdown.exe"
