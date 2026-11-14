#!/usr/bin/env python3
"""Small allow-listed Alertmanager receiver for safe deployment restarts."""

import datetime
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen


TOKEN = os.environ["SELF_HEALING_WEBHOOK_TOKEN"].strip()
DISCORD_WEBHOOK = os.environ["DISCORD_WEBHOOK_URL"].strip()
discord_url = urlparse(DISCORD_WEBHOOK)
if (
    discord_url.scheme != "https"
    or discord_url.hostname not in {"discord.com", "discordapp.com"}
    or discord_url.username is not None
    or discord_url.password is not None
    or not discord_url.path.startswith("/api/webhooks/")
):
    raise ValueError("Discord webhook must use HTTPS on discord.com or discordapp.com")
SERVICE_ACCOUNT = "/var/run/secrets/kubernetes.io/serviceaccount"
ALLOWED_ALERTS = {
    "EvaluationServiceHigh5xxRate": ("evaluation-service", "HTTP 5xx rate above 5%"),
}


def restart_deployment(name):
    with open(os.path.join(SERVICE_ACCOUNT, "token"), encoding="utf-8") as token_file:
        bearer = token_file.read().strip()
    with open(os.path.join(SERVICE_ACCOUNT, "ca.crt"), "rb") as ca_file:
        ca_bundle = ca_file.read()

    stamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
    patch = json.dumps({
        "spec": {"template": {"metadata": {
            "annotations": {"observability.togglemaster/restarted-at": stamp}
        }}}
    }).encode()
    request = Request(
        f"https://kubernetes.default.svc/apis/apps/v1/namespaces/toggle-master/deployments/{name}",
        data=patch,
        headers={"Authorization": f"Bearer {bearer}", "Content-Type": "application/merge-patch+json"},
        method="PATCH",
    )
    # urllib's default SSLContext validates against the system CA bundle. Kubernetes
    # service-account CA is supplied by the mounted trust store in the container.
    import ssl
    context = ssl.create_default_context(cadata=ca_bundle.decode())
    with urlopen(request, context=context, timeout=4) as response:
        return response.status


def send_discord(alert_name, service, summary, description, action):
    message = (
        f"**ToggleMaster incident: {alert_name}**\n"
        f"Serviço: `{service}`\n"
        f"Resumo: {summary}\n"
        f"Detalhes: {description}\n"
        f"Self-healing: {action}"
    )[:1900]
    request = Request(
        DISCORD_WEBHOOK,
        data=json.dumps({"content": message}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urlopen(request, timeout=4) as response:
        return response.status


class Handler(BaseHTTPRequestHandler):
    def setup(self):
        super().setup()
        self.request.settimeout(5)

    def do_GET(self):  # noqa: N802
        if self.path != "/healthz":
            self.send_error(404)
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok\n")

    def do_POST(self):  # noqa: N802
        if self.path != "/alert":
            self.send_error(404)
            return
        expected = f"Bearer {TOKEN}"
        if not hmac.compare_digest(self.headers.get("Authorization", ""), expected):
            self.send_error(401)
            return
        try:
            payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            if payload.get("status") != "firing":
                self.send_error(202)
                return
            outcomes = []
            for alert in payload.get("alerts", []):
                labels = alert.get("labels", {})
                alert_name = labels.get("alertname", "")
                allowed = ALLOWED_ALERTS.get(alert_name)
                if not allowed or labels.get("service") != "evaluation-service":
                    continue
                deployment, _ = allowed
                summary = alert.get("annotations", {}).get("summary", alert_name)
                description = alert.get("annotations", {}).get("description", "No details supplied")
                try:
                    restart_deployment(deployment)
                    action = f"rollout restart solicitado para deployment/{deployment}"
                except (HTTPError, URLError, TimeoutError, OSError) as error:
                    action = f"falha ao solicitar restart ({type(error).__name__})"
                try:
                    send_discord(alert_name, deployment, summary, description, action)
                except (HTTPError, URLError, TimeoutError, OSError):
                    pass
                outcomes.append(action)
            self.send_response(200)
            self.end_headers()
            self.wfile.write(json.dumps({"actions": outcomes}).encode())
        except (ValueError, TypeError, KeyError):
            self.send_error(400)

    def log_message(self, format_string, *args):
        print("self-healer request handled", flush=True)


ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
