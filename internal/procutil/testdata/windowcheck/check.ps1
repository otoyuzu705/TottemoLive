# Counts visible top-level windows that newly appear while a GUI-subsystem program
# (no console) launches child processes. 0 means no console window flashed.
# Keep this file ASCII: Windows PowerShell 5.1 misreads UTF-8 without BOM.
param(
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$Audio
)
Add-Type @"
using System; using System.Text; using System.Collections.Generic; using System.Runtime.InteropServices;
public class W {
  public delegate bool EnumProc(IntPtr h, IntPtr l);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc p, IntPtr l);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern int GetClassName(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  public static List<string> Visible() {
    var r = new List<string>();
    EnumWindows((h, l) => {
      if (IsWindowVisible(h)) {
        var c = new StringBuilder(256); GetClassName(h, c, 256);
        var t = new StringBuilder(256); GetWindowText(h, t, 256);
        uint pid; GetWindowThreadProcessId(h, out pid);
        r.Add(h.ToInt64() + "|" + c + "|" + t + "|" + pid);
      }
      return true;
    }, IntPtr.Zero);
    return r;
  }
}
"@
$before = [System.Collections.Generic.HashSet[string]]::new()
foreach ($e in [W]::Visible()) { [void]$before.Add($e.Split('|')[0]) }
$p = Start-Process -FilePath $Exe -ArgumentList "`"$Audio`"" -PassThru
$new = @{}
while (-not $p.HasExited) {
  foreach ($e in [W]::Visible()) {
    $f = $e.Split('|')
    if (-not $before.Contains($f[0])) { $new[$f[0]] = "class=" + $f[1] + " title='" + $f[2] + "' pid=" + $f[3] }
  }
  Start-Sleep -Milliseconds 5
}
"exit code = $($p.ExitCode); new visible windows during run = $($new.Count)"
$new.Values | Select-Object -First 5
if ($new.Count -gt 0) { exit 1 }
