use std::sync::mpsc::Sender;

pub const APP_ID: &str = "Missive.Desktop";

pub const REPLY_SEPARATOR: char = '\u{1f}';
#[cfg_attr(not(windows), allow(dead_code))]
const REPLY_INPUT: &str = "reply";

pub struct Notification {
    pub title: String,
    pub body: String,
    pub thread_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ToastAction {
    Open(String),
    Read(String),
    Reply { thread_id: String, text: String },
}

impl ToastAction {
    pub fn parse(raw: &str) -> Option<Self> {
        if let Some(rest) = raw.strip_prefix("reply:") {
            let (thread_id, text) = rest.split_once(REPLY_SEPARATOR)?;
            let text = text.trim();
            if thread_id.is_empty() || text.is_empty() {
                return None;
            }
            return Some(Self::Reply {
                thread_id: thread_id.to_string(),
                text: text.to_string(),
            });
        }
        if let Some(thread_id) = raw.strip_prefix("read:") {
            return (!thread_id.is_empty()).then(|| Self::Read(thread_id.to_string()));
        }
        let thread_id = raw.strip_prefix("open:").unwrap_or(raw);
        (!thread_id.is_empty()).then(|| Self::Open(thread_id.to_string()))
    }
}

#[cfg_attr(not(windows), allow(dead_code))]
fn activation_message(arguments: &str, reply: Option<String>) -> Option<String> {
    if arguments.starts_with("reply:") {
        let text = reply.unwrap_or_default();
        if text.trim().is_empty() {
            return None;
        }
        return Some(format!("{arguments}{REPLY_SEPARATOR}{text}"));
    }
    (!arguments.is_empty()).then(|| arguments.to_string())
}

#[cfg_attr(not(windows), allow(dead_code))]
fn xml_escape(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for c in text.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&apos;"),
            '\t' | '\n' | '\r' => out.push(c),
            c if (c as u32) < 0x20 || c == '\u{fffe}' || c == '\u{ffff}' => {}
            c => out.push(c),
        }
    }
    out
}

#[cfg_attr(not(windows), allow(dead_code))]
fn toast_xml(notification: &Notification) -> String {
    let id = xml_escape(&notification.thread_id);
    format!(
        concat!(
            "<toast launch=\"open:{id}\">",
            "<visual><binding template=\"ToastGeneric\">",
            "<text>{title}</text><text>{body}</text>",
            "</binding></visual>",
            "<actions>",
            "<input id=\"{input}\" type=\"text\" placeHolderContent=\"Répondre…\"/>",
            "<action content=\"Envoyer\" arguments=\"reply:{id}\" hint-inputId=\"{input}\"/>",
            "<action content=\"Marquer comme lu\" arguments=\"read:{id}\"/>",
            "</actions>",
            "</toast>"
        ),
        id = id,
        input = REPLY_INPUT,
        title = xml_escape(&notification.title),
        body = xml_escape(&truncate(&notification.body, 240)),
    )
}

#[cfg(windows)]
pub fn register() {
    use winreg::{RegKey, enums::HKEY_CURRENT_USER};

    let path = format!(r"Software\Classes\AppUserModelId\{APP_ID}");
    let result = RegKey::predef(HKEY_CURRENT_USER)
        .create_subkey(&path)
        .and_then(|(key, _)| key.set_value("DisplayName", &"Missive"));
    if let Err(err) = result {
        eprintln!("[app] notification registration failed: {err}");
    }
}

#[cfg(not(windows))]
pub fn register() {}

#[cfg(windows)]
pub fn unregister() {
    use winreg::{RegKey, enums::HKEY_CURRENT_USER};
    let path = format!(r"Software\Classes\AppUserModelId\{APP_ID}");
    let _ = RegKey::predef(HKEY_CURRENT_USER).delete_subkey_all(path);
}

#[cfg(not(windows))]
pub fn unregister() {}

#[cfg(windows)]
fn reply_text(args: &windows::UI::Notifications::ToastActivatedEventArgs) -> Option<String> {
    use windows::Foundation::IPropertyValue;
    use windows::core::{HSTRING, Interface};
    let value = args
        .UserInput()
        .ok()?
        .Lookup(&HSTRING::from(REPLY_INPUT))
        .ok()?;
    Some(value.cast::<IPropertyValue>().ok()?.GetString().ok()?.to_string())
}

#[cfg(windows)]
fn show_toast(notification: &Notification, clicks: Sender<String>) -> windows::core::Result<()> {
    use windows::Data::Xml::Dom::XmlDocument;
    use windows::Foundation::TypedEventHandler;
    use windows::UI::Notifications::{
        ToastActivatedEventArgs, ToastNotification, ToastNotificationManager,
    };
    use windows::core::{HSTRING, IInspectable, Interface};

    let document = XmlDocument::new()?;
    document.LoadXml(&HSTRING::from(toast_xml(notification)))?;
    let toast = ToastNotification::CreateToastNotification(&document)?;
    let fallback = format!("open:{}", notification.thread_id);
    toast.Activated(&TypedEventHandler::<ToastNotification, IInspectable>::new(
        move |_, args| {
            let args: &Option<IInspectable> = &args;
            let message = match args
                .as_ref()
                .and_then(|args| args.cast::<ToastActivatedEventArgs>().ok())
            {
                Some(args) => {
                    let arguments = args.Arguments().map(|a| a.to_string()).unwrap_or_default();
                    let arguments = if arguments.is_empty() {
                        fallback.clone()
                    } else {
                        arguments
                    };
                    activation_message(&arguments, reply_text(&args))
                }
                None => Some(fallback.clone()),
            };
            if let Some(message) = message {
                let _ = clicks.send(message);
            }
            Ok(())
        },
    ))?;
    ToastNotificationManager::CreateToastNotifierWithId(&HSTRING::from(APP_ID))?.Show(&toast)
}

#[cfg(windows)]
pub fn show(notification: Notification, clicks: Sender<String>) {
    if let Err(err) = show_toast(&notification, clicks) {
        eprintln!("[app] notification failed: {err}");
    }
}

#[cfg(not(windows))]
pub fn show(notification: Notification, _clicks: Sender<String>) {
    let result = notify_rust::Notification::new()
        .summary(&notification.title)
        .body(&truncate(&notification.body, 240))
        .show();
    if let Err(err) = result {
        eprintln!("[app] notification failed: {err}");
    }
}

fn truncate(text: &str, max_chars: usize) -> String {
    if text.chars().count() <= max_chars {
        return text.to_string();
    }
    let mut out: String = text.chars().take(max_chars).collect();
    out.push('…');
    out
}

#[cfg(test)]
mod tests {
    use super::{Notification, ToastAction, activation_message, toast_xml, xml_escape};

    #[::core::prelude::v1::test]
    fn parses_all_action_formats() {
        assert_eq!(ToastAction::parse("open:123"), Some(ToastAction::Open("123".into())));
        assert_eq!(ToastAction::parse("123"), Some(ToastAction::Open("123".into())));
        assert_eq!(ToastAction::parse("read:42"), Some(ToastAction::Read("42".into())));
        assert_eq!(
            ToastAction::parse("reply:42\u{1f} Bonjour : à demain\n"),
            Some(ToastAction::Reply {
                thread_id: "42".into(),
                text: "Bonjour : à demain".into(),
            })
        );
        assert_eq!(
            ToastAction::parse("reply:42\u{1f}a\u{1f}b"),
            Some(ToastAction::Reply {
                thread_id: "42".into(),
                text: "a\u{1f}b".into(),
            })
        );
    }

    #[::core::prelude::v1::test]
    fn rejects_empty_or_malformed_actions() {
        for raw in ["", "open:", "read:", "reply:42", "reply:42\u{1f}   ", "reply:\u{1f}texte"] {
            assert_eq!(ToastAction::parse(raw), None, "{raw:?}");
        }
    }

    #[::core::prelude::v1::test]
    fn activation_appends_reply_text() {
        assert_eq!(
            activation_message("reply:7", Some("Salut".into())),
            Some("reply:7\u{1f}Salut".into())
        );
        assert_eq!(activation_message("reply:7", Some("  ".into())), None);
        assert_eq!(activation_message("reply:7", None), None);
        assert_eq!(activation_message("read:7", None), Some("read:7".into()));
        assert_eq!(activation_message("open:7", Some("x".into())), Some("open:7".into()));
        let round_trip = activation_message("reply:7", Some("ok".into())).unwrap();
        assert_eq!(
            ToastAction::parse(&round_trip),
            Some(ToastAction::Reply {
                thread_id: "7".into(),
                text: "ok".into(),
            })
        );
    }

    #[::core::prelude::v1::test]
    fn escapes_xml_and_strips_control_characters() {
        assert_eq!(
            xml_escape("<a href=\"x\">Tom & Jerry's</a>\u{7}\u{1f}"),
            "&lt;a href=&quot;x&quot;&gt;Tom &amp; Jerry&apos;s&lt;/a&gt;"
        );
        assert_eq!(xml_escape("ligne\nsuivante"), "ligne\nsuivante");
    }

    #[::core::prelude::v1::test]
    fn toast_carries_reply_and_read_actions() {
        let xml = toast_xml(&Notification {
            title: "Camille & co".into(),
            body: "<salut>".into(),
            thread_id: "99".into(),
        });
        assert!(xml.starts_with("<toast launch=\"open:99\">"));
        assert!(xml.contains("<text>Camille &amp; co</text><text>&lt;salut&gt;</text>"));
        assert!(xml.contains("<input id=\"reply\" type=\"text\" placeHolderContent=\"Répondre…\"/>"));
        assert!(xml.contains("arguments=\"reply:99\" hint-inputId=\"reply\""));
        assert!(xml.contains("<action content=\"Marquer comme lu\" arguments=\"read:99\"/>"));
    }
}
