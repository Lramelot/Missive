use velopack::{
    UpdateCheck, UpdateInfo, UpdateManager,
    sources::{AutoSource, GithubSource},
};

pub const FEED_URL: Option<&str> = option_env!("MISSIVE_UPDATE_URL");

#[derive(Clone)]
pub enum UpdateState {
    Downloading {
        version: String,
    },
    Ready {
        version: String,
        info: Box<UpdateInfo>,
    },
}

fn manager() -> Option<UpdateManager> {
    let url = FEED_URL?;
    if url.starts_with("https://github.com/") {
        UpdateManager::new(GithubSource::new(url, None, false), None, None).ok()
    } else {
        UpdateManager::new(AutoSource::new(url), None, None).ok()
    }
}

pub fn is_managed() -> bool {
    manager().is_some()
}

pub fn current_version() -> String {
    manager()
        .map(|manager| manager.get_current_version_as_string())
        .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_string())
}

pub fn pending_restart() -> Option<String> {
    manager()?
        .get_update_pending_restart()
        .map(|asset| asset.Version)
}

pub fn check() -> anyhow::Result<Option<UpdateInfo>> {
    let Some(manager) = manager() else {
        return Ok(None);
    };
    match manager.check_for_updates()? {
        UpdateCheck::UpdateAvailable(info) => Ok(Some(*info)),
        UpdateCheck::NoUpdateAvailable | UpdateCheck::RemoteIsEmpty => Ok(None),
    }
}

pub fn download(info: &UpdateInfo) -> anyhow::Result<()> {
    let manager = manager().ok_or_else(|| anyhow::anyhow!("application not installed"))?;
    manager.download_updates(info, None)?;
    Ok(())
}

pub fn apply_and_restart(info: &UpdateInfo) -> anyhow::Result<()> {
    let manager = manager().ok_or_else(|| anyhow::anyhow!("application not installed"))?;
    manager.apply_updates_and_restart(info)?;
    Ok(())
}
