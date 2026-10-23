//! homelab-services: started by homelabd as a child process.

use std::process::ExitCode;
use std::sync::Arc;

use homelab_services::app::{app, AppState};
use homelab_services::config::{prepare_data_dir, prepare_storage, Config};
use homelab_services::dbs::Dbs;
use homelab_services::files::{spawn_purge, Files};
use homelab_services::secrets::Secrets;
use homelab_services::VERSION;

const USAGE: &str = "usage: homelab-services [version]\n\
\n\
With no arguments, runs the server. It is configured with:\n\
  HOMELAB_SERVICES_LISTEN  loopback ip:port to listen on\n\
  HOMELAB_SERVICES_DATA    folder for this program's own state\n\
  HOMELAB_SERVICES_KEY     homelabd's public key (base64url, no padding)\n\
  HOMELAB_SERVICES_STORAGE JSON list of disks: [{\"name\":\"main\",\"path\":\"/mnt/storage\"}]\n\
  HOMELAB_SERVICES_MAX_UPLOAD  biggest upload in bytes (optional, default 10 GiB)\n\
  HOMELAB_SERVICES_SECRETS_KEY    path of the master key file for secrets (optional; no secrets without it)\n\
  HOMELAB_SERVICES_SECRETS_IMPORT path of homelabd's old secrets.db, imported once (optional)";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.as_slice() {
        [] => {}
        [arg] if arg == "version" => {
            println!("homelab-services {VERSION}");
            return ExitCode::SUCCESS;
        }
        _ => {
            eprintln!("{USAGE}");
            return ExitCode::from(2);
        }
    }

    // Check the settings before starting anything.
    let config = match Config::from_env().and_then(|c| {
        prepare_data_dir(&c.data_dir)?;
        prepare_storage(&c.storage)?;
        Ok(c)
    }) {
        Ok(config) => config,
        Err(message) => {
            eprintln!("homelab-services: {message}");
            return ExitCode::FAILURE;
        }
    };

    match serve(config) {
        Ok(()) => ExitCode::SUCCESS,
        Err(message) => {
            eprintln!("homelab-services: {message}");
            ExitCode::FAILURE
        }
    }
}

/// Runs the server until it is told to stop.
#[tokio::main]
async fn serve(config: Config) -> Result<(), String> {
    tracing_subscriber::fmt()
        .with_writer(std::io::stderr)
        // homelabd adds the time and passes these lines on to its own log.
        .with_ansi(false)
        .without_time()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .init();

    let listener = tokio::net::TcpListener::bind(config.listen)
        .await
        .map_err(|e| format!("cannot listen on {}: {e}", config.listen))?;
    tracing::info!(listen = %config.listen, version = VERSION, "homelab-services starting");

    let (secrets, secrets_problem) = open_secrets(&config);
    let files = Arc::new(Files::open(
        &config.data_dir,
        config.storage,
        config.max_upload,
    )?);
    spawn_purge(files.clone());
    let dbs = Arc::new(Dbs::open(&config.data_dir.join("dbs.db"))?);
    let state = AppState {
        key: Arc::new(config.key),
        version: VERSION,
        files,
        secrets,
        secrets_problem,
        dbs,
    };
    axum::serve(listener, app(state))
        .with_graceful_shutdown(shutdown_signal())
        .await
        .map_err(|e| format!("server error: {e}"))
}

/// Opens the secrets store. If that is not possible the files service still runs, and the
/// secrets routes answer 503 with the reason.
fn open_secrets(config: &Config) -> (Option<Arc<Secrets>>, String) {
    let Some(key) = &config.secrets_key else {
        tracing::warn!("secrets are off: HOMELAB_SERVICES_SECRETS_KEY is not set");
        return (None, "no master key is set".to_string());
    };
    match Secrets::open(
        &config.data_dir.join("secrets.db"),
        key,
        config.secrets_import.as_deref(),
    ) {
        Ok(store) => {
            if let Some(done) = &store.imported {
                tracing::info!(
                    secrets = done.secrets,
                    versions = done.versions,
                    "imported the old secrets; the old file was left where it was"
                );
            }
            tracing::info!("secrets are ready");
            (Some(Arc::new(store)), String::new())
        }
        Err(why) => {
            tracing::error!("secrets are off: {why}");
            (None, why)
        }
    }
}

/// Finishes when we get Ctrl-C or (on unix) SIGTERM.
async fn shutdown_signal() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };

    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut sigterm) => {
                sigterm.recv().await;
            }
            // If we cannot listen for SIGTERM, rely on Ctrl-C only.
            Err(_) => std::future::pending::<()>().await,
        }
    };
    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {}
        _ = terminate => {}
    }
    tracing::info!("shutting down");
}
