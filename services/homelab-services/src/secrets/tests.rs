use super::*;
use crate::testutil::{write_master_key, TEST_MASTER_KEY};
use std::path::PathBuf;

const NOW: i64 = 1_800_000_000_000;

struct Rig {
    dir: tempfile::TempDir,
    db: PathBuf,
    key: PathBuf,
}

impl Rig {
    fn new() -> Rig {
        let dir = tempfile::tempdir().unwrap();
        let key = dir.path().join("master.key");
        write_master_key(&key, &TEST_MASTER_KEY);
        Rig {
            db: dir.path().join("secrets.db"),
            key,
            dir,
        }
    }

    fn open(&self) -> Secrets {
        Secrets::open(&self.db, &self.key, None).unwrap()
    }
}

fn put(s: &Secrets, app: &str, env: &str, name: &str, kind: Kind, value: &str) -> i64 {
    s.put(app, env, name, kind, value, "tester", NOW).unwrap()
}

fn value(s: &Secrets, app: &str, env: &str, name: &str) -> Option<String> {
    s.get(app, env, name, None)
        .unwrap()
        .map(|o| o.value.to_string())
}

// ---- basics ----

#[test]
fn stores_and_reads_both_kinds_and_the_platform() {
    let rig = Rig::new();
    let s = rig.open();
    assert_eq!(
        put(&s, "movies", "prod", "TMDB_API_KEY", Kind::Config, "k1"),
        1
    );
    assert_eq!(
        put(&s, "movies", "prod", "google_token:u1", Kind::App, "t1"),
        1
    );
    assert_eq!(put(&s, "", "", "GITHUB_TOKEN", Kind::Config, "ghp_1"), 1);
    assert_eq!(value(&s, "movies", "prod", "TMDB_API_KEY").unwrap(), "k1");
    assert_eq!(
        value(&s, "movies", "prod", "google_token:u1").unwrap(),
        "t1"
    );
    assert_eq!(value(&s, "", "", "GITHUB_TOKEN").unwrap(), "ghp_1");
    // Each place is separate.
    assert_eq!(value(&s, "movies", "dev", "TMDB_API_KEY"), None);
    assert_eq!(value(&s, "notes", "prod", "TMDB_API_KEY"), None);
    assert_eq!(value(&s, "", "", "TMDB_API_KEY"), None);

    let list = s.list("movies", "prod", None).unwrap();
    assert_eq!(list.len(), 2);
    assert_eq!(list[0].name, "TMDB_API_KEY");
    assert_eq!(list[0].kind, Kind::Config);
    assert_eq!(list[1].kind, Kind::App);
    assert_eq!(s.list("movies", "prod", Some(Kind::App)).unwrap().len(), 1);
    assert_eq!(
        s.list("movies", "prod", Some(Kind::Config)).unwrap().len(),
        1
    );
    assert_eq!(s.list("", "", None).unwrap().len(), 1);
}

#[test]
fn nothing_readable_is_stored_in_the_file() {
    let rig = Rig::new();
    let s = rig.open();
    put(
        &s,
        "movies",
        "prod",
        "TMDB_API_KEY",
        Kind::Config,
        "plain-marker-value-123",
    );
    drop(s);
    for suffix in ["", "-wal"] {
        let mut path = rig.db.clone().into_os_string();
        path.push(suffix);
        if let Ok(bytes) = std::fs::read(&path) {
            assert!(!bytes
                .windows(b"plain-marker-value-123".len())
                .any(|w| w == b"plain-marker-value-123"));
        }
    }
    use std::os::unix::fs::PermissionsExt;
    let mode = std::fs::metadata(&rig.db).unwrap().permissions().mode();
    assert_eq!(mode & 0o077, 0, "the database file must be private");
}

#[test]
fn every_value_gets_its_own_nonce() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "dev", "X", Kind::Config, "same");
    put(&s, "a", "dev", "Y", Kind::Config, "same");
    put(&s, "a", "dev", "X", Kind::Config, "same");
    let conn = s.lock();
    let mut stmt = conn
        .prepare("SELECT nonce, sealed FROM secret_versions")
        .unwrap();
    let rows: Vec<(Vec<u8>, Vec<u8>)> = stmt
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))
        .unwrap()
        .map(|r| r.unwrap())
        .collect();
    assert_eq!(rows.len(), 3);
    for (n, _) in &rows {
        assert_eq!(n.len(), 12);
    }
    for i in 0..rows.len() {
        for j in i + 1..rows.len() {
            assert_ne!(rows[i].0, rows[j].0, "nonce reused");
            assert_ne!(rows[i].1, rows[j].1, "same value sealed the same way");
        }
    }
}

#[test]
fn keeps_five_versions_and_numbers_continue() {
    let rig = Rig::new();
    let s = rig.open();
    for i in 1..=8 {
        assert_eq!(put(&s, "a", "prod", "K", Kind::Config, &format!("v{i}")), i);
    }
    let versions = s.versions("a", "prod", "K").unwrap();
    let numbers: Vec<i64> = versions.iter().map(|v| v.version).collect();
    assert_eq!(numbers, vec![8, 7, 6, 5, 4]);
    assert_eq!(versions[0].set_by, "tester");
    assert_eq!(versions[0].set_at, NOW);
    for n in 4..=8 {
        let o = s.get("a", "prod", "K", Some(n)).unwrap().unwrap();
        assert_eq!(*o.value, format!("v{n}"));
        assert_eq!(o.version, n);
    }
    assert!(s.get("a", "prod", "K", Some(3)).unwrap().is_none());
    assert!(s.get("a", "prod", "K", Some(9)).unwrap().is_none());
    assert_eq!(value(&s, "a", "prod", "K").unwrap(), "v8");
}

#[test]
fn restore_makes_an_old_value_the_newest_version() {
    let rig = Rig::new();
    let s = rig.open();
    for i in 1..=3 {
        put(&s, "a", "prod", "K", Kind::Config, &format!("v{i}"));
    }
    assert_eq!(s.restore("a", "prod", "K", 1, "owner", NOW + 5).unwrap(), 4);
    assert_eq!(value(&s, "a", "prod", "K").unwrap(), "v1");
    let info = s.info("a", "prod", "K").unwrap().unwrap();
    assert_eq!(
        (info.version, info.updated_by.as_str(), info.updated_at),
        (4, "owner", NOW + 5)
    );
    assert!(matches!(
        s.restore("a", "prod", "K", 99, "owner", NOW),
        Err(Error::Missing(_))
    ));
    assert!(matches!(
        s.restore("a", "prod", "NOPE", 1, "owner", NOW),
        Err(Error::Missing(_))
    ));
    // An app secret restores as an app secret.
    put(&s, "a", "prod", "saved", Kind::App, "one");
    put(&s, "a", "prod", "saved", Kind::App, "two");
    assert_eq!(s.restore("a", "prod", "saved", 1, "owner", NOW).unwrap(), 3);
    let o = s.get("a", "prod", "saved", None).unwrap().unwrap();
    assert_eq!((o.kind, o.value.as_str()), (Kind::App, "one"));
}

#[test]
fn delete_removes_versions_and_numbering_starts_again() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "1");
    put(&s, "a", "prod", "K", Kind::Config, "2");
    assert!(s.delete("a", "prod", "K", None).unwrap());
    assert!(!s.delete("a", "prod", "K", None).unwrap());
    assert!(s.versions("a", "prod", "K").unwrap().is_empty());
    assert_eq!(put(&s, "a", "prod", "K", Kind::Config, "3"), 1);
}

#[test]
fn delete_with_a_kind_refuses_the_other_kind() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "CONF", Kind::Config, "x");
    put(&s, "a", "prod", "saved", Kind::App, "y");
    assert!(matches!(
        s.delete("a", "prod", "CONF", Some(Kind::App)),
        Err(Error::Forbidden(_))
    ));
    assert!(value(&s, "a", "prod", "CONF").is_some());
    assert!(s.delete("a", "prod", "saved", Some(Kind::App)).unwrap());
    // Asking to delete a config secret by a name that is an app's leaves the app's alone.
    put(&s, "a", "prod", "again", Kind::App, "z");
    assert!(!s.delete("a", "prod", "again", Some(Kind::Config)).unwrap());
    assert!(value(&s, "a", "prod", "again").is_some());
}

#[test]
fn delete_app_removes_every_environment_and_kind_but_not_others() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "1");
    put(&s, "a", "dev", "K", Kind::Config, "1");
    put(&s, "a", "dev", "s", Kind::App, "1");
    put(&s, "b", "dev", "K", Kind::Config, "1");
    put(&s, "", "", "GITHUB_TOKEN", Kind::Config, "1");
    assert_eq!(s.delete_app("a").unwrap(), 3);
    assert!(s.list("a", "dev", None).unwrap().is_empty());
    assert!(s.versions("a", "dev", "K").unwrap().is_empty());
    assert_eq!(s.list("b", "dev", None).unwrap().len(), 1);
    assert_eq!(s.list("", "", None).unwrap().len(), 1);
    assert!(s.delete_app("").is_err());
}

#[test]
fn deploy_values_and_missing_are_config_only() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "ONE", Kind::Config, "1");
    put(&s, "a", "prod", "saved", Kind::App, "secret-of-the-app");
    let names: Vec<String> = ["ONE", "TWO", "saved"]
        .iter()
        .map(|s| s.to_string())
        .collect();
    let got = s.values_for_deploy("a", "prod", &names).unwrap();
    assert_eq!(got.len(), 1);
    assert_eq!(got["ONE"].as_str(), "1");
    assert_eq!(
        s.missing("a", "prod", &names).unwrap(),
        vec!["TWO", "saved"]
    );
}

// ---- rules ----

#[test]
fn config_names_are_environment_variable_names() {
    for good in ["A", "_x", "TMDB_API_KEY", "a1_B2"] {
        assert!(valid_config_name(good), "{good}");
    }
    for bad in ["", "1A", "A-B", "A.B", "A B", "a:b", "é", "A\n"] {
        assert!(!valid_config_name(bad), "{bad:?}");
    }
    let rig = Rig::new();
    let s = rig.open();
    for bad in ["1A", "A-B", "", "a:b"] {
        assert!(
            matches!(
                s.put("a", "prod", bad, Kind::Config, "v", "t", NOW),
                Err(Error::Invalid(_))
            ),
            "{bad}"
        );
    }
}

#[test]
fn app_names_follow_their_own_rules() {
    for good in ["a", "google_token:user42", "a.b-c_d", "1", &"x".repeat(200)] {
        assert!(valid_app_name(good), "{good}");
    }
    for bad in [
        "",
        ".",
        "..",
        "a b",
        "a/b",
        "a?b",
        "é",
        "a\n",
        &"x".repeat(201),
    ] {
        assert!(!valid_app_name(bad), "{bad:?}");
    }
    let rig = Rig::new();
    let s = rig.open();
    for bad in ["", "a b", "a/b", ".."] {
        assert!(
            matches!(
                s.put("a", "prod", bad, Kind::App, "v", "t", NOW),
                Err(Error::Invalid(_))
            ),
            "{bad}"
        );
    }
    put(&s, "a", "prod", &"x".repeat(200), Kind::App, "v");
}

#[test]
fn value_limits() {
    let rig = Rig::new();
    let s = rig.open();
    let c = |v: &str| s.put("a", "prod", "K", Kind::Config, v, "t", NOW);
    assert!(c(&"x".repeat(MAX_CONFIG_VALUE)).is_ok());
    assert!(matches!(
        c(&"x".repeat(MAX_CONFIG_VALUE + 1)),
        Err(Error::Invalid(_))
    ));
    assert!(matches!(c(""), Err(Error::Invalid(_))));
    assert!(matches!(c("a\nb"), Err(Error::Invalid(_))));
    assert!(matches!(c("a\rb"), Err(Error::Invalid(_))));
    assert!(matches!(c("a\0b"), Err(Error::Invalid(_))));
    let a = |v: &str| s.put("a", "prod", "saved", Kind::App, v, "t", NOW);
    let big = "y".repeat(MAX_APP_VALUE);
    assert!(a(&big).is_ok());
    assert_eq!(&*value_of(&s, "a", "prod", "saved"), &big);
    assert!(matches!(
        a(&"y".repeat(MAX_APP_VALUE + 1)),
        Err(Error::Invalid(_))
    ));
    assert!(matches!(a(""), Err(Error::Invalid(_))));
    // An app secret may hold line breaks (a certificate, a JSON file).
    assert!(a("line one\nline two").is_ok());
}

fn value_of(s: &Secrets, app: &str, env: &str, name: &str) -> Zeroizing<String> {
    s.get(app, env, name, None).unwrap().unwrap().value
}

#[test]
fn places_are_checked() {
    let rig = Rig::new();
    let s = rig.open();
    let p = |app: &str, env: &str, kind: Kind| s.put(app, env, "K", kind, "v", "t", NOW);
    assert!(matches!(
        p("a", "staging", Kind::Config),
        Err(Error::Invalid(_))
    ));
    assert!(matches!(p("a", "", Kind::Config), Err(Error::Invalid(_))));
    assert!(matches!(
        p("", "prod", Kind::Config),
        Err(Error::Invalid(_))
    ));
    assert!(matches!(
        p("A", "prod", Kind::Config),
        Err(Error::Invalid(_))
    ));
    assert!(matches!(
        p("a\0b", "prod", Kind::Config),
        Err(Error::Invalid(_))
    ));
    // Platform secrets are config secrets.
    assert!(matches!(p("", "", Kind::App), Err(Error::Invalid(_))));
    assert!(p("", "", Kind::Config).is_ok());
    assert!(matches!(s.list("a", "x", None), Err(Error::Invalid(_))));
}

#[test]
fn a_name_is_one_kind_in_one_place() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "SHARED", Kind::Config, "owner");
    // An app secret can't take a config secret's name.
    assert!(matches!(
        s.put("a", "prod", "SHARED", Kind::App, "app", "t", NOW),
        Err(Error::Forbidden(_))
    ));
    assert_eq!(value(&s, "a", "prod", "SHARED").unwrap(), "owner");
    // And the owner can't take an app secret's name.
    put(&s, "a", "prod", "saved", Kind::App, "app");
    assert!(matches!(
        s.put("a", "prod", "saved", Kind::Config, "owner", "t", NOW),
        Err(Error::Conflict(_))
    ));
    assert_eq!(value(&s, "a", "prod", "saved").unwrap(), "app");
    // The same name in another environment or app is free.
    put(&s, "a", "dev", "SHARED", Kind::App, "x");
    put(&s, "b", "prod", "SHARED", Kind::App, "x");
    // After a delete the name is free for the other kind.
    s.delete("a", "prod", "saved", None).unwrap();
    put(&s, "a", "prod", "saved", Kind::Config, "owner");
}

#[test]
fn an_app_environment_holds_at_most_ten_thousand_app_secrets() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "first", Kind::App, "v");
    {
        // Fill the rest straight into the table: sealing ten thousand values one at a
        // time would only slow the test down.
        let mut conn = s.lock();
        let tx = conn.transaction().unwrap();
        for i in 1..MAX_APP_SECRETS {
            tx.execute(
                "INSERT INTO secrets (app, env, name, kind, version, updated_at, updated_by)
                 VALUES ('a', 'prod', ?, 'app', 1, 0, 'fill')",
                [format!("n{i}")],
            )
            .unwrap();
        }
        tx.commit().unwrap();
    }
    assert_eq!(
        s.list("a", "prod", Some(Kind::App)).unwrap().len() as i64,
        MAX_APP_SECRETS
    );
    assert!(matches!(
        s.put("a", "prod", "one-too-many", Kind::App, "v", "t", NOW),
        Err(Error::Conflict(_))
    ));
    // Changing one that exists still works, other environments have their own count, and
    // config secrets are not counted.
    assert_eq!(put(&s, "a", "prod", "first", Kind::App, "v2"), 2);
    put(&s, "a", "dev", "one", Kind::App, "v");
    put(&s, "a", "prod", "CONFIG", Kind::Config, "v");
    // Room appears when one is deleted.
    s.delete("a", "prod", "n1", None).unwrap();
    put(&s, "a", "prod", "one-too-many", Kind::App, "v");
}

// ---- a sealed value opens only where it was sealed ----

fn sealed_row(s: &Secrets, app: &str, env: &str, name: &str, version: i64) -> (Vec<u8>, Vec<u8>) {
    s.lock()
        .query_row(
            "SELECT nonce, sealed FROM secret_versions WHERE app=? AND env=? AND name=? AND version=?",
            params![app, env, name, version],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .unwrap()
}

fn put_sealed(
    s: &Secrets,
    app: &str,
    env: &str,
    name: &str,
    version: i64,
    row: &(Vec<u8>, Vec<u8>),
) {
    s.lock()
        .execute(
            "UPDATE secret_versions SET nonce=?, sealed=? WHERE app=? AND env=? AND name=? AND version=?",
            params![row.0, row.1, app, env, name, version],
        )
        .unwrap();
}

#[test]
fn a_sealed_value_moved_to_another_name_does_not_open() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "ONE", Kind::Config, "value-one");
    put(&s, "a", "prod", "TWO", Kind::Config, "value-two");
    let one = sealed_row(&s, "a", "prod", "ONE", 1);
    put_sealed(&s, "a", "prod", "TWO", 1, &one);
    assert_eq!(s.get("a", "prod", "TWO", None).err(), Some(Error::Seal));
    assert_eq!(value(&s, "a", "prod", "ONE").unwrap(), "value-one");
}

#[test]
fn a_sealed_value_moved_to_another_app_or_env_does_not_open() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "from-a-prod");
    put(&s, "b", "prod", "K", Kind::Config, "from-b-prod");
    put(&s, "a", "dev", "K", Kind::Config, "from-a-dev");
    let a_prod = sealed_row(&s, "a", "prod", "K", 1);
    put_sealed(&s, "b", "prod", "K", 1, &a_prod);
    put_sealed(&s, "a", "dev", "K", 1, &a_prod);
    assert_eq!(s.get("b", "prod", "K", None).err(), Some(Error::Seal));
    assert_eq!(s.get("a", "dev", "K", None).err(), Some(Error::Seal));
}

#[test]
fn a_sealed_value_does_not_open_as_another_kind() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "was-config");
    s.lock()
        .execute("UPDATE secrets SET kind = 'app' WHERE name = 'K'", [])
        .unwrap();
    assert_eq!(s.get("a", "prod", "K", None).err(), Some(Error::Seal));
    // And the other way round.
    put(&s, "a", "prod", "saved", Kind::App, "was-app");
    s.lock()
        .execute(
            "UPDATE secrets SET kind = 'config' WHERE name = 'saved'",
            [],
        )
        .unwrap();
    assert_eq!(s.get("a", "prod", "saved", None).err(), Some(Error::Seal));
}

#[test]
fn a_sealed_value_does_not_open_as_another_version() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "first");
    put(&s, "a", "prod", "K", Kind::Config, "second");
    // Version 1's row presented as version 2, and the other way round.
    let v1 = sealed_row(&s, "a", "prod", "K", 1);
    put_sealed(&s, "a", "prod", "K", 2, &v1);
    assert_eq!(s.get("a", "prod", "K", None).err(), Some(Error::Seal));
    // The old row, renumbered.
    s.lock()
        .execute(
            "UPDATE secret_versions SET version = 9 WHERE name = 'K' AND version = 1",
            [],
        )
        .unwrap();
    assert_eq!(s.get("a", "prod", "K", Some(9)).err(), Some(Error::Seal));
}

#[test]
fn a_changed_sealed_value_or_nonce_does_not_open() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "first");
    let (nonce, mut sealed) = sealed_row(&s, "a", "prod", "K", 1);
    sealed[0] ^= 1;
    put_sealed(&s, "a", "prod", "K", 1, &(nonce.clone(), sealed.clone()));
    assert_eq!(s.get("a", "prod", "K", None).err(), Some(Error::Seal));
    sealed[0] ^= 1;
    let mut other = nonce;
    other[0] ^= 1;
    put_sealed(&s, "a", "prod", "K", 1, &(other, sealed));
    assert_eq!(s.get("a", "prod", "K", None).err(), Some(Error::Seal));
}

#[test]
fn the_sealing_format_is_exactly_what_the_docs_say() {
    assert_eq!(
        aad("movies", "prod", "TMDB_API_KEY", Kind::Config, 3),
        b"homelab-secret-v2\0movies\0prod\0TMDB_API_KEY\0config\x003".to_vec()
    );
    assert_eq!(
        aad("", "", "GITHUB_TOKEN", Kind::Config, 12),
        b"homelab-secret-v2\0\0\0GITHUB_TOKEN\0config\x0012".to_vec()
    );
    assert_eq!(
        aad("a", "dev", "n", Kind::App, 1),
        b"homelab-secret-v2\0a\0dev\0n\0app\x001".to_vec()
    );
    // Go's formats, copied from secretstore.go.
    assert_eq!(
        go_aad("movies", "prod", "K"),
        b"homelab-secret-v1\0movies\0prod\0K".to_vec()
    );
    assert_eq!(
        go_version_aad("movies", "prod", "K", 4),
        b"homelab-secret-version-v1\0movies\0prod\0K\x004".to_vec()
    );
}

// ---- the master key ----

#[test]
fn the_service_refuses_to_start_without_a_good_key() {
    let rig = Rig::new();
    let open = |key: &Path| Secrets::open(&rig.db, key, None).err();

    let missing = rig.dir.path().join("nope.key");
    assert!(open(&missing).unwrap().contains("missing"));

    use std::os::unix::fs::PermissionsExt;
    let set_mode =
        |p: &Path, m: u32| std::fs::set_permissions(p, std::fs::Permissions::from_mode(m)).unwrap();

    let short = rig.dir.path().join("short.key");
    for (text, what) in [
        (STANDARD.encode([1u8; 31]), "31 bytes"),
        (STANDARD.encode([1u8; 33]), "33 bytes"),
        ("not base64 !!".to_string(), "not base64"),
        (String::new(), "empty"),
        (STANDARD.encode([1u8; 32]).repeat(200), "huge"),
    ] {
        std::fs::write(&short, text).unwrap();
        set_mode(&short, 0o600);
        let err = open(&short).unwrap_or_else(|| panic!("{what} was accepted"));
        assert!(err.contains("isn't a valid key"), "{what}: {err}");
    }

    // Readable by others.
    for mode in [0o644, 0o640, 0o604, 0o660] {
        set_mode(&rig.key, mode);
        let err = open(&rig.key).unwrap_or_else(|| panic!("{mode:o} was accepted"));
        assert!(err.contains("chmod 600"), "{err}");
    }
    set_mode(&rig.key, 0o400);
    assert!(open(&rig.key).is_none(), "a read-only private key is fine");

    // A folder is not a key.
    assert!(open(rig.dir.path()).unwrap().contains("isn't a file"));
}

#[test]
fn the_key_file_text_homelabd_writes_is_accepted() {
    // homelabd writes base64 and a new line.
    let rig = Rig::new();
    let text = std::fs::read_to_string(&rig.key).unwrap();
    assert!(text.ends_with('\n'));
    assert!(Secrets::open(&rig.db, &rig.key, None).is_ok());
}

#[test]
fn a_different_key_does_not_open_what_is_stored() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "v");
    drop(s);
    write_master_key(&rig.key, &[4u8; 32]);
    let err = Secrets::open(&rig.db, &rig.key, None).err().unwrap();
    assert!(err.contains("doesn't open the secrets"), "{err}");
    // An empty store has nothing to prove, and any key works.
    let fresh = Rig::new();
    write_master_key(&fresh.key, &[4u8; 32]);
    assert!(fresh.open().list("a", "prod", None).unwrap().is_empty());
}

#[test]
fn values_survive_closing_and_opening() {
    let rig = Rig::new();
    let s = rig.open();
    put(&s, "a", "prod", "K", Kind::Config, "v1");
    put(&s, "a", "prod", "K", Kind::Config, "v2");
    put(&s, "a", "prod", "saved", Kind::App, "s1");
    drop(s);
    let s = rig.open();
    assert!(s.imported.is_none());
    assert_eq!(value(&s, "a", "prod", "K").unwrap(), "v2");
    assert_eq!(s.versions("a", "prod", "K").unwrap().len(), 2);
    assert_eq!(value(&s, "a", "prod", "saved").unwrap(), "s1");
}

// ---- the one-time import from homelabd's Go store ----

/// The Go fixture: made by the real Go store (see internal/adapters/secretstore/testdata),
/// with a known key. See also TestRustImportFixtureOpens in the Go tests.
fn fixture_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/go-secrets")
}

/// A copy of the fixture where the import can open it (the key file private).
struct GoCopy {
    dir: tempfile::TempDir,
    db: PathBuf,
    key: PathBuf,
}

fn go_copy() -> GoCopy {
    let dir = tempfile::tempdir().unwrap();
    let db = dir.path().join("secrets.db");
    let key = dir.path().join("master.key");
    std::fs::copy(fixture_dir().join("secrets.sqlite"), &db).unwrap();
    std::fs::copy(fixture_dir().join("master.key"), &key).unwrap();
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(&key, std::fs::Permissions::from_mode(0o600)).unwrap();
    GoCopy { dir, db, key }
}

fn sha(path: &Path) -> Vec<u8> {
    use sha2::{Digest, Sha256};
    Sha256::digest(std::fs::read(path).unwrap()).to_vec()
}

#[test]
fn imports_a_store_made_by_the_go_code() {
    let go = go_copy();
    let new_dir = tempfile::tempdir().unwrap();
    let before = sha(&go.db);
    let s = Secrets::open(&new_dir.path().join("secrets.db"), &go.key, Some(&go.db)).unwrap();
    let done = s.imported.as_ref().expect("it imported");
    assert_eq!(
        *done,
        Imported {
            secrets: 5,
            versions: 9
        }
    );

    // Current values.
    assert_eq!(
        value(&s, "", "", "GITHUB_TOKEN").unwrap(),
        "ghp_fixture_token"
    );
    assert_eq!(
        value(&s, "movies", "prod", "TMDB_API_KEY").unwrap(),
        "tmdb-v7"
    );
    assert_eq!(
        value(&s, "movies", "dev", "TMDB_API_KEY").unwrap(),
        "tmdb-dev"
    );
    assert_eq!(
        value(&s, "movies", "prod", "HOMELAB_TOKEN").unwrap(),
        "hlk_fixture"
    );
    assert_eq!(
        value(&s, "notes", "prod", "DB_URL").unwrap(),
        "postgres://u:p@h/db?x=1&y=ü"
    );
    // The kept versions and their numbers.
    let numbers: Vec<i64> = s
        .versions("movies", "prod", "TMDB_API_KEY")
        .unwrap()
        .iter()
        .map(|v| v.version)
        .collect();
    assert_eq!(numbers, vec![7, 6, 5, 4, 3]);
    for n in 3..=7 {
        assert_eq!(
            *s.get("movies", "prod", "TMDB_API_KEY", Some(n))
                .unwrap()
                .unwrap()
                .value,
            format!("tmdb-v{n}")
        );
    }
    let v = s.versions("movies", "prod", "TMDB_API_KEY").unwrap();
    assert_eq!(v[0].set_by, "fixture");
    assert!(v[0].set_at > 0);
    // Everything came in as config.
    for i in s.list("movies", "prod", None).unwrap() {
        assert_eq!(i.kind, Kind::Config);
    }
    let info = s.info("movies", "prod", "TMDB_API_KEY").unwrap().unwrap();
    assert_eq!((info.version, info.updated_by.as_str()), (7, "fixture"));
    // Imported values are sealed with the new format and open only in their place.
    let one = sealed_row(&s, "movies", "dev", "TMDB_API_KEY", 1);
    put_sealed(&s, "movies", "prod", "HOMELAB_TOKEN", 1, &one);
    assert_eq!(
        s.get("movies", "prod", "HOMELAB_TOKEN", None).err(),
        Some(Error::Seal)
    );

    // The Go file is only read.
    assert_eq!(sha(&go.db), before, "the Go file was changed");
    assert!(go.db.exists());
    // New writes carry on from the imported version numbers.
    assert_eq!(
        put(
            &s,
            "movies",
            "prod",
            "TMDB_API_KEY",
            Kind::Config,
            "tmdb-v8"
        ),
        8
    );
    let _ = &go.dir;
}

#[test]
fn the_import_runs_once_and_a_second_start_changes_nothing() {
    let go = go_copy();
    let new_dir = tempfile::tempdir().unwrap();
    let db = new_dir.path().join("secrets.db");
    let s = Secrets::open(&db, &go.key, Some(&go.db)).unwrap();
    assert!(s.imported.is_some());
    // The owner changes a value and deletes another after the import.
    assert_eq!(
        put(
            &s,
            "movies",
            "prod",
            "TMDB_API_KEY",
            Kind::Config,
            "changed"
        ),
        8
    );
    s.delete("notes", "prod", "DB_URL", None).unwrap();
    put(&s, "movies", "prod", "saved", Kind::App, "app-made");
    drop(s);

    for _ in 0..2 {
        let s = Secrets::open(&db, &go.key, Some(&go.db)).unwrap();
        assert!(s.imported.is_none(), "imported again");
        assert_eq!(
            value(&s, "movies", "prod", "TMDB_API_KEY").unwrap(),
            "changed"
        );
        assert_eq!(
            value(&s, "notes", "prod", "DB_URL"),
            None,
            "a deleted secret came back"
        );
        assert_eq!(value(&s, "movies", "prod", "saved").unwrap(), "app-made");
        assert_eq!(s.list("movies", "prod", None).unwrap().len(), 3);
    }
}

#[test]
fn a_failed_import_changes_nothing_and_is_tried_again() {
    let go = go_copy();
    let new_dir = tempfile::tempdir().unwrap();
    let db = new_dir.path().join("secrets.db");
    // The wrong key can't open the old values.
    let wrong = new_dir.path().join("wrong.key");
    write_master_key(&wrong, &[9u8; 32]);
    let err = Secrets::open(&db, &wrong, Some(&go.db)).err().unwrap();
    assert!(err.contains("importing the old secrets"), "{err}");
    assert!(err.contains("master key"), "{err}");
    {
        let conn = Connection::open(&db).unwrap();
        let n: i64 = conn
            .query_row("SELECT (SELECT COUNT(*) FROM secrets) + (SELECT COUNT(*) FROM secret_versions) + (SELECT COUNT(*) FROM meta)", [], |r| r.get(0))
            .unwrap();
        assert_eq!(n, 0, "a failed import left something behind");
    }
    // With the right key it works.
    let s = Secrets::open(&db, &go.key, Some(&go.db)).unwrap();
    assert_eq!(s.imported.as_ref().unwrap().secrets, 5);
}

#[test]
fn one_bad_row_stops_the_whole_import() {
    let go = go_copy();
    // Damage one kept version in the copy.
    {
        let conn = Connection::open(&go.db).unwrap();
        conn.execute(
            "UPDATE secret_versions SET sealed = X'00112233445566778899AABBCCDDEEFF' WHERE name = 'TMDB_API_KEY' AND env = 'prod' AND version = 4",
            [],
        )
        .unwrap();
    }
    let new_dir = tempfile::tempdir().unwrap();
    let db = new_dir.path().join("secrets.db");
    assert!(Secrets::open(&db, &go.key, Some(&go.db)).is_err());
    let conn = Connection::open(&db).unwrap();
    let n: i64 = conn
        .query_row("SELECT COUNT(*) FROM secret_versions", [], |r| r.get(0))
        .unwrap();
    assert_eq!(n, 0);
}

#[test]
fn a_go_value_moved_to_another_name_is_not_imported() {
    // Go bound values to their place too; the import must not paper over a swap.
    let go = go_copy();
    {
        let conn = Connection::open(&go.db).unwrap();
        conn.execute(
            "UPDATE secrets SET name = 'OTHER' WHERE name = 'DB_URL'",
            [],
        )
        .unwrap();
    }
    let new_dir = tempfile::tempdir().unwrap();
    let err = Secrets::open(&new_dir.path().join("secrets.db"), &go.key, Some(&go.db))
        .err()
        .unwrap();
    assert!(err.contains("importing the old secrets"), "{err}");
}

#[test]
fn no_old_file_means_nothing_to_import_and_a_late_file_is_ignored() {
    let go = go_copy();
    let new_dir = tempfile::tempdir().unwrap();
    let db = new_dir.path().join("secrets.db");
    let absent = new_dir.path().join("absent.db");
    let s = Secrets::open(&db, &go.key, Some(&absent)).unwrap();
    assert_eq!(
        s.imported,
        Some(Imported {
            secrets: 0,
            versions: 0
        })
    );
    drop(s);
    // The old file shows up later: it is not imported by surprise.
    let s = Secrets::open(&db, &go.key, Some(&go.db)).unwrap();
    assert!(s.imported.is_none());
    assert!(s.list("movies", "prod", None).unwrap().is_empty());
    // And with no import path at all.
    let other = tempfile::tempdir().unwrap();
    let s = Secrets::open(&other.path().join("secrets.db"), &go.key, None).unwrap();
    assert!(s.list("", "", None).unwrap().is_empty());
}

#[test]
fn imports_a_go_store_that_has_no_versions_table_yet() {
    // A store from before versions existed: no version column, no secret_versions table.
    let go = go_copy();
    let old_dir = tempfile::tempdir().unwrap();
    let old = old_dir.path().join("secrets.db");
    {
        let src = Connection::open(&go.db).unwrap();
        let dst = Connection::open(&old).unwrap();
        dst.execute_batch(
            "CREATE TABLE secrets (app TEXT NOT NULL, env TEXT NOT NULL, name TEXT NOT NULL,
             nonce BLOB NOT NULL, sealed BLOB NOT NULL, updated_at INTEGER NOT NULL,
             updated_by TEXT NOT NULL, PRIMARY KEY (app, env, name))",
        )
        .unwrap();
        type OldRow = (String, String, String, Vec<u8>, Vec<u8>, i64, String);
        let rows: Vec<OldRow> = src
            .prepare("SELECT app, env, name, nonce, sealed, updated_at, updated_by FROM secrets WHERE name = 'DB_URL'")
            .unwrap()
            .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?, r.get(5)?, r.get(6)?)))
            .unwrap()
            .map(|r| r.unwrap())
            .collect();
        for r in rows {
            dst.execute(
                "INSERT INTO secrets VALUES (?,?,?,?,?,?,?)",
                params![r.0, r.1, r.2, r.3, r.4, r.5, r.6],
            )
            .unwrap();
        }
    }
    let new_dir = tempfile::tempdir().unwrap();
    let s = Secrets::open(&new_dir.path().join("secrets.db"), &go.key, Some(&old)).unwrap();
    assert_eq!(
        s.imported,
        Some(Imported {
            secrets: 1,
            versions: 1
        })
    );
    let o = s.get("notes", "prod", "DB_URL", None).unwrap().unwrap();
    assert_eq!(
        (o.version, o.value.as_str()),
        (1, "postgres://u:p@h/db?x=1&y=ü")
    );
}

#[test]
fn the_import_needs_a_good_key_before_it_starts() {
    let go = go_copy();
    let new_dir = tempfile::tempdir().unwrap();
    let db = new_dir.path().join("secrets.db");
    let err = Secrets::open(&db, &new_dir.path().join("none.key"), Some(&go.db))
        .err()
        .unwrap();
    assert!(err.contains("missing"), "{err}");
}
