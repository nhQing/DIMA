//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// pickFolder shows the Windows folder chooser.
//
// The dialog is opened by powershell.exe instead of by calling shell32 from
// this process. Doing it by hand worked, but it made the unsigned binary
// look suspicious to Smart App Control, which then refused to run DIMA at
// all — an app that will not start is worse than a dialog that takes an
// extra second to appear. powershell.exe ships signed by Microsoft, so this
// route is allowed. Its console window is suppressed by hideWindow.
//
// It is the Explorer-style chooser (IFileOpenDialog with FOS_PICKFOLDERS) —
// address bar, Quick access and This PC on the left — the same one a browser
// shows when it asks where to save a download. WinForms' FolderBrowserDialog
// on .NET Framework only offers the old XP tree, so it is not used.
//
// powershell runs in the background and has no right to take the
// foreground, so a dialog opened straight away lands behind DIMA's window.
// It is therefore owned by an invisible TopMost window: windows owned by a
// topmost window stay on top too, and the Alt press/release unlocks
// SetForegroundWindow so the dialog also gets the keyboard.
func pickFolder(title string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePowerShell(pickerScript))
	// The title goes in through stdin rather than into the script, so no
	// character in it can break the PowerShell string.
	cmd.Stdin = strings.NewReader(title)
	hideWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("không mở được hộp thoại chọn thư mục: %s", msg)
		}
		return "", fmt.Errorf("không mở được hộp thoại chọn thư mục: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// encodePowerShell turns a script into what -EncodedCommand expects:
// base64 of its UTF-16LE bytes.
func encodePowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

const pickerScript = `$ErrorActionPreference = 'Stop'
trap { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
[Console]::InputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -ReferencedAssemblies System.Windows.Forms, System.Drawing -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Windows.Forms;

public static class DimaFolderPicker {
  [ComImport, Guid("DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7")]
  class FileOpenDialogCoClass {}

  // Method order must match the IFileDialog vtable; declared up to GetResult.
  [ComImport, Guid("42f85136-db7e-439c-85f1-e4075d135fc8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  interface IFileOpenDialog {
    [PreserveSig] int Show(IntPtr parent);
    void SetFileTypes(uint cFileTypes, IntPtr rgFilterSpec);
    void SetFileTypeIndex(uint iFileType);
    void GetFileTypeIndex(out uint piFileType);
    void Advise(IntPtr pfde, out uint pdwCookie);
    void Unadvise(uint dwCookie);
    void SetOptions(uint fos);
    void GetOptions(out uint pfos);
    void SetDefaultFolder(IShellItem psi);
    void SetFolder(IShellItem psi);
    void GetFolder(out IShellItem ppsi);
    void GetCurrentSelection(out IShellItem ppsi);
    void SetFileName([MarshalAs(UnmanagedType.LPWStr)] string pszName);
    void GetFileName([MarshalAs(UnmanagedType.LPWStr)] out string pszName);
    void SetTitle([MarshalAs(UnmanagedType.LPWStr)] string pszTitle);
    void SetOkButtonLabel([MarshalAs(UnmanagedType.LPWStr)] string pszText);
    void SetFileNameLabel([MarshalAs(UnmanagedType.LPWStr)] string pszLabel);
    void GetResult(out IShellItem ppsi);
  }

  [ComImport, Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  interface IShellItem {
    void BindToHandler(IntPtr pbc, [MarshalAs(UnmanagedType.LPStruct)] Guid bhid, [MarshalAs(UnmanagedType.LPStruct)] Guid riid, out IntPtr ppv);
    void GetParent(out IShellItem ppsi);
    void GetDisplayName(uint sigdnName, [MarshalAs(UnmanagedType.LPWStr)] out string ppszName);
  }

  [DllImport("user32.dll")] static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] static extern void keybd_event(byte bVk, byte bScan, uint dwFlags, UIntPtr dwExtraInfo);
  [DllImport("user32.dll")] static extern IntPtr GetWindow(IntPtr hWnd, uint uCmd);
  [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll")] static extern bool GetWindowRect(IntPtr hWnd, out RECT lpRect);
  [DllImport("user32.dll")] static extern bool SetWindowPos(IntPtr hWnd, IntPtr after, int x, int y, int cx, int cy, uint flags);
  [StructLayout(LayoutKind.Sequential)] struct RECT { public int Left, Top, Right, Bottom; }

  const uint FOS_PICKFOLDERS = 0x20, FOS_FORCEFILESYSTEM = 0x40, FOS_PATHMUSTEXIST = 0x800;
  const uint SIGDN_FILESYSPATH = 0x80058000;
  const int ERROR_CANCELLED = unchecked((int)0x800704C7);
  const uint GW_ENABLEDPOPUP = 6, SWP_NOSIZE = 0x1, SWP_NOZORDER = 0x4;

  // The dialog opens with its corner at the owner's position, which pushes
  // most of it off the bottom-right of the screen. The timer ticks inside
  // the dialog's own message loop, so it catches the dialog as soon as it is
  // on screen and moves it to the middle of that monitor. Right after it
  // appears the dialog places itself again (and may grow to its remembered
  // size), undoing the first move, so it is put back in the middle until it
  // has stayed there for a while.
  static void CenterWhenShown(Form owner) {
    Timer t = new Timer();
    t.Interval = 15;
    int still = 0, ticks = 0;
    bool focused = false;
    t.Tick += delegate {
      if (++ticks > 200) { t.Stop(); return; }
      IntPtr h = GetWindow(owner.Handle, GW_ENABLEDPOPUP);
      if (h == IntPtr.Zero || !IsWindowVisible(h)) return;
      if (!focused) { SetForegroundWindow(h); focused = true; }
      RECT r;
      GetWindowRect(h, out r);
      int w = r.Right - r.Left, ht = r.Bottom - r.Top;
      System.Drawing.Rectangle wa = Screen.FromHandle(h).WorkingArea;
      int x = wa.Left + Math.Max(0, (wa.Width - w) / 2);
      int y = wa.Top + Math.Max(0, (wa.Height - ht) / 2);
      if (r.Left == x && r.Top == y) {
        if (++still >= 20) t.Stop();
        return;
      }
      still = 0;
      SetWindowPos(h, IntPtr.Zero, x, y, 0, 0, SWP_NOSIZE | SWP_NOZORDER);
    };
    t.Start();
  }

  public static string Pick(string title) {
    Form owner = new Form();
    owner.TopMost = true;
    owner.ShowInTaskbar = false;
    owner.FormBorderStyle = FormBorderStyle.None;
    owner.StartPosition = FormStartPosition.CenterScreen;
    owner.Size = new System.Drawing.Size(1, 1);
    owner.Opacity = 0;
    owner.Show();
    keybd_event(0x12, 0, 0, UIntPtr.Zero);
    keybd_event(0x12, 0, 2, UIntPtr.Zero);
    SetForegroundWindow(owner.Handle);
    owner.Activate();

    try {
      IFileOpenDialog dlg = (IFileOpenDialog)new FileOpenDialogCoClass();
      dlg.SetOptions(FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM | FOS_PATHMUSTEXIST);
      if (!string.IsNullOrEmpty(title)) dlg.SetTitle(title);
      dlg.SetOkButtonLabel("Chọn thư mục");
      CenterWhenShown(owner);
      int hr = dlg.Show(owner.Handle);
      if (hr == ERROR_CANCELLED) return "";
      if (hr != 0) Marshal.ThrowExceptionForHR(hr);
      IShellItem item;
      dlg.GetResult(out item);
      string path;
      item.GetDisplayName(SIGDN_FILESYSPATH, out path);
      return path;
    } finally {
      owner.Close();
    }
  }
}
'@
[Console]::Out.Write([DimaFolderPicker]::Pick([Console]::In.ReadToEnd().Trim()))
`
