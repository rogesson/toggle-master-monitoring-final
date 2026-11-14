import os

from flask import Flask, jsonify, request

app = Flask(__name__)


@app.get("/health")
def health():
    return jsonify(service="targeting-service", status="ok")


@app.get("/")
def index():
    return jsonify(service="targeting-service")


@app.get("/target")
def target():
    user_id = request.args.get("user_id", "anonymous")
    return jsonify(
        service="targeting-service", user_id=user_id, segment="all-users"
    )


if __name__ == "__main__":
    app.run(
        host="0.0.0.0",  # nosec B104 - required for Kubernetes traffic
        port=int(os.getenv("PORT", "8003")),
    )
