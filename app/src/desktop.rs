use gpui_kit::Window;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TrayAction {
    Open,
    Quit,
}

pub struct Tray {
    icon: tray_icon::TrayIcon,
    open_id: tray_icon::menu::MenuId,
    quit_id: tray_icon::menu::MenuId,
    open_item: tray_icon::menu::MenuItem,
    quit_item: tray_icon::menu::MenuItem,
    unread: u32,
}

fn tooltip(unread: u32) -> String {
    if unread == 0 {
        "Missive".to_string()
    } else {
        tn!("desktop.tray.tooltip_unread", unread).into_owned()
    }
}

const ICON_SIZE: u32 = 32;

fn icon(unread: u32) -> Option<tray_icon::Icon> {
    tray_icon::Icon::from_rgba(crate::logo::tray_rgba(ICON_SIZE, unread), ICON_SIZE, ICON_SIZE)
        .ok()
}

impl Tray {
    pub fn create() -> Option<Self> {
        use tray_icon::menu::{Menu, MenuItem, PredefinedMenuItem};

        let menu = Menu::new();
        let open = MenuItem::new(t!("desktop.tray.open"), true, None);
        let quit = MenuItem::new(t!("desktop.tray.quit"), true, None);
        menu.append_items(&[&open, &PredefinedMenuItem::separator(), &quit])
            .ok()?;
        let icon = tray_icon::TrayIconBuilder::new()
            .with_menu(Box::new(menu))
            .with_menu_on_left_click(false)
            .with_tooltip("Missive")
            .with_icon(icon(0)?)
            .build()
            .map_err(|err| eprintln!("[app] tray icon unavailable: {err}"))
            .ok()?;
        Some(Self {
            icon,
            open_id: open.id().clone(),
            quit_id: quit.id().clone(),
            open_item: open,
            quit_item: quit,
            unread: 0,
        })
    }

    pub fn refresh_language(&self) {
        self.open_item.set_text(t!("desktop.tray.open"));
        self.quit_item.set_text(t!("desktop.tray.quit"));
        let _ = self.icon.set_tooltip(Some(tooltip(self.unread)));
    }

    pub fn poll(&self) -> Vec<TrayAction> {
        let mut actions = Vec::new();
        while let Ok(event) = tray_icon::TrayIconEvent::receiver().try_recv() {
            if let tray_icon::TrayIconEvent::Click {
                button: tray_icon::MouseButton::Left,
                button_state: tray_icon::MouseButtonState::Up,
                ..
            } = event
            {
                actions.push(TrayAction::Open);
            }
        }
        while let Ok(event) = tray_icon::menu::MenuEvent::receiver().try_recv() {
            if event.id == self.open_id {
                actions.push(TrayAction::Open);
            } else if event.id == self.quit_id {
                actions.push(TrayAction::Quit);
            }
        }
        actions
    }

    pub fn set_unread(&mut self, unread: u32) {
        if unread == self.unread {
            return;
        }
        let previous = self.unread;
        self.unread = unread;
        let _ = self.icon.set_tooltip(Some(tooltip(unread)));
        let label = |count: u32| (count > 0).then(|| crate::logo::badge_label(count));
        if label(previous) != label(unread) {
            let _ = self.icon.set_icon(icon(unread));
        }
    }
}

#[cfg(windows)]
thread_local! {
    static TASKBAR: std::cell::RefCell<TaskbarBadge> = std::cell::RefCell::new(TaskbarBadge::default());
}

#[cfg(windows)]
#[derive(Default)]
struct TaskbarBadge {
    list: Option<windows::Win32::UI::Shell::ITaskbarList3>,
    applied: Option<Option<String>>,
    failed: bool,
}

#[cfg(windows)]
pub fn set_taskbar_badge(window: &Window, unread: u32) {
    use windows::Win32::Foundation::HWND;
    use windows::Win32::System::Com::{CLSCTX_INPROC_SERVER, CoCreateInstance};
    use windows::Win32::UI::Shell::{ITaskbarList3, TaskbarList};
    use windows::Win32::UI::WindowsAndMessaging::HICON;
    use windows::core::PCWSTR;
    use windows_sys::Win32::UI::WindowsAndMessaging::{CreateIconFromResourceEx, DestroyIcon};

    let label = (unread > 0).then(|| crate::logo::badge_label(unread));
    let Some(hwnd) = hwnd(window) else { return };
    TASKBAR.with(|cell| {
        let mut state = cell.borrow_mut();
        if state.failed || state.applied.as_ref() == Some(&label) {
            return;
        }
        if state.list.is_none() {
            let created: windows::core::Result<ITaskbarList3> =
                unsafe { CoCreateInstance(&TaskbarList, None, CLSCTX_INPROC_SERVER) };
            match created {
                Ok(list) if unsafe { list.HrInit() }.is_ok() => state.list = Some(list),
                _ => {
                    state.failed = true;
                    return;
                }
            }
        }
        let Some(list) = state.list.clone() else { return };
        let hwnd = HWND(hwnd as *mut core::ffi::c_void);
        let result = match &label {
            None => unsafe { list.SetOverlayIcon(hwnd, HICON::default(), PCWSTR::null()) },
            Some(text) => {
                let size = 32u32;
                let bits = crate::logo::overlay_resource(size, unread);
                let raw = unsafe {
                    CreateIconFromResourceEx(
                        bits.as_ptr(),
                        bits.len() as u32,
                        1,
                        0x0003_0000,
                        size as i32,
                        size as i32,
                        0,
                    )
                };
                if raw.is_null() {
                    return;
                }
                let description: Vec<u16> = t!("desktop.taskbar.unread_badge", text = text).into_owned()
                    .encode_utf16()
                    .chain(std::iter::once(0))
                    .collect();
                let result = unsafe {
                    list.SetOverlayIcon(hwnd, HICON(raw as _), PCWSTR(description.as_ptr()))
                };
                unsafe { DestroyIcon(raw) };
                result
            }
        };
        if result.is_ok() {
            state.applied = Some(label);
        }
    });
}

#[cfg(not(windows))]
pub fn set_taskbar_badge(_window: &Window, _unread: u32) {}

#[cfg(windows)]
pub fn reset_taskbar_badge() {
    TASKBAR.with(|cell| cell.borrow_mut().applied = None);
}

#[cfg(not(windows))]
pub fn reset_taskbar_badge() {}

#[cfg(windows)]
fn hwnd(window: &Window) -> Option<isize> {
    use raw_window_handle::{HasWindowHandle, RawWindowHandle};
    match HasWindowHandle::window_handle(window).ok()?.as_raw() {
        RawWindowHandle::Win32(handle) => Some(handle.hwnd.get()),
        _ => None,
    }
}

#[cfg(windows)]
pub fn hide_window(window: &Window) -> bool {
    use windows_sys::Win32::UI::WindowsAndMessaging::{SW_HIDE, ShowWindow};
    let Some(hwnd) = hwnd(window) else {
        return false;
    };
    unsafe { ShowWindow(hwnd as _, SW_HIDE) };
    true
}

#[cfg(windows)]
pub fn set_window_icon(window: &Window) {
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        CreateIconFromResourceEx, ICON_BIG, ICON_SMALL, SendMessageW, WM_SETICON,
    };
    let Some(hwnd) = hwnd(window) else { return };
    for (kind, size) in [(ICON_BIG, 48u32), (ICON_SMALL, 24u32)] {
        let bits = crate::logo::icon_resource(size);
        unsafe {
            let icon = CreateIconFromResourceEx(
                bits.as_ptr(),
                bits.len() as u32,
                1,
                0x0003_0000,
                size as i32,
                size as i32,
                0,
            );
            if !icon.is_null() {
                SendMessageW(hwnd as _, WM_SETICON, kind as usize, icon as isize);
            }
        }
    }
}

#[cfg(not(windows))]
pub fn set_window_icon(_window: &Window) {}

#[cfg(not(windows))]
pub fn hide_window(window: &Window) -> bool {
    window.minimize_window();
    true
}

#[cfg(windows)]
pub fn show_window(window: &Window) {
    use windows_sys::Win32::UI::WindowsAndMessaging::{SW_RESTORE, SW_SHOW, ShowWindow};
    if let Some(hwnd) = hwnd(window) {
        unsafe {
            ShowWindow(hwnd as _, SW_SHOW);
            ShowWindow(hwnd as _, SW_RESTORE);
        }
    }
    reset_taskbar_badge();
    window.activate_window();
}

#[cfg(not(windows))]
pub fn show_window(window: &Window) {
    window.activate_window();
}

#[cfg(windows)]
pub fn set_launch_at_login(enabled: bool) -> anyhow::Result<()> {
    use winreg::{RegKey, enums::HKEY_CURRENT_USER};
    let (key, _) = RegKey::predef(HKEY_CURRENT_USER)
        .create_subkey(r"Software\Microsoft\Windows\CurrentVersion\Run")?;
    if enabled {
        let exe = std::env::current_exe()?;
        key.set_value("Missive", &format!("\"{}\" --background", exe.display()))?;
    } else {
        match key.delete_value("Missive") {
            Ok(()) => {}
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => {}
            Err(err) => return Err(err.into()),
        }
    }
    Ok(())
}

#[cfg(target_os = "macos")]
pub fn set_launch_at_login(enabled: bool) -> anyhow::Result<()> {
    let dir = dirs::home_dir()
        .ok_or_else(|| anyhow::anyhow!("no home directory"))?
        .join("Library/LaunchAgents");
    let plist = dir.join("app.missive.desktop.plist");
    if enabled {
        std::fs::create_dir_all(&dir)?;
        let exe = std::env::current_exe()?;
        let body = format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict><key>Label</key><string>app.missive.desktop</string><key>ProgramArguments</key><array><string>{}</string><string>--background</string></array><key>RunAtLoad</key><true/></dict></plist>\n",
            exe.display()
        );
        std::fs::write(plist, body)?;
    } else if plist.exists() {
        std::fs::remove_file(plist)?;
    }
    Ok(())
}

#[cfg(not(any(windows, target_os = "macos")))]
pub fn set_launch_at_login(_enabled: bool) -> anyhow::Result<()> {
    Ok(())
}

#[cfg(windows)]
pub struct Instance {
    _mutex: isize,
    show_event: isize,
}

#[cfg(windows)]
fn wide(text: &str) -> Vec<u16> {
    text.encode_utf16().chain(std::iter::once(0)).collect()
}

#[cfg(windows)]
impl Instance {
    pub fn acquire() -> Option<Self> {
        use windows_sys::Win32::Foundation::{ERROR_ALREADY_EXISTS, GetLastError};
        use windows_sys::Win32::System::Threading::{CreateEventW, CreateMutexW, SetEvent};

        let scope = if crate::bridge::demo_requested() { ".Demo" } else { "" };
        let mutex_name = wide(&format!("Local\\Missive.Desktop.Instance{scope}"));
        let event_name = wide(&format!("Local\\Missive.Desktop.Show{scope}"));
        unsafe {
            let mutex = CreateMutexW(std::ptr::null(), 1, mutex_name.as_ptr());
            let already_running = GetLastError() == ERROR_ALREADY_EXISTS;
            let show_event = CreateEventW(std::ptr::null(), 0, 0, event_name.as_ptr());
            if already_running {
                if !show_event.is_null() {
                    SetEvent(show_event);
                }
                return None;
            }
            Some(Self {
                _mutex: mutex as isize,
                show_event: show_event as isize,
            })
        }
    }

    pub fn take_show_request(&self) -> bool {
        use windows_sys::Win32::Foundation::WAIT_OBJECT_0;
        use windows_sys::Win32::System::Threading::WaitForSingleObject;
        if self.show_event == 0 {
            return false;
        }
        unsafe { WaitForSingleObject(self.show_event as _, 0) == WAIT_OBJECT_0 }
    }
}

#[cfg(not(windows))]
pub struct Instance;

#[cfg(not(windows))]
impl Instance {
    pub fn acquire() -> Option<Self> {
        Some(Self)
    }

    pub fn take_show_request(&self) -> bool {
        false
    }
}

const SAFE_TO_OPEN: [&str; 16] = [
    "png", "jpg", "jpeg", "gif", "webp", "bmp", "heic", "mp4", "mov", "webm", "mp3", "m4a", "ogg",
    "wav", "pdf", "txt",
];

pub fn open_local(path: &std::path::Path, cx: &mut gpui_kit::App) {
    let safe = path
        .extension()
        .map(|ext| ext.to_string_lossy().to_lowercase())
        .is_some_and(|ext| SAFE_TO_OPEN.contains(&ext.as_str()));
    if safe {
        cx.open_with_system(path);
    } else {
        cx.reveal_path(path);
    }
}

pub fn open_web(url: &str, cx: &mut gpui_kit::App) {
    if url.starts_with("https://") || url.starts_with("http://") {
        cx.open_url(url);
    }
}

pub fn started_in_background() -> bool {
    std::env::args().any(|arg| arg == "--background")
}
