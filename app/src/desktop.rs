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
    unread: u32,
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
        let open = MenuItem::new("Ouvrir Missive", true, None);
        let quit = MenuItem::new("Quitter", true, None);
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
            unread: 0,
        })
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
        let tooltip = match unread {
            0 => "Missive".to_string(),
            1 => "Missive · 1 message non lu".to_string(),
            n => format!("Missive · {n} messages non lus"),
        };
        let _ = self.icon.set_tooltip(Some(tooltip));
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
                let description: Vec<u16> = format!("{text} non lus")
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

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ShortcutRepair {
    Create,
    SetAppId,
    Nothing,
}

#[cfg_attr(not(windows), allow(dead_code))]
fn path_within(path: &std::path::Path, root: &std::path::Path) -> bool {
    let normalize = |p: &std::path::Path| {
        let mut text = p.to_string_lossy().replace('/', "\\").to_lowercase();
        while text.ends_with('\\') {
            text.pop();
        }
        text
    };
    let (path, root) = (normalize(path), normalize(root));
    !root.is_empty() && (path == root || path.starts_with(&format!("{root}\\")))
}

#[cfg_attr(not(windows), allow(dead_code))]
fn plan_shortcut_repair(
    existing: Option<(Option<std::path::PathBuf>, Option<String>)>,
    root: &std::path::Path,
    app_id: &str,
) -> ShortcutRepair {
    match existing {
        None => ShortcutRepair::Create,
        Some((Some(target), id)) if path_within(&target, root) => {
            if id.as_deref() == Some(app_id) {
                ShortcutRepair::Nothing
            } else {
                ShortcutRepair::SetAppId
            }
        }
        Some(_) => ShortcutRepair::Nothing,
    }
}

#[cfg(windows)]
mod shortcut {
    use std::path::{Path, PathBuf};

    use anyhow::Context as _;
    use windows::Win32::Foundation::PROPERTYKEY;
    use windows::Win32::System::Com::StructuredStorage::{PROPVARIANT, PropVariantClear};
    use windows::Win32::System::Com::{
        CLSCTX_INPROC_SERVER, COINIT_APARTMENTTHREADED, CoCreateInstance, CoInitializeEx,
        CoUninitialize, IPersistFile, STGM_READWRITE,
    };
    use windows::Win32::System::Variant::VT_LPWSTR;
    use windows::Win32::UI::Shell::PropertiesSystem::IPropertyStore;
    use windows::Win32::UI::Shell::{IShellLinkW, ShellLink};
    use windows::core::{GUID, HSTRING, Interface, PWSTR};

    use super::{ShortcutRepair, plan_shortcut_repair};

    const APP_ID_KEY: PROPERTYKEY = PROPERTYKEY {
        fmtid: GUID::from_u128(0x9f4c2855_9f79_4b39_a8d0_e1d42de1d5f3),
        pid: 5,
    };

    struct Com(bool);

    impl Drop for Com {
        fn drop(&mut self) {
            if self.0 {
                unsafe { CoUninitialize() };
            }
        }
    }

    fn target(link: &IShellLinkW) -> Option<PathBuf> {
        let mut buffer = [0u16; 1024];
        unsafe { link.GetPath(&mut buffer, std::ptr::null_mut(), 0) }.ok()?;
        let len = buffer.iter().position(|c| *c == 0).unwrap_or(buffer.len());
        (len > 0).then(|| PathBuf::from(String::from_utf16_lossy(&buffer[..len])))
    }

    fn app_id(store: &IPropertyStore) -> Option<String> {
        let mut value = unsafe { store.GetValue(&APP_ID_KEY) }.ok()?;
        let text = unsafe {
            let inner = &value.Anonymous.Anonymous;
            let pointer = inner.Anonymous.pwszVal;
            if inner.vt == VT_LPWSTR && !pointer.is_null() {
                pointer.to_string().ok()
            } else {
                None
            }
        };
        let _ = unsafe { PropVariantClear(&mut value) };
        text
    }

    fn set_app_id(store: &IPropertyStore, id: &str) -> windows::core::Result<()> {
        let mut wide: Vec<u16> = id.encode_utf16().chain(std::iter::once(0)).collect();
        let mut value = PROPVARIANT::default();
        unsafe {
            let inner = &mut value.Anonymous.Anonymous;
            inner.vt = VT_LPWSTR;
            inner.Anonymous.pwszVal = PWSTR(wide.as_mut_ptr());
            store.SetValue(&APP_ID_KEY, &value)?;
            store.Commit()
        }
    }

    pub(super) fn ensure(link_path: &Path, target_exe: &Path, root: &Path, id: &str) -> anyhow::Result<ShortcutRepair> {
        let _com = Com(unsafe { CoInitializeEx(None, COINIT_APARTMENTTHREADED) }.is_ok());
        let link: IShellLinkW = unsafe { CoCreateInstance(&ShellLink, None, CLSCTX_INPROC_SERVER) }
            .context("ShellLink unavailable")?;
        let file: IPersistFile = link.cast()?;
        let store: IPropertyStore = link.cast()?;
        let path = HSTRING::from(link_path.as_os_str());
        let existing = if link_path.exists() {
            unsafe { file.Load(&path, STGM_READWRITE) }.context("unreadable shortcut")?;
            Some((target(&link), app_id(&store)))
        } else {
            None
        };
        let plan = plan_shortcut_repair(existing, root, id);
        match plan {
            ShortcutRepair::Nothing => return Ok(plan),
            ShortcutRepair::Create => unsafe {
                link.SetPath(&HSTRING::from(target_exe.as_os_str()))?;
                link.SetWorkingDirectory(&HSTRING::from(root.as_os_str()))?;
                link.SetIconLocation(&HSTRING::from(target_exe.as_os_str()), 0)?;
                link.SetDescription(&HSTRING::from("Missive"))?;
            },
            ShortcutRepair::SetAppId => {}
        }
        set_app_id(&store, id)?;
        if let Some(parent) = link_path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        unsafe { file.Save(&path, true) }?;
        Ok(plan)
    }
}

#[cfg(windows)]
fn repair_start_menu_shortcut_now() -> anyhow::Result<()> {
    use anyhow::Context as _;
    use velopack::locator::{LocationContext, ShortcutLocationFlags, auto_locate_app_manifest};

    let Ok(locator) = auto_locate_app_manifest(LocationContext::FromCurrentExe) else {
        return Ok(());
    };
    if locator.get_is_portable()
        || !locator
            .get_manifest_shortcut_locations()
            .contains(ShortcutLocationFlags::START_MENU_ROOT)
    {
        return Ok(());
    }
    let root = locator.get_root_dir();
    let main_exe = locator.get_main_exe_path();
    let target = main_exe
        .file_name()
        .map(|name| root.join(name))
        .filter(|stub| stub.is_file())
        .unwrap_or(main_exe);
    let title = locator.get_manifest_title();
    let title = if title.trim().is_empty() {
        "Missive".to_string()
    } else {
        title
    };
    let link = dirs::data_dir()
        .context("no roaming data directory")?
        .join(r"Microsoft\Windows\Start Menu\Programs")
        .join(format!("{title}.lnk"));
    match shortcut::ensure(&link, &target, &root, crate::notifications::APP_ID)? {
        ShortcutRepair::Create => eprintln!("[app] start menu shortcut recreated"),
        ShortcutRepair::SetAppId => eprintln!("[app] start menu shortcut app id restored"),
        ShortcutRepair::Nothing => {}
    }
    Ok(())
}

#[cfg(windows)]
pub fn repair_start_menu_shortcut() {
    let marker = crate::storage::data_file("shortcut-checked.txt");
    let version = env!("CARGO_PKG_VERSION");
    if marker
        .as_ref()
        .and_then(|path| std::fs::read_to_string(path).ok())
        .is_some_and(|checked| checked.trim() == version)
    {
        return;
    }
    let spawned = std::thread::Builder::new()
        .name("shortcut-repair".into())
        .spawn(move || {
            std::thread::sleep(std::time::Duration::from_secs(20));
            match repair_start_menu_shortcut_now() {
                Ok(()) => {
                    if let Some(path) = marker {
                        let _ = std::fs::write(path, version);
                    }
                }
                Err(err) => eprintln!("[app] start menu shortcut check failed: {err:#}"),
            }
        });
    if let Err(err) = spawned {
        eprintln!("[app] start menu shortcut check not started: {err}");
    }
}

#[cfg(not(windows))]
pub fn repair_start_menu_shortcut() {}

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

#[cfg(test)]
mod tests {
    use super::{ShortcutRepair, path_within, plan_shortcut_repair};
    use std::path::{Path, PathBuf};

    const ID: &str = "Missive.Desktop";

    #[::core::prelude::v1::test]
    fn path_within_is_case_insensitive_and_bounded() {
        let root = Path::new(r"C:\Users\u\AppData\Local\Missive");
        assert!(path_within(Path::new(r"c:\users\u\appdata\local\missive\Missive.exe"), root));
        assert!(path_within(Path::new(r"C:\Users\u\AppData\Local\Missive\current\missive.exe"), root));
        assert!(path_within(Path::new(r"C:\Users\u\AppData\Local\Missive\"), root));
        assert!(!path_within(Path::new(r"C:\Users\u\AppData\Local\MissiveOld\missive.exe"), root));
        assert!(!path_within(Path::new(r"C:\Program Files\Other\other.exe"), root));
        assert!(!path_within(Path::new(r"C:\x.exe"), Path::new("")));
    }

    #[::core::prelude::v1::test]
    fn shortcut_repair_plan() {
        let root = Path::new(r"C:\Users\u\AppData\Local\Missive");
        let ours = Some(PathBuf::from(r"C:\Users\u\AppData\Local\Missive\Missive.exe"));
        assert_eq!(plan_shortcut_repair(None, root, ID), ShortcutRepair::Create);
        assert_eq!(
            plan_shortcut_repair(Some((ours.clone(), None)), root, ID),
            ShortcutRepair::SetAppId
        );
        assert_eq!(
            plan_shortcut_repair(Some((ours.clone(), Some("velopack.Missive".into()))), root, ID),
            ShortcutRepair::SetAppId
        );
        assert_eq!(
            plan_shortcut_repair(Some((ours, Some(ID.into()))), root, ID),
            ShortcutRepair::Nothing
        );
        assert_eq!(
            plan_shortcut_repair(Some((Some(PathBuf::from(r"D:\Autre\app.exe")), None)), root, ID),
            ShortcutRepair::Nothing
        );
        assert_eq!(plan_shortcut_repair(Some((None, None)), root, ID), ShortcutRepair::Nothing);
    }
}
