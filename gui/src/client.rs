use reqwest::{blocking::Client, redirect::Policy, Url};
use serde_json::{json, Value};
use std::{io::Read, net::IpAddr, time::Duration};

pub const PROTOCOL: &str = "2025-06-18";
const MAX_RESPONSE: u64 = 2 * 1024 * 1024;

#[derive(Clone)]
pub struct Remote {
    client: Client,
    url: Url,
    token: String,
}

pub fn endpoint(input: &str) -> Result<Url, String> {
    let mut url = Url::parse(input.trim()).map_err(|_| "请输入完整的 HTTPS 地址".to_owned())?;
    let loopback = url.host_str().is_some_and(|h| {
        h.trim_matches(['[', ']'])
            .parse::<IpAddr>()
            .is_ok_and(|ip| ip.is_loopback())
    });
    if url.scheme() != "https" && !(url.scheme() == "http" && loopback) {
        return Err("远程连接必须使用 HTTPS；仅回环 IP 允许 HTTP".into());
    }
    if !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err("地址不能包含用户名、密码、查询参数或片段".into());
    }
    if url.path() == "/" || url.path().is_empty() {
        url.set_path("/mcp");
    }
    if url.path() != "/mcp" && url.path() != "/mcp/" {
        return Err("请输入服务根地址或 /mcp 地址".into());
    }
    Ok(url)
}

impl Remote {
    pub fn new(url: &str, token: &str, timeout: Duration) -> Result<Self, String> {
        if token.trim().len() < 16 {
            return Err("访问令牌至少需要 16 个字符".into());
        }
        let url = endpoint(url)?;
        let mut builder = Client::builder()
            .redirect(Policy::none())
            .connect_timeout(Duration::from_secs(8))
            .timeout(timeout);
        // Local startup probes must never leave the machine via a system proxy.
        if url.scheme() == "http" {
            builder = builder.no_proxy();
        }
        Ok(Self {
            client: builder.build().map_err(|e| e.to_string())?,
            url,
            token: token.trim().into(),
        })
    }

    pub fn rpc(&self, id: u64, method: &str, params: Value) -> Result<Value, String> {
        let response = self
            .client
            .post(self.url.clone())
            .bearer_auth(&self.token)
            .header("MCP-Protocol-Version", PROTOCOL)
            .header("Accept", "application/json, text/event-stream")
            .json(&json!({"jsonrpc":"2.0", "id":id, "method":method, "params":params}))
            .send()
            .map_err(|e| format!("连接失败：{e}"))?;
        let status = response.status();
        if !status.is_success() {
            return Err(match status.as_u16() {
                401 => "认证失败：请检查远程访问令牌".into(),
                403 => "访问被拒绝：请检查服务端 IP 白名单".into(),
                _ => format!("服务返回 HTTP {status}；请检查服务状态"),
            });
        }
        let mut bytes = Vec::new();
        response
            .take(MAX_RESPONSE + 1)
            .read_to_end(&mut bytes)
            .map_err(|e| e.to_string())?;
        if bytes.len() as u64 > MAX_RESPONSE {
            return Err("响应超过 2 MiB，请缩小查询范围".into());
        }
        let body: Value =
            serde_json::from_slice(&bytes).map_err(|e| format!("无效 MCP 响应：{e}"))?;
        if body["jsonrpc"] != "2.0" || body["id"] != json!(id) {
            return Err("MCP 响应 ID 或协议不匹配".into());
        }
        if let Some(error) = body.get("error") {
            return Err(format!("MCP 错误：{error}"));
        }
        body.get("result")
            .cloned()
            .ok_or_else(|| "MCP 响应缺少 result".into())
    }

    pub fn connect(&self) -> Result<String, String> {
        let init = self.rpc(
            rand::random(),
            "initialize",
            json!({
                "protocolVersion":PROTOCOL, "capabilities":{},
                "clientInfo":{"name":"mcp-shell-gui", "version":env!("CARGO_PKG_VERSION")}
            }),
        )?;
        if init["serverInfo"]["name"] != "mcp-shell" {
            return Err("目标不是 mcp-shell 服务".into());
        }
        self.notify("notifications/initialized", json!({}))?;
        self.call(rand::random(), "list_roots", json!({}))
    }

    pub fn notify(&self, method: &str, params: Value) -> Result<(), String> {
        let status = self
            .client
            .post(self.url.clone())
            .timeout(Duration::from_secs(5))
            .bearer_auth(&self.token)
            .header("MCP-Protocol-Version", PROTOCOL)
            .header("Accept", "application/json, text/event-stream")
            .json(&json!({"jsonrpc":"2.0", "method":method, "params":params}))
            .send()
            .map_err(|e| e.to_string())?
            .status();
        if !status.is_success() {
            return Err(format!("通知失败：HTTP {status}"));
        }
        Ok(())
    }

    pub fn call(&self, id: u64, name: &str, args: Value) -> Result<String, String> {
        let result = self.rpc(id, "tools/call", json!({"name":name, "arguments":args}))?;
        let text = result["content"]
            .as_array()
            .ok_or("工具响应缺少 content")?
            .iter()
            .filter_map(|item| item["text"].as_str())
            .collect::<Vec<_>>()
            .join("\n");
        if result["isError"] == true {
            Err(text)
        } else {
            Ok(text)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn enforces_transport_and_credentials() {
        for bad in [
            "http://example.com",
            "http://localhost",
            "ftp://example.com",
            "https://u:p@example.com",
            "https://example.com?token=secret",
            "https://example.com/other",
        ] {
            assert!(endpoint(bad).is_err(), "{bad}");
        }
        assert_eq!(endpoint("https://example.com").unwrap().path(), "/mcp");
        assert!(endpoint("http://127.0.0.1:8765").is_ok());
        assert!(endpoint("http://[::1]:8765/mcp").is_ok());
    }
}
