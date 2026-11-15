# clipnest Windows 冒烟探针：窗口层级 / 前台进程 / 发送 Ctrl+V。
# 用法（在 Windows PowerShell 里，仓库根目录）：
#   powershell -ExecutionPolicy Bypass -File tools\win-smoke.ps1 -Action windows
#   powershell -ExecutionPolicy Bypass -File tools\win-smoke.ps1 -Action front
#   powershell -ExecutionPolicy Bypass -File tools\win-smoke.ps1 -Action paste
param(
    [ValidateSet("windows", "front", "paste")]
    [string]$Action = "windows",
    [string]$Owner = "clipnest"
)

Add-Type -Namespace Win -Name User32 -MemberDefinition @'
[DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);
public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
[DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
[DllImport("user32.dll")] public static extern int GetWindowLong(IntPtr hWnd, int nIndex);
[DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
[DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowText(IntPtr hWnd, System.Text.StringBuilder text, int max);
[DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetClassName(IntPtr hWnd, System.Text.StringBuilder text, int max);
[DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
[DllImport("user32.dll")] public static extern void keybd_event(byte bVk, byte bScan, uint dwFlags, UIntPtr dwExtraInfo);
'@

$WS_EX_TOPMOST = 0x8
$WS_EX_TOOLWINDOW = 0x80
$GWL_EXSTYLE = -20
$VK_CONTROL = 0x11
$VK_V = 0x56
$KEYUP = 0x2

function Get-FrontProcess {
    $h = [Win.User32]::GetForegroundWindow()
    if ($h -eq [IntPtr]::Zero) { return "foreground=<none>" }
    $pid = 0
    [void][Win.User32]::GetWindowThreadProcessId($h, [ref]$pid)
    $p = Get-Process -Id $pid -ErrorAction SilentlyContinue
    $name = if ($p) { $p.ProcessName } else { "pid=$pid" }
    $sb = New-Object System.Text.StringBuilder 256
    [void][Win.User32]::GetWindowText($h, $sb, 256)
    "foreground=$name title=[$($sb.ToString())]"
}

switch ($Action) {
    "front" {
        Get-FrontProcess
    }
    "paste" {
        # Ctrl+V：先按下 Ctrl，再点 V，再一起抬起。
        [Win.User32]::keybd_event($VK_CONTROL, 0, 0, [UIntPtr]::Zero)
        [Win.User32]::keybd_event($VK_V, 0, 0, [UIntPtr]::Zero)
        Start-Sleep -Milliseconds 40
        [Win.User32]::keybd_event($VK_V, 0, $KEYUP, [UIntPtr]::Zero)
        [Win.User32]::keybd_event($VK_CONTROL, 0, $KEYUP, [UIntPtr]::Zero)
        "posted ctrl+v"
        Get-FrontProcess
    }
    default {
        Get-FrontProcess
        # 列出属于 clipnest 的顶层窗口：是否可见、是否 topmost/toolwindow、类名、标题。
        # 期望：一个 Popup 面板（可见）＋主窗口不可见或被挪出屏幕。
        $found = 0
        $cb = [Win.User32+EnumWindowsProc] {
            param($hWnd, $lParam)
            $pid = 0
            [void][Win.User32]::GetWindowThreadProcessId($hWnd, [ref]$pid)
            $p = Get-Process -Id $pid -ErrorAction SilentlyContinue
            if ($p -and $p.ProcessName -like "*$Owner*") {
                $script:found++
                $style = [Win.User32]::GetWindowLong($hWnd, $GWL_EXSTYLE)
                $cls = New-Object System.Text.StringBuilder 256
                [void][Win.User32]::GetClassName($hWnd, $cls, 256)
                $title = New-Object System.Text.StringBuilder 256
                [void][Win.User32]::GetWindowText($hWnd, $title, 256)
                $topmost = if ($style -band $WS_EX_TOPMOST) { "yes" } else { "no" }
                $tool = if ($style -band $WS_EX_TOOLWINDOW) { "yes" } else { "no" }
                $vis = [Win.User32]::IsWindowVisible($hWnd)
                "hwnd=$hWnd class=$($cls.ToString()) visible=$vis topmost=$topmost toolwindow=$tool title=[$($title.ToString())]"
            }
            return $true
        }
        [void][Win.User32]::EnumWindows($cb, [IntPtr]::Zero)
        if ($script:found -eq 0) { "no window of process like *$Owner*" }
    }
}
