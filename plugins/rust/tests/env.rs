//! Alone in its own test binary because it changes the process environment.
use homelab::{Error, Homelab};

#[test]
fn from_env_reads_vars_and_reports_what_is_missing() {
    std::env::remove_var("HOMELAB_API_URL");
    std::env::remove_var("HOMELAB_TOKEN");
    match Homelab::from_env() {
        Err(Error::Config(m)) => assert!(m.contains("Set HOMELAB_API_URL or pass url"), "{m}"),
        other => panic!("{other:?}"),
    }
    std::env::set_var("HOMELAB_API_URL", "http://127.0.0.1:7085/");
    match Homelab::from_env() {
        Err(Error::Config(m)) => assert!(m.contains("HOMELAB_TOKEN"), "{m}"),
        other => panic!("{other:?}"),
    }
    std::env::set_var("HOMELAB_TOKEN", "");
    assert!(matches!(Homelab::from_env(), Err(Error::Config(_))));
    std::env::set_var("HOMELAB_TOKEN", "hlk_x");
    let h = Homelab::from_env().unwrap();
    assert!(!format!("{h:?}").contains("hlk_x"));
}
