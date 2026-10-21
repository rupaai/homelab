#![allow(dead_code)]
use std::collections::VecDeque;
use std::sync::{Arc, Mutex};

use axum::body::Bytes;
use axum::http::{HeaderMap, Method, StatusCode, Uri};
use axum::Router;

#[derive(Debug, Clone)]
pub struct Recorded {
    pub method: String,
    pub path: String,
    pub query: String,
    pub headers: HeaderMap,
    pub body: Vec<u8>,
}

impl Recorded {
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers.get(name).and_then(|v| v.to_str().ok())
    }
    pub fn json(&self) -> serde_json::Value {
        serde_json::from_slice(&self.body).unwrap()
    }
}

pub struct Canned {
    pub status: u16,
    pub headers: Vec<(&'static str, &'static str)>,
    pub body: Vec<u8>,
}

pub fn json(status: u16, body: &str) -> Canned {
    Canned {
        status,
        headers: vec![("content-type", "application/json")],
        body: body.as_bytes().to_vec(),
    }
}

pub struct Fake {
    pub url: String,
    pub requests: Arc<Mutex<Vec<Recorded>>>,
}

impl Fake {
    pub fn last(&self) -> Recorded {
        self.requests.lock().unwrap().last().cloned().unwrap()
    }
    pub fn all(&self) -> Vec<Recorded> {
        self.requests.lock().unwrap().clone()
    }
}

/// Starts a server on 127.0.0.1:0. Each request gets the next canned answer.
pub async fn fake(responses: Vec<Canned>) -> Fake {
    let requests = Arc::new(Mutex::new(Vec::new()));
    let queue = Arc::new(Mutex::new(VecDeque::from(responses)));
    let rec = requests.clone();
    let app = Router::new().fallback(
        move |method: Method, uri: Uri, headers: HeaderMap, body: Bytes| {
            let rec = rec.clone();
            let queue = queue.clone();
            async move {
                rec.lock().unwrap().push(Recorded {
                    method: method.to_string(),
                    path: uri.path().to_string(),
                    query: uri.query().unwrap_or("").to_string(),
                    headers,
                    body: body.to_vec(),
                });
                let c = queue.lock().unwrap().pop_front().unwrap_or(Canned {
                    status: 500,
                    headers: vec![],
                    body: br#"{"error":"no canned answer"}"#.to_vec(),
                });
                let mut r = axum::response::Response::builder()
                    .status(StatusCode::from_u16(c.status).unwrap());
                for (k, v) in c.headers {
                    r = r.header(k, v);
                }
                r.body(axum::body::Body::from(c.body)).unwrap()
            }
        },
    );
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    Fake {
        url: format!("http://{addr}"),
        requests,
    }
}
