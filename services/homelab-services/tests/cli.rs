//! Runs the real binary to check the command line and start-up errors.

use std::process::Command;

fn bin() -> Command {
    let mut cmd = Command::new(env!("CARGO_BIN_EXE_homelab-services"));
    // Start from a clean environment so the host's settings never leak in.
    cmd.env_remove("HOMELAB_SERVICES_LISTEN")
        .env_remove("HOMELAB_SERVICES_DATA")
        .env_remove("HOMELAB_SERVICES_KEY")
        .env_remove("HOMELAB_SERVICES_STORAGE")
        .env_remove("HOMELAB_SERVICES_MAX_UPLOAD")
        .env_remove("HOMELAB_SERVICES_SECRETS_KEY")
        .env_remove("HOMELAB_SERVICES_SECRETS_IMPORT");
    cmd
}

#[test]
fn version_prints_and_exits_zero() {
    let out = bin().arg("version").output().unwrap();
    assert!(out.status.success());
    let text = String::from_utf8(out.stdout).unwrap();
    assert_eq!(
        text,
        format!("homelab-services {}\n", homelab_services::VERSION)
    );
}

#[test]
fn unknown_argument_exits_two_with_usage() {
    let out = bin().arg("bogus").output().unwrap();
    assert_eq!(out.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&out.stderr).contains("usage:"));
}

#[test]
fn missing_settings_exit_one_with_a_message() {
    let out = bin().output().unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("HOMELAB_SERVICES_LISTEN"));
}

#[test]
fn non_loopback_listen_is_refused() {
    let out = bin()
        .env("HOMELAB_SERVICES_LISTEN", "0.0.0.0:9000")
        .env(
            "HOMELAB_SERVICES_DATA",
            std::env::temp_dir().join("hs-cli-unused"),
        )
        .env("HOMELAB_SERVICES_KEY", "AAAA")
        .env(
            "HOMELAB_SERVICES_STORAGE",
            r#"[{"name":"main","path":"/mnt/storage"}]"#,
        )
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("loopback"));
}

#[test]
fn missing_or_bad_storage_is_refused() {
    let base = || {
        let mut cmd = bin();
        cmd.env("HOMELAB_SERVICES_LISTEN", "127.0.0.1:0")
            .env(
                "HOMELAB_SERVICES_DATA",
                std::env::temp_dir().join("hs-cli-unused"),
            )
            .env("HOMELAB_SERVICES_KEY", "AAAA");
        cmd
    };
    let out = base().output().unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("HOMELAB_SERVICES_STORAGE"));

    let out = base()
        .env("HOMELAB_SERVICES_STORAGE", "[]")
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("HOMELAB_SERVICES_STORAGE"));
}
