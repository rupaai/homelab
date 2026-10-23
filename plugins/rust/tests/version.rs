//! Alone in its own test binary because it changes the process environment.
use homelab::Homelab;

#[test]
fn version_is_homelab_version_or_none() {
    std::env::remove_var("HOMELAB_VERSION");
    assert_eq!(homelab::version(), None);
    std::env::set_var("HOMELAB_VERSION", "  ");
    assert_eq!(homelab::version(), None);
    std::env::set_var("HOMELAB_VERSION", "v0.4.0-dev.2");
    assert_eq!(homelab::version().as_deref(), Some("v0.4.0-dev.2"));
    let h = Homelab::new("http://127.0.0.1:7085", "hlk_x").unwrap();
    assert_eq!(h.version().as_deref(), Some("v0.4.0-dev.2"));
}
