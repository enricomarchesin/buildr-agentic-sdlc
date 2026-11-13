"""A model proxy for building on a Claude subscription instead of an API key.

    python3 harness/subscription-proxy.py ADDRESS PORT LOG

The Docker sandbox's own proxy keeps api.anthropic.com for its Anthropic credential, which is
an API key: it strips any other Authorization or x-api-key header a sandbox sends there. So a
build cannot carry a subscription's token to Anthropic itself. This proxy runs on the host and
does it instead: a build reaches it as H0 reaches any model proxy, with a local key, and every
request under /v1/ is sent on to Anthropic's API with the subscription's token in place of that
key. The token stays on the host; the sandbox never sees it, nor the local key, which the
sandbox's proxy swaps in from its placeholder.

    HEATCTL_TOKEN_COMMAND   prints the subscription's token, from `claude setup-token`; default
                            reads the macOS Keychain item heatctl-subscription
    HEATCTL_PROXY_KEY       file holding the local key, made on first start; default
                            ~/.config/heatctl/subscription-proxy.key

It answers /health/liveliness, which H0 tries as a control, and refuses everything outside /v1/,
the proxy's admin routes among them. One line per request goes to LOG: the time, the build's
tags, the path, the status, the model, why it stopped and the usage Anthropic reported.
"""
import hmac, http.client, http.server, json, os, secrets, ssl, subprocess, sys, threading, time

UPSTREAM = "api.anthropic.com"
OAUTH_BETA = "oauth-2025-04-20"
DROP = {"host", "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade", "te",
        "trailer", "content-length", "accept-encoding", "authorization", "x-api-key", "anthropic-beta",
        "x-litellm-tags"}

address, port, log_path = sys.argv[1], int(sys.argv[2]), sys.argv[3]
token_command = os.environ.get("HEATCTL_TOKEN_COMMAND",
                               f"/usr/bin/security find-generic-password -a {os.environ['USER']} -s heatctl-subscription -w")
TOKEN = subprocess.run(token_command, shell=True, capture_output=True, text=True, check=True).stdout.strip()
key_path = os.path.expanduser(os.environ.get("HEATCTL_PROXY_KEY", "~/.config/heatctl/subscription-proxy.key"))
if not os.path.exists(key_path):
    fd = os.open(key_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.write(fd, secrets.token_hex(24).encode())
    os.close(fd)
KEY = open(key_path).read().strip()
lock = threading.Lock()
context = ssl.create_default_context()


def usage_of(body, content_type):
    """The model and the usage in a response, streamed or not."""
    model, usage, stop = None, {}, None
    try:
        if "event-stream" in content_type:
            for line in body.decode("utf-8", "replace").splitlines():
                if not line.startswith("data:"):
                    continue
                event = json.loads(line[5:])
                if event.get("type") == "message_start":
                    model = event["message"].get("model")
                    usage.update(event["message"].get("usage") or {})
                elif event.get("type") == "message_delta":
                    usage.update(event.get("usage") or {})
                    stop = (event.get("delta") or {}).get("stop_reason") or stop
        else:
            j = json.loads(body)
            model, usage, stop = j.get("model"), j.get("usage") or {}, j.get("stop_reason")
    except (ValueError, KeyError, AttributeError):
        pass
    return model, usage, stop


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.forward()

    def do_POST(self):
        self.forward()

    def forward(self):
        start = time.time()
        n = int(self.headers.get("content-length") or 0)
        body = self.rfile.read(n) if n else b""
        if self.path.startswith("/health"):
            return self.reply(200, {"status": "ok"})
        given = self.headers.get("x-api-key") or self.headers.get("authorization", "").removeprefix("Bearer ").strip()
        if not hmac.compare_digest(given.encode(), KEY.encode()):
            return self.reply(401, {"error": "wrong key"})
        if not self.path.startswith("/v1/"):
            return self.reply(403, {"error": "only /v1/ is forwarded"})
        headers = {k: v for k, v in self.headers.items() if k.lower() not in DROP}
        betas = [b.strip() for b in (self.headers.get("anthropic-beta") or "").split(",") if b.strip()]
        headers["anthropic-beta"] = ",".join(betas + [OAUTH_BETA] * (OAUTH_BETA not in betas))
        headers["authorization"] = "Bearer " + TOKEN
        headers["content-length"] = str(len(body))
        upstream = http.client.HTTPSConnection(UPSTREAM, timeout=900, context=context)
        try:
            upstream.request(self.command, self.path, body, headers)
            r = upstream.getresponse()
        except OSError as e:
            return self.reply(502, {"error": f"upstream: {e}"})
        self.send_response(r.status)
        for k, v in r.getheaders():
            if k.lower() not in ("transfer-encoding", "connection", "content-length", "keep-alive"):
                self.send_header(k, v)
        self.send_header("transfer-encoding", "chunked")
        self.end_headers()
        seen = []
        try:
            while chunk := r.read1(65536):
                seen.append(chunk)
                self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk))
                self.wfile.flush()
            self.wfile.write(b"0\r\n\r\n")
        except OSError:
            self.close_connection = True
        finally:
            upstream.close()
        model, usage, stop = usage_of(b"".join(seen), r.getheader("content-type") or "")
        line = {"time": round(start, 3), "seconds": round(time.time() - start, 3),
                "tags": self.headers.get("x-litellm-tags"), "method": self.command, "path": self.path,
                "status": r.status, "model": model, "stop_reason": stop,
                "usage": usage}
        with lock, open(log_path, "a") as f:
            f.write(json.dumps(line) + "\n")


server = http.server.ThreadingHTTPServer((address, port), Handler)
server.daemon_threads = True
print(f"subscription proxy on http://{address}:{port}, log {log_path}", flush=True)
server.serve_forever()
