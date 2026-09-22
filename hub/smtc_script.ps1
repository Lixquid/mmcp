# Helper script for the MMCP hub's SMTC simulator (Windows only).
#
# It monitors every System Media Transport Controls session and reports a
# compact snapshot of each one as a JSON line on stdout once per second
# ({"t":"tick","sessions":[...]}). Control commands arrive as JSON lines on
# stdin ({"c":"PLAY"|"PAUSE"|"NEXT"|"PREV"|"SEEK","sid":...,"arg":...}).
#
# Requires Windows PowerShell 5.1 (ships with Windows 10/11); the WinRT
# projection for Windows.Media.Control is only available there.

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
try { [Console]::InputEncoding = [System.Text.Encoding]::UTF8 } catch { }

Add-Type -AssemblyName System.Runtime.WindowsRuntime

# AsTask overloads for awaiting WinRT IAsyncOperation / IAsyncAction.
$asTaskOp = [System.WindowsRuntimeSystemExtensions].GetMethods() |
    Where-Object {
        $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and
        $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation`1'
    } | Select-Object -First 1
$asAction = [System.WindowsRuntimeSystemExtensions].GetMethods() |
    Where-Object {
        $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and
        $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncAction'
    } | Select-Object -First 1
if (-not $asTaskOp -or -not $asAction) {
    throw 'WindowsRuntime AsTask helpers not available'
}

function AwaitOp($op, $t) {
    $task = $asTaskOp.MakeGenericMethod($t).Invoke($null, @($op))
    $task.Wait()
    $task.Result
}
function AwaitAction($act) {
    $task = $asAction.Invoke($null, @($act))
    $task.Wait()
}

# Activate the SMTC session manager.
[void][Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager,Windows.Media.Control,ContentType=WindowsRuntime]
$mgr = AwaitOp ([Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager]::RequestAsync()) `
    ([Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager])

# Command loop: a separate in-process runspace reads JSON commands from
# stdin and performs them on the matching session, so the monitor loop can
# keep writing snapshots.
$cmdScript = @'
param($mgr, $asTaskOp, $asAction)
function AwaitOp($op, $t) {
    $task = $asTaskOp.MakeGenericMethod($t).Invoke($null, @($op))
    $task.Wait()
    $task.Result
}
function AwaitAction($act) {
    $task = $asAction.Invoke($null, @($act))
    $task.Wait()
}
while ($true) {
    $line = [Console]::In.ReadLine()
    if ($null -eq $line) { return }
    try {
        $c = $line | ConvertFrom-Json
        $session = $null
        foreach ($s in $mgr.GetSessions()) {
            if ($s.SessionId -eq $c.sid) { $session = $s; break }
        }
        if ($session) {
            switch ($c.c) {
                'PLAY'  { AwaitAction ($session.TryPlayAsync()) }
                'PAUSE' { AwaitAction ($session.TryPauseAsync()) }
                'NEXT'  { AwaitAction ($session.TrySkipNextAsync()) }
                'PREV'  { AwaitAction ($session.TrySkipPreviousAsync()) }
                'SEEK'  {
                    # MMCP SEEK carries the target position in seconds; SMTC
                    # wants hundreds of nanoseconds.
                    $ticks = [int64][Math]::Round([double]$c.arg * 10000000)
                    AwaitAction ($session.TryChangePlaybackPositionAsync($ticks))
                }
            }
        }
    } catch { }
}
'@
$cmdPS = [powershell]::Create()
$cmdPS.Runspace.SessionStateProxy.SetVariable('mgr', $mgr)
$cmdPS.Runspace.SessionStateProxy.SetVariable('asTaskOp', $asTaskOp)
$cmdPS.Runspace.SessionStateProxy.SetVariable('asAction', $asAction)
$null = $cmdPS.AddScript($cmdScript).BeginInvoke()

# Monitor loop: one JSON line per tick with a snapshot of every session.
while ($true) {
    try {
        $snaps = @()
        foreach ($s in $mgr.GetSessions()) {
            try {
                $pi = $s.GetPlaybackInfo()
                $status = $pi.PlaybackStatus.ToString()
                $tl = $s.GetTimelineProperties()
                $mp = AwaitOp ($s.GetMediaPropertiesAsync()) `
                    ([Windows.Media.Control.GlobalSystemMediaTransportControlsSessionMediaProperties])
                $pos = $tl.Position.TotalSeconds
                if ($status -eq 'Playing') {
                    # Timeline positions only advance on events: interpolate
                    # the effective position from LastUpdatedTime.
                    $drift = ([DateTimeOffset]::Now - $tl.LastUpdatedTime).TotalSeconds
                    if ($drift -gt 0) { $pos += $drift }
                }
                $snaps += @{
                    id     = $s.SessionId
                    app    = $s.SourceAppUserModelId
                    status = $status
                    title  = [string]$mp.Title
                    artist = [string]$mp.Artist
                    album  = [string]$mp.AlbumTitle
                    pos    = [Math]::Round($pos, 1)
                    len    = [Math]::Round($tl.EndTime.TotalSeconds, 1)
                }
            } catch { }
        }
        @{ t = 'tick'; sessions = $snaps } | ConvertTo-Json -Compress -Depth 4
    } catch {
        # The session manager is gone: exit so the hub respawns the helper.
        exit 1
    }
    Start-Sleep -Milliseconds 1000
}